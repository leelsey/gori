package google

import (
	"testing"

	"github.com/leelsey/gori"
)

func TestBuildContentsThinkingRoundTrip(t *testing.T) {
	req := gori.Request{Messages: []gori.Message{
		gori.UserText("hi"),
		{Role: gori.RoleAssistant, Content: []gori.Content{gori.Thinking{Text: "reasoning"}}},
		gori.UserText("continue"),
	}}
	contents := buildContents(req)
	for i, c := range contents {
		if len(c.Parts) == 0 {
			t.Errorf("content %d has empty parts (Gemini would reject it)", i)
		}
	}
	foundThought := false
	for _, c := range contents {
		for _, p := range c.Parts {
			if p.Thought && p.Text == "reasoning" {
				foundThought = true
			}
		}
	}
	if !foundThought {
		t.Error("thinking block was not re-sent as a thought part")
	}
}
