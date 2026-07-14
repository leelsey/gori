package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestServeReturnsDespiteStuckHandler(t *testing.T) {
	cEnd, sEnd := NewPipe()
	srv := NewServer()
	srv.drain = 50 * time.Millisecond
	started := make(chan struct{})
	srv.Handle("stuck", func(context.Context, json.RawMessage) (any, error) {
		close(started)
		select {}
	})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), sEnd) }()

	if err := cEnd.WriteMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"stuck"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-started
	cEnd.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve hung on a handler that ignores ctx")
	}
}

func TestServeCleanEOFLetsHandlerFinish(t *testing.T) {
	cEnd, sEnd := NewPipe()
	srv := NewServer()
	srv.drain = time.Second
	cancelled := make(chan bool, 1)
	srv.Handle("work", func(ctx context.Context, _ json.RawMessage) (any, error) {
		time.Sleep(50 * time.Millisecond)
		cancelled <- ctx.Err() != nil
		return "done", nil
	})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), sEnd) }()

	if err := cEnd.WriteMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"work"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	cEnd.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case wasCancelled := <-cancelled:
		if wasCancelled {
			t.Fatal("handler saw ctx cancellation during the clean-EOF grace period")
		}
	default:
		t.Fatal("handler did not complete before Serve returned")
	}
}

func TestServeDrainsCtxAwareHandler(t *testing.T) {
	cEnd, sEnd := NewPipe()
	srv := NewServer()
	srv.drain = 50 * time.Millisecond
	started := make(chan struct{})
	finished := make(chan struct{})
	srv.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), sEnd) }()

	if err := cEnd.WriteMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"wait"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-started
	cEnd.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler was not cancelled before Serve returned")
	}
}
