package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/internal/jsonrpc"
	"github.com/leelsey/gori/internal/rpc"
)

// Server exposes gori tools and agents over MCP. Construct it, add tools/agents,
// then call Serve with a transport (typically rpc.Stdio()).
type Server struct {
	info     Implementation
	registry *gori.Registry
	rpc      *rpc.Server
}

// NewServer returns a Server identifying itself as name/version.
func NewServer(name, version string) *Server {
	s := &Server{
		info:     Implementation{Name: name, Version: version},
		registry: gori.NewRegistry(),
		rpc:      rpc.NewServer(),
	}
	s.rpc.Handle("initialize", s.handleInitialize)
	s.rpc.Handle("notifications/initialized", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	s.rpc.Handle("ping", func(context.Context, json.RawMessage) (any, error) { return struct{}{}, nil })
	s.rpc.Handle("tools/list", s.handleListTools)
	s.rpc.Handle("tools/call", s.handleCallTool)
	return s
}

// AddTools exposes the given tools.
func (s *Server) AddTools(tools ...gori.Tool) { s.registry.Register(tools...) }

// AddRegistry exposes every tool in reg.
func (s *Server) AddRegistry(reg *gori.Registry) { s.registry.Register(reg.List()...) }

// AddAgent exposes an agent as a single tool that runs the agent on an "input"
// string and returns its final text. Each call uses a fresh session clone.
func (s *Server) AddAgent(name, description string, a *gori.Agent) {
	s.registry.Register(gori.TextTool(name, description, "input", "",
		func(ctx context.Context, input string) (string, error) {
			msg, err := a.Clone().Run(ctx, input)
			if err != nil {
				return "", err
			}
			return msg.Text(), nil
		}))
}

// Serve runs the MCP server over t until the transport closes.
func (s *Server) Serve(ctx context.Context, t rpc.Transport) error { return s.rpc.Serve(ctx, t) }

func (s *Server) handleInitialize(_ context.Context, params json.RawMessage) (any, error) {
	version := ProtocolVersion
	var p InitializeParams
	if json.Unmarshal(params, &p) == nil && versionSupported(p.ProtocolVersion) {
		version = p.ProtocolVersion // agree on the client's version when we support it
	}
	return InitializeResult{
		ProtocolVersion: version,
		Capabilities:    ServerCapabilities{Tools: &ToolsCapability{}},
		ServerInfo:      s.info,
	}, nil
}

func (s *Server) handleListTools(_ context.Context, _ json.RawMessage) (any, error) {
	tools := s.registry.List()
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		schema := t.Schema()
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, Tool{Name: t.Name(), Description: t.Description(), InputSchema: schema})
	}
	return ListToolsResult{Tools: out}, nil
}

func (s *Server) handleCallTool(ctx context.Context, params json.RawMessage) (any, error) {
	var p CallToolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid tools/call params")
	}
	tool, ok := s.registry.Get(p.Name)
	if !ok {
		return textResult(fmt.Sprintf("unknown tool %q", p.Name), true), nil
	}
	args := p.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	out, err := safeExecute(ctx, tool, args)
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	return textResult(out, false), nil
}

// safeExecute runs a tool, converting a panic into an error so one misbehaving
// tool cannot crash the whole server.
func safeExecute(ctx context.Context, tool gori.Tool, args json.RawMessage) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool panicked: %v", r)
		}
	}()
	return tool.Execute(ctx, args)
}
