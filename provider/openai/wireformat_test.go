package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

func TestReasoningEffortFromBudget(t *testing.T) {
	body := captureBody(t, gori.Request{
		Model:    "o3-mini",
		Messages: []gori.Message{gori.UserText("hi")},
		Thinking: gori.ThinkingConfig{Mode: gori.ThinkingBudget, Budget: 1024},
	})
	if !strings.Contains(body, `"reasoning_effort":"low"`) {
		t.Errorf("budget 1024 should map to low effort, got: %s", body)
	}
}

func TestToolResultErrorMarked(t *testing.T) {
	body := captureBody(t, gori.Request{
		Model: "gpt-4o",
		Messages: []gori.Message{
			{Role: gori.RoleTool, Content: []gori.Content{gori.ToolResult{ToolUseID: "t1", Content: "boom", IsError: true}}},
		},
	})
	if !strings.Contains(body, "Error: boom") {
		t.Errorf("tool error not marked on the wire: %s", body)
	}
}

func TestContentlessAssistantGetsContentField(t *testing.T) {
	body := captureBody(t, gori.Request{
		Model: "gpt-4o",
		Messages: []gori.Message{
			{Role: gori.RoleAssistant, Content: []gori.Content{gori.Thinking{Text: "hmm"}}},
		},
	})
	if !strings.Contains(body, `"content":""`) {
		t.Errorf("content-less assistant message missing content field: %s", body)
	}
}

func TestStreamToolCallMissingIDSkipped(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			fl.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := New("k").WithBaseURL(srv.URL)
	resp, err := c.Stream(context.Background(), gori.Request{Model: "gpt-4o", Messages: []gori.Message{gori.UserText("hi")}}, func(gori.StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if n := len(resp.Message.ToolUses()); n != 0 {
		t.Errorf("assembled %d tool uses from an id-less tool call, want 0", n)
	}
}
