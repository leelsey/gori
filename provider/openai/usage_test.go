package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leelsey/gori"
)

func TestCompleteUsageDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":25,"prompt_tokens_details":{"cached_tokens":12},"completion_tokens_details":{"reasoning_tokens":20}}}`)
	}))
	defer srv.Close()

	c := New("key").WithBaseURL(srv.URL)
	resp, err := c.Complete(context.Background(), gori.Request{Model: "m", Messages: []gori.Message{gori.UserText("hello")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	want := gori.Usage{InputTokens: 30, OutputTokens: 5, ThinkingTokens: 20, CacheReadTokens: 12}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestUsageDetailsClamped(t *testing.T) {
	u := apiUsage{CompletionTokens: 5}
	u.CompletionTokensDetails.ReasoningTokens = 9
	got := u.toUsage()
	if got.OutputTokens != 0 || got.ThinkingTokens != 9 {
		t.Errorf("clamped usage = %+v", got)
	}
}
