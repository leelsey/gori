package gori

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAgentTruncatedToolCallRecovers(t *testing.T) {
	fp := &fakeProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			Text{Text: "calling"},
			ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)},
		}}, StopReason: StopMaxTokens},
		{Message: AssistantText("final"), StopReason: StopEndTurn},
	}}
	reg := NewRegistry()
	reg.Register(echoTool())
	a := &Agent{Provider: fp, Model: "x", Tools: reg, Session: NewSession()}

	out, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text() != "final" {
		t.Errorf("out = %q, want final", out.Text())
	}
	h := a.Session.History()
	if h[len(h)-1].Role != RoleAssistant {
		t.Errorf("session should end on the final assistant answer")
	}
	uses, results := 0, 0
	for _, m := range h {
		uses += len(m.ToolUses())
		for _, c := range m.Content {
			if _, ok := c.(ToolResult); ok {
				results++
			}
		}
	}
	if uses != results {
		t.Errorf("unbalanced tool_use/tool_result: %d uses, %d results", uses, results)
	}
}

func TestAgentToolUseWithOtherStopNotExecuted(t *testing.T) {
	fp := &fakeProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{"x":`)},
		}}, StopReason: StopOther},
		{Message: AssistantText("final"), StopReason: StopEndTurn},
	}}
	reg := NewRegistry()
	reg.Register(echoTool())
	a := &Agent{Provider: fp, Model: "x", Tools: reg, Session: NewSession()}

	out, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text() != "final" {
		t.Fatalf("out = %q, want final", out.Text())
	}
	seen := false
	for _, m := range a.Session.History() {
		for _, c := range m.Content {
			if r, ok := c.(ToolResult); ok {
				seen = true
				if !r.IsError {
					t.Fatalf("tool result = %q; tool ran on a StopOther response", r.Content)
				}
			}
		}
	}
	if !seen {
		t.Fatal("no tool result answered the dangling tool_use")
	}
}

func TestAgentToolUseWithEndTurnStopExecutes(t *testing.T) {
	fp := &fakeProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{"x":1}`)},
		}}, StopReason: StopEndTurn},
		{Message: AssistantText("final"), StopReason: StopEndTurn},
	}}
	reg := NewRegistry()
	reg.Register(echoTool())
	a := &Agent{Provider: fp, Model: "x", Tools: reg, Session: NewSession()}

	out, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text() != "final" {
		t.Fatalf("out = %q, want final", out.Text())
	}
	if fp.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (tool must have run)", fp.calls)
	}
	for _, m := range a.Session.History() {
		for _, c := range m.Content {
			if r, ok := c.(ToolResult); ok {
				if r.IsError {
					t.Fatalf("tool result is an error %q; tools were dropped instead of executed", r.Content)
				}
				if r.Content != `echoed:{"x":1}` {
					t.Fatalf("tool result = %q, want echoed output", r.Content)
				}
			}
		}
	}
}
