// Package netbus bridges gori's in-process event Bus across processes and
// machines via a small central hub over HTTP + Server-Sent Events. Pure standard
// library — no third-party dependencies.
package netbus

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultRingSize = 256

const (
	defaultMaxPublishBytes int64 = 16 << 20         // caps a /publish body
	defaultWriteTimeout          = 10 * time.Second // a stalled SSE consumer must not park its goroutine
)

// Event is the wire form of a bus event.
type Event struct {
	ID     int64           `json:"id,omitempty"`
	Topic  string          `json:"topic"`
	Origin string          `json:"origin,omitempty"`
	Agent  string          `json:"agent,omitempty"`
	Kind   string          `json:"kind,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`

	wire []byte // marshalled once at ID assignment; per-subscriber writes reuse it
}

type subscriber struct {
	topics map[string]bool // empty = all topics
	ch     chan Event
}

// Hub is a central publish/subscribe broker. Clients POST events to /publish and
// receive matching events over an SSE stream from /subscribe. Delivery to a slow
// subscriber is dropped rather than blocking the publisher.
type Hub struct {
	// AuthToken, when non-empty, requires "Authorization: Bearer <AuthToken>" on
	// every request; without it, expose the hub only on trusted interfaces.
	AuthToken string

	// MaxPublishBytes and WriteTimeout override the limits set by NewHub.
	MaxPublishBytes int64
	WriteTimeout    time.Duration

	mu        sync.RWMutex
	subs      map[int]*subscriber
	nextSub   int
	shutdown  bool
	pub       sync.Mutex // serialises ID assignment + broadcast, see handlePublish
	ring      *ring
	heartbeat time.Duration
}

// NewHub returns a ready Hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[int]*subscriber), ring: newRing(defaultRingSize), heartbeat: 30 * time.Second,
		MaxPublishBytes: defaultMaxPublishBytes, WriteTimeout: defaultWriteTimeout}
}

// Handler returns the hub's HTTP handler: POST /publish and GET /subscribe.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/publish", h.handlePublish)
	mux.HandleFunc("/subscribe", h.handleSubscribe)
	return mux
}

// CloseAll closes every active subscriber channel, making their SSE handlers
// return promptly. Register it via http.Server.RegisterOnShutdown so graceful
// shutdown does not hang on long-lived /subscribe streams.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shutdown = true // a /subscribe racing shutdown must not park forever
	for id, s := range h.subs {
		close(s.ch)
		delete(h.subs, id)
	}
}

// SubscriberCount reports the number of active subscribers (useful in tests).
func (h *Hub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// authorised checks the bearer token in constant time; a Hub without AuthToken
// accepts every request.
func (h *Hub) authorised(r *http.Request) bool {
	if h.AuthToken == "" {
		return true
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(h.AuthToken)) == 1
}

func (h *Hub) handlePublish(w http.ResponseWriter, r *http.Request) {
	if !h.authorised(r) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.MaxPublishBytes)
	var ev Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	// bridges treat an empty Origin as locally emitted and would republish it
	if ev.Origin == "" {
		ev.Origin = "external"
	}
	// pub keeps delivery in ID order: subscribers dedupe on "ID <= last seen",
	// so out-of-order delivery would permanently drop the lower-ID event
	h.pub.Lock()
	ev = h.ring.add(ev)
	h.broadcast(ev)
	h.pub.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (h *Hub) broadcast(ev Event) {
	// the read lock keeps removeSub from closing a channel mid-send (panic)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		if len(s.topics) == 0 || s.topics[ev.Topic] {
			select {
			case s.ch <- ev:
			default: // slow subscriber: drop rather than block
			}
		}
	}
}

func (h *Hub) addSub(topics map[string]bool) (int, chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Event, 64)
	if h.shutdown {
		close(ch) // registered after CloseAll: hand back a closed channel so the handler returns
		return -1, ch
	}
	id := h.nextSub
	h.nextSub++
	h.subs[id] = &subscriber{topics: topics, ch: ch}
	return id, ch
}

func (h *Hub) removeSub(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.subs[id]; ok {
		close(s.ch)
		delete(h.subs, id)
	}
}

func (h *Hub) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if !h.authorised(r) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	topics := map[string]bool{}
	if t := r.URL.Query().Get("topics"); t != "" {
		for _, p := range strings.Split(t, ",") {
			if p != "" {
				topics[p] = true
			}
		}
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")

	rc := http.NewResponseController(w)
	id, ch := h.addSub(topics)
	defer h.removeSub(id)

	// Flush the 200 + SSE headers now so a subscriber on a quiet topic is not
	// mistaken for a dead connection until the first event or heartbeat arrives.
	flusher.Flush()

	// exact replayed IDs (not a high-water mark): an event racing the snapshot
	// must be deduped, one evicted before it must still be delivered live
	var replayed map[int64]bool
	if last := lastEventID(r); last > 0 {
		snapshot := h.ring.after(last)
		replayed = make(map[int64]bool, len(snapshot))
		for _, ev := range snapshot {
			replayed[ev.ID] = true
			if len(topics) == 0 || topics[ev.Topic] {
				_ = rc.SetWriteDeadline(time.Now().Add(h.WriteTimeout))
				if writeEvent(w, ev) != nil {
					return
				}
			}
		}
		flusher.Flush()
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if replayed[ev.ID] {
				delete(replayed, ev.ID) // already delivered during replay; avoid a duplicate
				continue
			}
			_ = rc.SetWriteDeadline(time.Now().Add(h.WriteTimeout))
			if writeEvent(w, ev) != nil {
				return // connection broken or stalled; drop the subscriber
			}
			flusher.Flush()
		case <-ticker.C:
			_ = rc.SetWriteDeadline(time.Now().Add(h.WriteTimeout))
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return // connection broken or stalled; drop the subscriber
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

var kindSanitiser = strings.NewReplacer("\r", "", "\n", "")

func writeEvent(w http.ResponseWriter, ev Event) error {
	data := ev.wire
	if data == nil {
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		data = b
	}
	kind := kindSanitiser.Replace(ev.Kind) // SSE values must not contain newlines (frame injection)
	_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, kind, data)
	return err
}

func lastEventID(r *http.Request) int64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("lastEventID")
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// ring buffers recent events for Last-Event-ID replay and assigns IDs; ID and
// insertion share one lock so buffer order equals ID order.
type ring struct {
	mu  sync.Mutex
	seq int64
	buf []Event
	cap int
}

func newRing(c int) *ring { return &ring{cap: c} }

// add assigns the next event ID, appends the event, and returns it with the ID set.
func (r *ring) add(ev Event) Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	ev.ID = r.seq
	r.buf = append(r.buf, ev) // stored without wire so the ring is not doubled
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
	if b, err := json.Marshal(ev); err == nil {
		ev.wire = b // live broadcast marshals once per publish, not per subscriber
	}
	return ev
}

func (r *ring) after(id int64) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, ev := range r.buf {
		if ev.ID > id {
			out = append(out, ev)
		}
	}
	return out
}
