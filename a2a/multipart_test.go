package a2a

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/leelsey/gori"
)

type captureProvider struct{ got gori.Message }

func (c *captureProvider) Name() string                    { return "capture" }
func (c *captureProvider) Capabilities() gori.Capabilities { return gori.Capabilities{Images: true} }

func (c *captureProvider) Complete(_ context.Context, req gori.Request) (gori.Response, error) {
	if len(req.Messages) > 0 {
		c.got = req.Messages[len(req.Messages)-1]
	}
	return gori.Response{Message: gori.AssistantText("ok"), StopReason: gori.StopEndTurn}, nil
}

func (c *captureProvider) Stream(ctx context.Context, req gori.Request, _ func(gori.StreamEvent) error) (gori.Response, error) {
	return c.Complete(ctx, req)
}

func TestHandlerMapsFilePartToImage(t *testing.T) {
	cp := &captureProvider{}
	ag := &gori.Agent{Provider: cp, Model: "x", Session: gori.NewSession()}
	h := AgentHandler(ag)
	img := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	msg := Message{Role: "user", Parts: []Part{
		TextPart("describe"),
		{File: &FilePart{MimeType: "image/png", Bytes: img}},
	}}
	if _, err := h.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	hasImage := false
	for _, c := range cp.got.Content {
		if _, ok := c.(gori.Image); ok {
			hasImage = true
		}
	}
	if !hasImage {
		t.Errorf("expected an Image to reach the agent, got %#v", cp.got.Content)
	}
}
