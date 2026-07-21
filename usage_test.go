package gori

import (
	"context"
	"encoding/json"
	"testing"
)

func TestUsageAddTotal(t *testing.T) {
	var u Usage
	u.Add(Usage{InputTokens: 10, OutputTokens: 5, ThinkingTokens: 2, CacheReadTokens: 4, CacheWriteTokens: 3})
	u.Add(Usage{InputTokens: 1, OutputTokens: 1, ThinkingTokens: 1, CacheReadTokens: 1, CacheWriteTokens: 1})
	want := Usage{InputTokens: 11, OutputTokens: 6, ThinkingTokens: 3, CacheReadTokens: 5, CacheWriteTokens: 4}
	if u != want {
		t.Errorf("Add = %+v, want %+v", u, want)
	}
	if got := u.Total(); got != 20 {
		t.Errorf("Total = %d, want 20", got)
	}
}

func TestAgentSessionAndStepUsage(t *testing.T) {
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
			Usage:      Usage{InputTokens: 20, OutputTokens: 7, CacheReadTokens: 8},
		},
		{
			Message:    AssistantText("again"),
			StopReason: StopEndTurn,
			Usage:      Usage{InputTokens: 40, OutputTokens: 3},
		},
	}}
	reg := NewRegistry()
	reg.Register(echoTool())
	agent := &Agent{Provider: fp, Model: "x", Tools: reg, Session: NewSession()}

	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if len(agent.StepUsage) != 2 || agent.StepUsage[0].InputTokens != 10 || agent.StepUsage[1].CacheReadTokens != 8 {
		t.Errorf("StepUsage after run 1 = %+v", agent.StepUsage)
	}
	if agent.TotalUsage != (Usage{InputTokens: 30, OutputTokens: 12, CacheReadTokens: 8}) {
		t.Errorf("TotalUsage after run 1 = %+v", agent.TotalUsage)
	}

	if _, err := agent.Run(context.Background(), "more"); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(agent.StepUsage) != 1 || agent.StepUsage[0].InputTokens != 40 {
		t.Errorf("StepUsage after run 2 = %+v", agent.StepUsage)
	}
	if agent.TotalUsage != (Usage{InputTokens: 40, OutputTokens: 3}) {
		t.Errorf("TotalUsage after run 2 = %+v", agent.TotalUsage)
	}
	if agent.SessionUsage != (Usage{InputTokens: 70, OutputTokens: 15, CacheReadTokens: 8}) {
		t.Errorf("SessionUsage = %+v", agent.SessionUsage)
	}

	cl := agent.Clone()
	if cl.SessionUsage != (Usage{}) || cl.TotalUsage != (Usage{}) || cl.StepUsage != nil {
		t.Errorf("Clone kept usage: %+v %+v %+v", cl.SessionUsage, cl.TotalUsage, cl.StepUsage)
	}
}
