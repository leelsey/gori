package grpc

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/a2a"
)

type stubProvider struct{}

func (stubProvider) Name() string                    { return "stub" }
func (stubProvider) Capabilities() gori.Capabilities { return gori.Capabilities{} }
func (stubProvider) Complete(_ context.Context, req gori.Request) (gori.Response, error) {
	return gori.Response{Message: gori.AssistantText("agent:" + req.Messages[len(req.Messages)-1].Text()), StopReason: gori.StopEndTurn}, nil
}
func (s stubProvider) Stream(ctx context.Context, req gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	_ = fn(gori.StreamEvent{Type: gori.EventTextDelta, Text: "agent:" + req.Messages[len(req.Messages)-1].Text()})
	return gori.Response{Message: gori.AssistantText("agent:" + req.Messages[len(req.Messages)-1].Text()), StopReason: gori.StopEndTurn}, nil
}

func dialBuf(t *testing.T, h a2a.Handler) (*Client, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	RegisterServer(g, h)
	go func() { _ = g.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	cli := NewClient(conn)
	return cli, func() { cli.Close(); g.Stop() }
}

func TestGRPCSendMessage(t *testing.T) {
	cli, cleanup := dialBuf(t, a2a.AgentHandler(&gori.Agent{Provider: stubProvider{}, Model: "x"}))
	defer cleanup()
	out, err := cli.SendMessage(context.Background(), "hi")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if out != "agent:hi" {
		t.Errorf("SendMessage = %q, want agent:hi", out)
	}
}

func TestGRPCStream(t *testing.T) {
	cli, cleanup := dialBuf(t, a2a.AgentHandler(&gori.Agent{Provider: stubProvider{}, Model: "x"}))
	defer cleanup()
	var streamed string
	out, err := cli.SendMessageStream(context.Background(), "yo", func(s string) { streamed += s })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if streamed != "agent:yo" || out != "agent:yo" {
		t.Errorf("streamed=%q out=%q", streamed, out)
	}
}

func TestGRPCAsTool(t *testing.T) {
	cli, cleanup := dialBuf(t, a2a.AgentHandler(&gori.Agent{Provider: stubProvider{}, Model: "x"}))
	defer cleanup()
	out, err := cli.AsTool("remote", "delegate").Execute(context.Background(), json.RawMessage(`{"task":"ping"}`))
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	if out != "agent:ping" {
		t.Errorf("AsTool = %q, want agent:ping", out)
	}
}
