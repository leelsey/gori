package google

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

func TestComplete(t *testing.T) {
	var body struct {
		Contents []struct {
			Role  string           `json:"role"`
			Parts []map[string]any `json:"parts"`
		} `json:"contents"`
		SystemInstruction *struct {
			Parts []map[string]any `json:"parts"`
		} `json:"systemInstruction"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "key" {
			t.Errorf("missing api key header")
		}
		if !strings.Contains(r.URL.Path, ":generateContent") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"},{"functionCall":{"name":"echo","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5}}`)
	}))
	defer srv.Close()

	c := New("key").WithBaseURL(srv.URL)
	resp, err := c.Complete(context.Background(), gori.Request{
		Model:  "gemini-x",
		System: "sys",
		Messages: []gori.Message{
			gori.UserText("hello"),
			{Role: gori.RoleAssistant, Content: []gori.Content{gori.ToolUse{ID: "c0", Name: "echo", Input: json.RawMessage(`{"x":1}`)}}},
			{Role: gori.RoleTool, Content: []gori.Content{gori.ToolResult{ToolUseID: "c0", Content: "echoed"}}},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != gori.StopToolUse {
		t.Errorf("stop = %q (tool call should force tool_use)", resp.StopReason)
	}
	if resp.Message.Text() != "hi" {
		t.Errorf("text = %q", resp.Message.Text())
	}
	uses := resp.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "echo" {
		t.Errorf("tool uses = %+v", uses)
	}
	if body.SystemInstruction == nil || body.SystemInstruction.Parts[0]["text"] != "sys" {
		t.Errorf("system instruction not mapped: %+v", body.SystemInstruction)
	}
	last := body.Contents[len(body.Contents)-1]
	fr, ok := last.Parts[0]["functionResponse"].(map[string]any)
	if !ok || fr["name"] != "echo" {
		t.Errorf("function response name not resolved: %+v", last.Parts[0])
	}
}

func TestStream(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"echo","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":9}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("expected alt=sse, got %q", r.URL.RawQuery)
		}
		w.Header().Set("content-type", "text/event-stream")
		for _, ch := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", ch)
		}
	}))
	defer srv.Close()

	c := New("key").WithBaseURL(srv.URL)
	var text string
	resp, err := c.Stream(context.Background(), gori.Request{Model: "gemini-x", Messages: []gori.Message{gori.UserText("hi")}}, func(ev gori.StreamEvent) error {
		if ev.Type == gori.EventTextDelta {
			text += ev.Text
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "Hello" {
		t.Errorf("text = %q", text)
	}
	uses := resp.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "echo" {
		t.Errorf("tool uses = %+v", uses)
	}
	if resp.StopReason != gori.StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
}
