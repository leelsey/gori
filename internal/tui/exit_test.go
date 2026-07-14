package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/leelsey/gori"
)

type repeatReader struct {
	s []byte
	i int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	for n := range p {
		p[n] = r.s[r.i]
		r.i = (r.i + 1) % len(r.s)
	}
	return len(p), nil
}

type failProvider struct{ emitTool bool }

func (failProvider) Name() string                    { return "f" }
func (failProvider) Capabilities() gori.Capabilities { return gori.Capabilities{Tools: true} }
func (failProvider) Complete(context.Context, gori.Request) (gori.Response, error) {
	return gori.Response{}, errors.New("boom")
}
func (p failProvider) Stream(ctx context.Context, _ gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	if p.emitTool {
		_ = fn(gori.StreamEvent{Type: gori.EventToolStart, ToolName: "search"})
		return gori.Response{Message: gori.AssistantText("done")}, nil
	}
	return gori.Response{}, errors.New("boom")
}

func TestRunExitsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	agent := &gori.Agent{Provider: failProvider{}, Model: "x", Session: gori.NewSession()}

	done := make(chan error, 1)
	go func() { done <- Run(ctx, agent, &repeatReader{s: []byte("hi\n")}, io.Discard, nil) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit on context done (spun on a dead session)")
	}
}

func TestRunShowsToolActivity(t *testing.T) {
	var out strings.Builder
	agent := &gori.Agent{Provider: failProvider{emitTool: true}, Model: "x", Session: gori.NewSession()}
	if err := Run(context.Background(), agent, strings.NewReader("hi\n"), &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "search") {
		t.Errorf("tool activity not shown in TUI output: %q", out.String())
	}
}
