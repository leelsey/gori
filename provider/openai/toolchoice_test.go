package openai

import (
	"encoding/json"
	"testing"

	"github.com/leelsey/gori"
)

func TestBuildRequestToolChoice(t *testing.T) {
	c := New("k")
	out := c.buildRequest(gori.Request{
		Model: "gpt-4o",
		Tools: []gori.ToolDef{{
			Name: "emit", Description: "d", Schema: json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: "emit",
	}, false)

	b, _ := json.Marshal(out.ToolChoice)
	if got := string(b); got != `{"function":{"name":"emit"},"type":"function"}` {
		t.Errorf("tool_choice = %s, want forced function 'emit'", got)
	}

	auto := c.buildRequest(gori.Request{Model: "gpt-4o"}, false)
	if auto.ToolChoice != nil {
		t.Errorf("tool_choice = %v, want nil when unset", auto.ToolChoice)
	}
}
