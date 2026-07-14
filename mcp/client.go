package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/internal/rpc"
)

// defaultCloseGrace bounds how long Close waits for a spawned subprocess to
// exit after the transport is closed, before killing it; CloseGrace on Client
// overrides it.
const defaultCloseGrace = 5 * time.Second

// Client speaks MCP to a server. Use NewClient with an existing transport, or
// Dial to spawn a server subprocess over stdio.
type Client struct {
	// CloseGrace overrides how long Close waits for a spawned subprocess to
	// exit before killing it; zero means defaultCloseGrace.
	CloseGrace time.Duration

	rpc *rpc.Client
	cmd *exec.Cmd
}

// NewClient wraps an existing transport (e.g. an in-process pipe).
func NewClient(t rpc.Transport) *Client { return &Client{rpc: rpc.NewClient(t)} }

// Dial launches command as a stdio MCP server subprocess. ctx guards only the
// launch; the subprocess's lifetime is owned by the Client and ends via Close.
func Dial(ctx context.Context, command string, args ...string) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := rpc.NewStreamTransport(stdout, stdin, stdin)
	return &Client{rpc: rpc.NewClient(t), cmd: cmd}, nil
}

// Initialize performs the MCP handshake.
func (c *Client) Initialize(ctx context.Context, clientName string) error {
	var res InitializeResult
	err := c.rpc.Call(ctx, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      Implementation{Name: clientName, Version: gori.Version},
	}, &res)
	if err != nil {
		return err
	}
	// any server revision is accepted: the subset spoken here is wire-identical
	// across published MCP revisions
	return c.rpc.Notify(ctx, "notifications/initialized", nil)
}

// ListTools returns the server's advertised tools, following cursor pagination
// until the server reports no more pages.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res ListToolsResult
		if err := c.rpc.Call(ctx, "tools/list", params, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" || res.NextCursor == cursor {
			return all, nil // done, or the server made no progress (guard against a stuck cursor)
		}
		cursor = res.NextCursor
	}
}

// CallTool invokes a tool, returning its concatenated text content and whether
// the server flagged the result as an error.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	var res CallToolResult
	if err := c.rpc.Call(ctx, "tools/call", CallToolParams{Name: name, Arguments: args}, &res); err != nil {
		return "", false, err
	}
	var sb strings.Builder
	for _, blk := range res.Content {
		switch blk.Type {
		case "text", "":
			sb.WriteString(blk.Text)
		default:
			// surface non-text blocks (image/audio/resource) rather than dropping them
			if blk.MimeType != "" {
				fmt.Fprintf(&sb, "[%s %s]", blk.Type, blk.MimeType)
			} else {
				fmt.Fprintf(&sb, "[%s]", blk.Type)
			}
		}
	}
	return sb.String(), res.IsError, nil
}

// Tools returns the server's tools wrapped as gori.Tool values, ready to add to
// a gori.Registry so a gori.Agent can call them.
func (c *Client) Tools(ctx context.Context) ([]gori.Tool, error) {
	list, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gori.Tool, 0, len(list))
	for _, t := range list {
		name := t.Name
		out = append(out, gori.ToolFunc{
			NameVal:        name,
			DescriptionVal: t.Description,
			SchemaVal:      t.InputSchema,
			Fn: func(ctx context.Context, input json.RawMessage) (string, error) {
				text, isErr, err := c.CallTool(ctx, name, input)
				if err != nil {
					return "", err
				}
				if isErr {
					return "", fmt.Errorf("%s", text)
				}
				return text, nil
			},
		})
	}
	return out, nil
}

// Close shuts down the client; a spawned subprocess that outlives CloseGrace
// after stdin closes is killed so Close cannot hang.
func (c *Client) Close() error {
	err := c.rpc.Close()
	if c.cmd != nil {
		done := make(chan struct{})
		go func() { _ = c.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(cmp.Or(c.CloseGrace, defaultCloseGrace)):
			_ = c.cmd.Process.Kill()
			<-done
		}
	}
	return err
}
