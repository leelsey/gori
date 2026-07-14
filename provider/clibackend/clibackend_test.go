package clibackend

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

func requireCat(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not available")
	}
}

func TestCompleteStdin(t *testing.T) {
	requireCat(t)
	c := New(Config{Command: "cat"})
	resp, err := c.Complete(context.Background(), gori.Request{
		System:   "be nice",
		Messages: []gori.Message{gori.UserText("hi there")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := resp.Message.Text()
	if !strings.Contains(got, "[system] be nice") || !strings.Contains(got, "[user] hi there") {
		t.Errorf("rendered prompt not echoed: %q", got)
	}
	if resp.StopReason != gori.StopEndTurn {
		t.Errorf("stop = %q", resp.StopReason)
	}
}

func TestStream(t *testing.T) {
	requireCat(t)
	c := New(Config{Command: "cat"})
	var streamed string
	resp, err := c.Stream(context.Background(), gori.Request{Messages: []gori.Message{gori.UserText("stream me")}}, func(ev gori.StreamEvent) error {
		if ev.Type == gori.EventTextDelta {
			streamed += ev.Text
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(streamed, "stream me") {
		t.Errorf("streamed = %q", streamed)
	}
	if !strings.Contains(resp.Message.Text(), "stream me") {
		t.Errorf("assembled = %q", resp.Message.Text())
	}
}
