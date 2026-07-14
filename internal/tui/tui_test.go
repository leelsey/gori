package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

type stubProvider struct{}

func (stubProvider) Name() string                    { return "stub" }
func (stubProvider) Capabilities() gori.Capabilities { return gori.Capabilities{} }
func (stubProvider) Complete(_ context.Context, req gori.Request) (gori.Response, error) {
	return gori.Response{Message: gori.AssistantText("echo:" + req.Messages[len(req.Messages)-1].Text()), StopReason: gori.StopEndTurn}, nil
}
func (s stubProvider) Stream(ctx context.Context, req gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	last := req.Messages[len(req.Messages)-1].Text()
	_ = fn(gori.StreamEvent{Type: gori.EventTextDelta, Text: "echo:" + last})
	return gori.Response{Message: gori.AssistantText("echo:" + last), StopReason: gori.StopEndTurn}, nil
}

func TestRunInteractive(t *testing.T) {
	agent := &gori.Agent{Provider: stubProvider{}, Model: "x", Session: gori.NewSession()}
	in := strings.NewReader("hello\n/reset\nworld\n/exit\nnever\n")
	var out bytes.Buffer

	if err := Run(context.Background(), agent, in, &out, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "echo:hello") {
		t.Errorf("first turn not echoed: %q", s)
	}
	if !strings.Contains(s, "session reset") {
		t.Errorf("/reset not handled")
	}
	if !strings.Contains(s, "echo:world") {
		t.Errorf("second turn not echoed")
	}
	if strings.Contains(s, "echo:never") {
		t.Errorf("/exit did not stop the loop")
	}
	if strings.Contains(s, "\033[") {
		t.Errorf("ANSI escapes leaked into non-TTY output")
	}
}
