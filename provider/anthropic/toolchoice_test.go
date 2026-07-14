package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/leelsey/gori"
)

func TestBuildRequestToolChoice(t *testing.T) {
	c := New("k")
	out := c.buildRequest(gori.Request{
		Model:     "claude",
		MaxTokens: 1024,
		Tools: []gori.ToolDef{{
			Name: "emit", Description: "d", Schema: json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: "emit",
	}, false)

	b, _ := json.Marshal(out.ToolChoice)
	if got := string(b); got != `{"name":"emit","type":"tool"}` {
		t.Errorf("tool_choice = %s, want forced tool 'emit'", got)
	}

	// Empty ToolChoice must omit the field (auto).
	auto := c.buildRequest(gori.Request{Model: "claude", MaxTokens: 10}, false)
	if auto.ToolChoice != nil {
		t.Errorf("tool_choice = %v, want nil when unset", auto.ToolChoice)
	}
}
