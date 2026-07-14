package gori

import (
	"context"
	"errors"
	"testing"
)

func TestLoopHonoursCancelledContext(t *testing.T) {
	fp := &fakeProvider{responses: []Response{{Message: Message{Role: RoleAssistant}}}}
	a := &Agent{Provider: fp, Model: "x", Tools: NewRegistry(), Session: NewSession()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.Run(ctx, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if fp.calls != 0 {
		t.Errorf("provider called %d times despite cancelled ctx", fp.calls)
	}
}

func TestCancelledRunDoesNotPoisonSession(t *testing.T) {
	fp := &fakeProvider{responses: []Response{{Message: Message{Role: RoleAssistant}}}}
	a := &Agent{Provider: fp, Model: "x", Tools: NewRegistry(), Session: NewSession()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := a.Run(ctx, "hi"); err == nil {
		t.Fatal("expected ctx error")
	}
	if n := len(a.Session.History()); n != 0 {
		t.Errorf("session has %d messages after cancelled run; want 0", n)
	}
}
