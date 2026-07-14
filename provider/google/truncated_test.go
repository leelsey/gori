package google

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

func TestStreamTruncatedWithoutFinishErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`+"\n\n")
	}))
	defer srv.Close()

	c := New("k").WithBaseURL(srv.URL)
	_, err := c.Stream(context.Background(), gori.Request{Model: "m", Messages: []gori.Message{gori.UserText("hi")}}, func(gori.StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("truncated stream returned nil error (silent partial answer)")
	}
}

func TestStreamPromptBlockedSurfacesBlockReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, `data: {"promptFeedback":{"blockReason":"SAFETY"}}`+"\n\n")
	}))
	defer srv.Close()

	c := New("k").WithBaseURL(srv.URL)
	_, err := c.Stream(context.Background(), gori.Request{Model: "m", Messages: []gori.Message{gori.UserText("hi")}}, func(gori.StreamEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "SAFETY") {
		t.Fatalf("err = %v, want a prompt-blocked error naming the reason", err)
	}
}

func TestStreamWithFinishSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"full"}]},"finishReason":"STOP"}]}`+"\n\n")
	}))
	defer srv.Close()

	c := New("k").WithBaseURL(srv.URL)
	resp, err := c.Stream(context.Background(), gori.Request{Model: "m", Messages: []gori.Message{gori.UserText("hi")}}, func(gori.StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Message.Text() != "full" {
		t.Errorf("text = %q, want full", resp.Message.Text())
	}
}
