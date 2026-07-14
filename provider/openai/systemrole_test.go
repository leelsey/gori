package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

func TestSystemMessageWithImageRejected(t *testing.T) {
	c := New("k").WithBaseURL("http://127.0.0.1:1")
	req := gori.Request{Model: "m", Messages: []gori.Message{
		{Role: gori.RoleSystem, Content: []gori.Content{gori.Image{MediaType: "image/png", Data: []byte{1}}}},
		gori.UserText("hi"),
	}}
	if _, err := c.Complete(context.Background(), req); err == nil || !strings.Contains(err.Error(), "text-only") {
		t.Fatalf("Complete err = %v, want a text-only system message error", err)
	}
	_, err := c.Stream(context.Background(), req, func(gori.StreamEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "text-only") {
		t.Fatalf("Stream err = %v, want a text-only system message error", err)
	}
}
