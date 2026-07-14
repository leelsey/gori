package anthropic

import (
	"testing"

	"github.com/leelsey/gori"
)

func TestBuildRequestThinkingBudgetHeadroom(t *testing.T) {
	c := New("k")
	out := c.buildRequest(gori.Request{
		Model:     "claude",
		MaxTokens: 1024,
		Thinking:  gori.ThinkingConfig{Mode: gori.ThinkingBudget, Budget: 8000},
	}, false)

	if out.MaxTokens <= 8000 {
		t.Errorf("max_tokens %d must exceed thinking budget 8000", out.MaxTokens)
	}
	if bt, _ := out.Thinking["budget_tokens"].(int); bt != 8000 {
		t.Errorf("budget_tokens = %v, want 8000", out.Thinking["budget_tokens"])
	}
}
