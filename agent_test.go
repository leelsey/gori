package gori

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeProvider struct {
	responses []Response
	calls     int
	lastReq   Request
}

func (f *fakeProvider) Name() string               { return "fake" }
func (f *fakeProvider) Capabilities() Capabilities { return Capabilities{Tools: true} }

func (f *fakeProvider) Complete(_ context.Context, req Request) (Response, error) {
	f.lastReq = req
	r := f.responses[f.calls]
	f.calls++
	return r, nil
}

func (f *fakeProvider) Stream(ctx context.Context, req Request, fn func(StreamEvent) error) (Response, error) {
	r, err := f.Complete(ctx, req)
	if err != nil {
		return Response{}, err
	}
	_ = fn(StreamEvent{Type: EventDone})
	return r, nil
}

func echoTool() Tool {
	return ToolFunc{
		NameVal:        "echo",
		DescriptionVal: "echoes its input",
		SchemaVal:      json.RawMessage(`{"type":"object"}`),
		Fn: func(_ context.Context, input json.RawMessage) (string, error) {
			return "echoed:" + string(input), nil
		},
	}
}

func TestAgentReActLoop(t *testing.T) {
	fp := &fakeProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{"x":1}`)},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    AssistantText("done"),
			StopReason: StopEndTurn,
		},
	}}

	reg := NewRegistry()
	reg.Register(echoTool())
	agent := &Agent{Provider: fp, Model: "x", Tools: reg, Session: NewSession()}

	out, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Text() != "done" {
		t.Fatalf("final text = %q, want %q", out.Text(), "done")
	}
	if fp.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", fp.calls)
	}
	hist := agent.Session.History()
	if len(hist) != 4 {
		t.Fatalf("history len = %d, want 4", len(hist))
	}
	if hist[2].Role != RoleTool {
		t.Errorf("history[2].Role = %q, want %q", hist[2].Role, RoleTool)
	}
	tr, ok := hist[2].Content[0].(ToolResult)
	if !ok || tr.ToolUseID != "c1" || tr.Content != `echoed:{"x":1}` {
		t.Errorf("tool result wrong: %+v", hist[2].Content[0])
	}
}

func TestAgentUnknownTool(t *testing.T) {
	fp := &fakeProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				ToolUse{ID: "c1", Name: "missing", Input: json.RawMessage(`{}`)},
			}},
			StopReason: StopToolUse,
		},
		{Message: AssistantText("ok"), StopReason: StopEndTurn},
	}}
	agent := &Agent{Provider: fp, Model: "x", Tools: NewRegistry(), Session: NewSession()}
	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	hist := agent.Session.History()
	tr := hist[2].Content[0].(ToolResult)
	if !tr.IsError {
		t.Errorf("expected error tool result for unknown tool")
	}
}

func TestAgentTotalUsage(t *testing.T) {
	fp := &fakeProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)},
			}},
			StopReason: StopToolUse,
			Usage:      Usage{InputTokens: 10, OutputTokens: 5},
		},
		{
			Message:    AssistantText("done"),
			StopReason: StopEndTurn,
			Usage:      Usage{InputTokens: 20, OutputTokens: 7},
		},
	}}
	reg := NewRegistry()
	reg.Register(echoTool())
	agent := &Agent{Provider: fp, Model: "x", Tools: reg, Session: NewSession()}

	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if agent.TotalUsage.InputTokens != 30 {
		t.Errorf("TotalUsage.InputTokens = %d, want 30", agent.TotalUsage.InputTokens)
	}
	if agent.TotalUsage.OutputTokens != 12 {
		t.Errorf("TotalUsage.OutputTokens = %d, want 12", agent.TotalUsage.OutputTokens)
	}
}

func TestAgentMaxSteps(t *testing.T) {
	loop := Response{
		Message: Message{Role: RoleAssistant, Content: []Content{
			ToolUse{ID: "c", Name: "echo", Input: json.RawMessage(`{}`)},
		}},
		StopReason: StopToolUse,
	}
	resps := make([]Response, 10)
	for i := range resps {
		resps[i] = loop
	}
	reg := NewRegistry()
	reg.Register(echoTool())
	agent := &Agent{Provider: &fakeProvider{responses: resps}, Model: "x", Tools: reg, Session: NewSession(), MaxSteps: 3}
	if _, err := agent.Run(context.Background(), "hi"); err == nil {
		t.Fatalf("expected max-steps error")
	}
	h := agent.Session.History()
	if n := len(h); n == 0 || h[n-1].Role != RoleTool {
		t.Fatalf("session should end with a tool result, not a dangling tool_use")
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

func TestAgentToolPanicRecovered(t *testing.T) {
	panicTool := ToolFunc{
		NameVal:   "boom",
		SchemaVal: json.RawMessage(`{"type":"object"}`),
		Fn:        func(context.Context, json.RawMessage) (string, error) { panic("kaboom") },
	}
	for _, parallel := range []bool{false, true} {
		fp := &fakeProvider{responses: []Response{
			{Message: Message{Role: RoleAssistant, Content: []Content{
				ToolUse{ID: "c1", Name: "boom", Input: json.RawMessage(`{}`)},
				ToolUse{ID: "c2", Name: "boom", Input: json.RawMessage(`{}`)},
			}}, StopReason: StopToolUse},
			{Message: AssistantText("recovered"), StopReason: StopEndTurn},
		}}
		reg := NewRegistry()
		reg.Register(panicTool)
		a := &Agent{Provider: fp, Model: "x", Tools: reg, Session: NewSession(), ParallelTools: parallel}
		out, err := a.Run(context.Background(), "hi")
		if err != nil {
			t.Fatalf("parallel=%v: Run errored instead of recovering: %v", parallel, err)
		}
		if out.Text() != "recovered" {
			t.Errorf("parallel=%v: out=%q, want recovered", parallel, out.Text())
		}
	}
}
