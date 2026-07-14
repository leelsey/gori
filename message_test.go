package gori

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMessageJSONRoundTrip(t *testing.T) {
	in := Message{
		Role: RoleAssistant,
		Content: []Content{
			Thinking{Text: "let me think", Signature: "sig123"},
			Text{Text: "hello"},
			ToolUse{ID: "t1", Name: "search", Input: json.RawMessage(`{"q":"go"}`)},
			ToolResult{ToolUseID: "t1", Content: "result", IsError: false},
			Plan{Steps: []string{"a", "b"}},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Message
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Role != in.Role || len(out.Content) != len(in.Content) {
		t.Fatalf("round-trip mismatch: got %+v", out)
	}
	if out.Content[1].(Text).Text != "hello" {
		t.Errorf("text block lost: %+v", out.Content[1])
	}
	tu := out.Content[2].(ToolUse)
	if tu.ID != "t1" || tu.Name != "search" || !bytes.Equal(tu.Input, json.RawMessage(`{"q":"go"}`)) {
		t.Errorf("tool_use block lost: %+v", tu)
	}
	if out.Content[0].(Thinking).Signature != "sig123" {
		t.Errorf("thinking signature lost")
	}
}

func TestMessageHelpers(t *testing.T) {
	m := Message{Role: RoleAssistant, Content: []Content{
		Text{Text: "a"},
		ToolUse{ID: "x", Name: "t"},
		Text{Text: "b"},
	}}
	if got := m.Text(); got != "ab" {
		t.Errorf("Text() = %q, want %q", got, "ab")
	}
	if uses := m.ToolUses(); len(uses) != 1 || uses[0].ID != "x" {
		t.Errorf("ToolUses() = %+v", uses)
	}
}
