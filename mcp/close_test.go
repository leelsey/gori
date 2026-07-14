package mcp

import (
	"context"
	"testing"
	"time"
)

func TestClientCloseKillsHungChild(t *testing.T) {
	c, err := Dial(context.Background(), "sleep", "30")
	if err != nil {
		t.Skipf("sleep unavailable: %v", err)
	}
	c.CloseGrace = 50 * time.Millisecond
	done := make(chan struct{})
	go func() { _ = c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung on a child that ignores stdin EOF")
	}
}
