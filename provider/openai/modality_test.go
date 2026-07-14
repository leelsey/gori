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

func TestAudioOutputModalityRequested(t *testing.T) {
	var body struct {
		Modalities []string `json:"modalities"`
		Audio      *struct {
			Format string `json:"format"`
		} `json:"audio"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	_, err := New("k").WithBaseURL(srv.URL).Complete(context.Background(), gori.Request{
		Model:              "m",
		Messages:           []gori.Message{gori.UserText("speak")},
		ResponseModalities: []string{"audio"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	has := map[string]bool{}
	for _, m := range body.Modalities {
		has[m] = true
	}
	if !has["text"] || !has["audio"] {
		t.Errorf("modalities not opted in: %v", body.Modalities)
	}
	if body.Audio == nil || body.Audio.Format != "wav" {
		t.Errorf("audio output config missing: %+v", body.Audio)
	}
}
