package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leelsey/gori/internal/rpc"
)

func TestInitializeAcceptsOlderServerVersion(t *testing.T) {
	cEnd, sEnd := rpc.NewPipe()
	rs := rpc.NewServer()
	rs.Handle("initialize", func(context.Context, json.RawMessage) (any, error) {
		return InitializeResult{ProtocolVersion: "2024-11-05", ServerInfo: Implementation{Name: "old", Version: "1"}}, nil
	})
	rs.Handle("notifications/initialized", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	go func() { _ = rs.Serve(context.Background(), sEnd) }()

	c := NewClient(cEnd)
	if err := c.Initialize(context.Background(), "test-client"); err != nil {
		t.Fatalf("Initialize rejected an older server version: %v", err)
	}
	_ = c.Close()
}
