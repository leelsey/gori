package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

func TestAudioContentRejected(t *testing.T) {
	c := New("k").WithBaseURL("http://127.0.0.1:1")
	req := gori.Request{Model: "m", Messages: []gori.Message{
		{Role: gori.RoleUser, Content: []gori.Content{gori.Audio{MediaType: "audio/wav", Data: []byte{1}}}},
	}}
	if _, err := c.Complete(context.Background(), req); err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("Complete err = %v, want an audio-not-supported error", err)
	}
	_, err := c.Stream(context.Background(), req, func(gori.StreamEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("Stream err = %v, want an audio-not-supported error", err)
	}
}
