package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leelsey/gori/internal/rpc"
)

func TestCallToolSurfacesNonTextContent(t *testing.T) {
	a, b := rpc.NewPipe()
	srv := rpc.NewServer()
	srv.Handle("tools/call", func(context.Context, json.RawMessage) (any, error) {
		return CallToolResult{Content: []Content{
			{Type: "text", Text: "hello "},
			{Type: "image", MimeType: "image/png", Data: "AAAA"},
		}}, nil
	})
	go func() { _ = srv.Serve(context.Background(), a) }()

	c := NewClient(b)
	defer c.Close()
	out, isErr, err := c.CallTool(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if isErr {
		t.Error("unexpected isError")
	}
	if out != "hello [image image/png]" {
		t.Errorf("got %q, want %q", out, "hello [image image/png]")
	}
}
