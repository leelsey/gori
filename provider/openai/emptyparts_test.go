package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leelsey/gori"
)

func TestEmptyUserPartsNotEmptyArray(t *testing.T) {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c := New("key").WithBaseURL(srv.URL)
	_, err := c.Complete(context.Background(), gori.Request{
		Model: "m",
		Messages: []gori.Message{{Role: gori.RoleUser, Content: []gori.Content{
			gori.Audio{MediaType: "audio/wav"},
		}}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for _, m := range body.Messages {
		if m.Role == "user" && string(m.Content) == "[]" {
			t.Errorf("empty user parts serialised as [], which OpenAI rejects")
		}
	}
}
