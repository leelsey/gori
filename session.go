package gori

import (
	"encoding/json"
	"os"
	"sync"
)

// Session holds conversation history plus arbitrary key/value state. It is safe
// for concurrent use and can be persisted to a JSON file.
type Session struct {
	mu      sync.Mutex
	history []Message
	state   map[string]any
}

// NewSession returns an empty Session.
func NewSession() *Session {
	return &Session{state: make(map[string]any)}
}

// Append adds messages to the history.
func (s *Session) Append(msgs ...Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
}

// History returns a copy of the message history, deep enough that replacing
// elements of a returned message does not write through to the session.
func (s *Session) History() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.history))
	for i, m := range s.history {
		out[i] = m
		if len(m.Content) > 0 {
			out[i].Content = append([]Content(nil), m.Content...)
		}
	}
	return out
}

// Trim keeps roughly the last keepLast messages (keepLast <= 0 clears all).
// The cut is aligned to a user-turn boundary — providers reject histories that
// do not start with a user message — and a turn is never split, so the window
// can exceed keepLast by up to one turn (bounded by MaxSteps). Dropping always
// reallocates so trimmed content can be garbage-collected.
func (s *Session) Trim(keepLast int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if keepLast <= 0 {
		s.history = nil
		return
	}
	h := s.history
	start := 0
	if len(h) > keepLast {
		start = len(h) - keepLast
	}
	for start > 0 && h[start].Role != RoleUser {
		start--
	}
	for start < len(h) && h[start].Role != RoleUser {
		start++ // no user turn at or before the window: fall forward
	}
	if start == 0 {
		return
	}
	s.history = append([]Message(nil), h[start:]...)
}

// DropBefore removes the first n messages from the history. n <= 0 is a no-op;
// n >= len(history) clears it. A fresh backing array is allocated so the dropped
// messages can be garbage-collected.
func (s *Session) DropBefore(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		return
	}
	if n >= len(s.history) {
		s.history = nil
		return
	}
	s.history = append([]Message(nil), s.history[n:]...)
}

// Set stores a state value. It works on a zero-value Session, matching
// Append/History/Trim.
func (s *Session) Set(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		s.state = make(map[string]any)
	}
	s.state[key] = value
}

// Get retrieves a state value.
func (s *Session) Get(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.state[key]
	return v, ok
}

type sessionFile struct {
	History []Message      `json:"history"`
	State   map[string]any `json:"state,omitempty"`
}

// Save writes the session to path as JSON with 0600 permissions.
func (s *Session) Save(path string) error {
	// Marshal under the lock: the state map is shared and a concurrent Set would
	// otherwise race the marshaller (concurrent map iteration and write).
	s.mu.Lock()
	b, err := json.MarshalIndent(sessionFile{History: s.history, State: s.state}, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Load replaces the session contents with those read from path.
func (s *Session) Load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var sf sessionFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = sf.History
	if sf.State != nil {
		s.state = sf.State
	} else {
		s.state = make(map[string]any)
	}
	return nil
}
