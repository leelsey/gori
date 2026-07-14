package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/leelsey/gori/internal/jsonrpc"
)

// maxConcurrentRequests bounds in-flight handlers; excess requests apply
// backpressure on the read loop.
const maxConcurrentRequests = 64

// drainTimeout bounds how long Serve waits for in-flight handlers after the
// transport closes.
const drainTimeout = 5 * time.Second

// Handler processes a JSON-RPC method call. The returned value is marshalled as
// the result; returning a *jsonrpc.Error controls the wire error code.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server dispatches incoming JSON-RPC requests to registered handlers.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	drain    time.Duration // handler drain bound on Serve return; tests shrink it
}

// NewServer returns an empty Server.
func NewServer() *Server { return &Server{handlers: make(map[string]Handler), drain: drainTimeout} }

// Handle registers h for method.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Serve reads requests from t until the transport closes (io.EOF) or a transport
// error occurs, dispatching each and writing responses.
func (s *Server) Serve(ctx context.Context, t Transport) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// On clean EOF (half-closed peer awaiting responses) handlers get s.drain to
	// finish before cancellation, then s.drain more to unwind; a ctx-ignoring
	// handler is abandoned rather than hanging Serve.
	cleanEOF := false
	defer func() {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		if cleanEOF {
			select {
			case <-done:
			case <-time.After(s.drain):
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(s.drain):
		}
	}()
	sem := make(chan struct{}, maxConcurrentRequests)
	for {
		msg, err := t.ReadMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				cleanEOF = true
				return nil
			}
			if errors.Is(err, io.ErrClosedPipe) {
				return nil // locally closed: tear down without the pre-cancel grace
			}
			return err
		}
		var req jsonrpc.Request
		if err := json.Unmarshal(msg, &req); err != nil {
			_ = s.write(t, jsonrpc.ErrorResponse(nil, jsonrpc.CodeParseError, "parse error"))
			continue
		}
		if req.JSONRPC != jsonrpc.Version {
			if !req.IsNotification() {
				_ = s.write(t, jsonrpc.ErrorResponse(req.ID, jsonrpc.CodeInvalidRequest, `invalid request: jsonrpc must be "2.0"`))
			}
			continue
		}
		// concurrent dispatch avoids head-of-line blocking; the transport's writer
		// mutex serialises responses
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		wg.Add(1)
		go func(req jsonrpc.Request) {
			defer wg.Done()
			defer func() { <-sem }()
			if resp := s.handle(ctx, &req); resp != nil {
				_ = s.write(t, resp)
			}
		}(req)
	}
}

func (s *Server) handle(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	s.mu.RLock()
	h, ok := s.handlers[req.Method]
	s.mu.RUnlock()
	if !ok {
		if req.IsNotification() {
			return nil
		}
		return jsonrpc.ErrorResponse(req.ID, jsonrpc.CodeMethodNotFound, "method not found: "+req.Method)
	}
	result, err := h(ctx, req.Params)
	if req.IsNotification() {
		return nil
	}
	if err != nil {
		var je *jsonrpc.Error
		if errors.As(err, &je) {
			return &jsonrpc.Response{JSONRPC: jsonrpc.Version, ID: req.ID, Error: je}
		}
		return jsonrpc.ErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error())
	}
	raw, mErr := json.Marshal(result)
	if mErr != nil {
		return jsonrpc.ErrorResponse(req.ID, jsonrpc.CodeInternalError, mErr.Error())
	}
	return jsonrpc.ResultResponse(req.ID, raw)
}

func (s *Server) write(t Transport, resp *jsonrpc.Response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return t.WriteMessage(b)
}
