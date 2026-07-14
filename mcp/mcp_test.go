package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/internal/rpc"
)

func echoTool() gori.Tool {
	return gori.ToolFunc{
		NameVal:        "echo",
		DescriptionVal: "echoes its input",
		SchemaVal:      json.RawMessage(`{"type":"object"}`),
		Fn: func(_ context.Context, in json.RawMessage) (string, error) {
			return "echoed:" + string(in), nil
		},
	}
}

func newPair(t *testing.T, srv *Server) *Client {
	t.Helper()
	cEnd, sEnd := rpc.NewPipe()
	go func() { _ = srv.Serve(context.Background(), sEnd) }()
	client := NewClient(cEnd)
	t.Cleanup(func() { client.Close() })
	if err := client.Initialize(context.Background(), "test-client"); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return client
}

func TestServerToolsListAndCall(t *testing.T) {
	srv := NewServer("test-server", "0.1")
	srv.AddTools(echoTool())
	client := newPair(t, srv)

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}

	out, isErr, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	if err != nil || isErr {
		t.Fatalf("call: out=%q isErr=%v err=%v", out, isErr, err)
	}
	if out != `echoed:{"x":1}` {
		t.Errorf("call result = %q", out)
	}

	if _, isErr, _ := client.CallTool(context.Background(), "nope", nil); !isErr {
		t.Errorf("expected isError for unknown tool")
	}
}

func TestClientToolsBridge(t *testing.T) {
	srv := NewServer("test-server", "0.1")
	srv.AddTools(echoTool())
	client := newPair(t, srv)

	gtools, err := client.Tools(context.Background())
	if err != nil || len(gtools) != 1 {
		t.Fatalf("bridge tools = %d err=%v", len(gtools), err)
	}
	res, err := gtools[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || res != `echoed:{}` {
		t.Fatalf("bridged execute = %q err=%v", res, err)
	}
}

type stubProvider struct{}

func (stubProvider) Name() string                    { return "stub" }
func (stubProvider) Capabilities() gori.Capabilities { return gori.Capabilities{} }
func (stubProvider) Complete(_ context.Context, req gori.Request) (gori.Response, error) {
	last := req.Messages[len(req.Messages)-1].Text()
	return gori.Response{Message: gori.AssistantText("ran:" + last), StopReason: gori.StopEndTurn}, nil
}
func (s stubProvider) Stream(ctx context.Context, req gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	return s.Complete(ctx, req)
}

func TestServerAgentAsTool(t *testing.T) {
	srv := NewServer("test-server", "0.1")
	srv.AddAgent("assistant", "runs the assistant agent", &gori.Agent{Provider: stubProvider{}, Model: "x"})
	client := newPair(t, srv)

	out, isErr, err := client.CallTool(context.Background(), "assistant", json.RawMessage(`{"input":"hello"}`))
	if err != nil || isErr {
		t.Fatalf("agent call: out=%q isErr=%v err=%v", out, isErr, err)
	}
	if out != "ran:hello" {
		t.Errorf("agent result = %q", out)
	}
}
