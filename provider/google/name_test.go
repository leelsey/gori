package google

import (
	"testing"

	"github.com/leelsey/gori"
)

func TestFunctionResponseNameFromToolResult(t *testing.T) {
	req := gori.Request{Messages: []gori.Message{
		gori.UserText("hi"),
		{Role: gori.RoleTool, Content: []gori.Content{
			gori.ToolResult{ToolUseID: "call_0", Name: "lookup", Content: "ok"},
		}},
	}}
	found := false
	for _, c := range buildContents(req) {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.Name == "lookup" {
				found = true
			}
		}
	}
	if !found {
		t.Error("functionResponse.name not resolved from ToolResult.Name after compaction")
	}
}
