// Package build constructs concrete providers and agents from a gori.Config.
// It lives outside the root package so the root can stay free of provider
// imports (avoiding an import cycle).
package build

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/a2a"
	"github.com/leelsey/gori/mcp"
	"github.com/leelsey/gori/provider/anthropic"
	"github.com/leelsey/gori/provider/clibackend"
	"github.com/leelsey/gori/provider/google"
	"github.com/leelsey/gori/provider/openai"
)

// MCPTools connects to every configured MCP server, returning their tools plus
// a closer that must be called when the tools are no longer needed.
func MCPTools(ctx context.Context, cfg *gori.Config) ([]gori.Tool, func(), error) {
	var tools []gori.Tool
	var clients []*mcp.Client
	closeAll := func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}
	for _, ms := range cfg.MCPServers {
		c, err := mcp.Dial(ctx, ms.Command, ms.Args...)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("build: dial mcp server %q: %w", ms.Name, err)
		}
		if err := c.Initialize(ctx, "gori"); err != nil {
			_ = c.Close()
			closeAll()
			return nil, nil, fmt.Errorf("build: init mcp server %q: %w", ms.Name, err)
		}
		ts, err := c.Tools(ctx)
		if err != nil {
			_ = c.Close()
			closeAll()
			return nil, nil, fmt.Errorf("build: list tools from mcp server %q: %w", ms.Name, err)
		}
		tools = append(tools, ts...)
		clients = append(clients, c)
	}
	return tools, closeAll, nil
}

// Provider builds a gori.Provider from a ProviderConfig, reading any API key
// from the configured environment variable. A non-nil hc replaces the HTTP
// client of the HTTP-backed providers (e.g. to inject httpdebug); the cli
// backend has no HTTP and ignores it.
func Provider(pc gori.ProviderConfig, hc *http.Client) (gori.Provider, error) {
	key := ""
	switch {
	case pc.APIKeyCmd != "":
		k, err := keyFromCmd(pc.APIKeyCmd)
		if err != nil {
			return nil, fmt.Errorf("build: api_key_cmd for provider %q: %w", pc.Name, err)
		}
		key = k
	case pc.APIKeyEnv != "":
		key = os.Getenv(pc.APIKeyEnv)
	}
	switch pc.Type {
	case "anthropic":
		c := anthropic.New(key)
		if pc.BaseURL != "" {
			c.WithBaseURL(pc.BaseURL)
		}
		if hc != nil {
			c.WithHTTPClient(hc)
		}
		return c, nil
	case "openai":
		c := openai.New(key)
		if pc.BaseURL != "" {
			c.WithBaseURL(pc.BaseURL)
		}
		if hc != nil {
			c.WithHTTPClient(hc)
		}
		return c, nil
	case "google":
		c := google.New(key)
		if pc.BaseURL != "" {
			c.WithBaseURL(pc.BaseURL)
		}
		if hc != nil {
			c.WithHTTPClient(hc)
		}
		return c, nil
	case "cli":
		return clibackend.New(clibackend.Config{
			Name:    pc.Name,
			Command: pc.Command,
			Args:    pc.Args,
			Via:     clibackend.PromptVia(pc.PromptVia),
		}), nil
	default:
		return nil, fmt.Errorf("build: unknown provider type %q", pc.Type)
	}
}

// SplitArgs splits a command string into argv, honouring quotes (and backslash
// escapes inside double quotes); quote characters are syntax, not data.
func SplitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inSingle, inDouble, started := false, false, false
	flush := func() {
		if started {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
			started = true
		case inDouble:
			switch {
			case c == '"':
				inDouble = false
			case c == '\\' && i+1 < len(s):
				i++
				cur.WriteByte(s[i])
			default:
				cur.WriteByte(c)
			}
			started = true
		case c == '\'':
			inSingle, started = true, true
		case c == '"':
			inDouble, started = true, true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flush()
	return args
}

// keyFromCmd runs the operator-controlled cmd (split with SplitArgs, no shell)
// and returns its trimmed stdout as the API key.
func keyFromCmd(cmd string) (string, error) {
	fields := SplitArgs(cmd)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty command")
	}
	out, err := exec.Command(fields[0], fields[1:]...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Orchestrator builds a multi-agent Orchestrator from cfg, wiring sub-agents as
// delegation tools on the main agent. hc is passed through to every provider.
func Orchestrator(cfg *gori.Config, bus *gori.Bus, tools *gori.Registry, hc *http.Client) (*gori.Orchestrator, error) {
	o := gori.NewOrchestrator(bus)
	descriptions := map[string]string{}
	for _, p := range cfg.Agents {
		ag, err := Agent(cfg, p.Name, tools, hc)
		if err != nil {
			return nil, err
		}
		role := p.Role
		if role == "" {
			role = "sub"
		}
		o.Add(p.Name, role, ag)
		if p.Description != "" {
			descriptions[p.Name] = p.Description
		}
	}
	if err := o.WireDelegation(descriptions); err != nil {
		return nil, err
	}
	return o, nil
}

// A2ATools wraps each remote agent in cfg.A2AAgents as a delegation gori.Tool.
func A2ATools(cfg *gori.Config) []gori.Tool {
	tools := make([]gori.Tool, 0, len(cfg.A2AAgents))
	for _, a := range cfg.A2AAgents {
		desc := a.Description
		if desc == "" {
			desc = "Delegate a task to the remote A2A agent " + a.Name + "."
		}
		tools = append(tools, a2a.NewClient(a.URL).AsTool(a.Name, desc))
	}
	return tools
}

// Agent builds a gori.Agent for the named persona, wiring its provider and any
// tools resolved from the supplied registry. hc is passed to the provider.
func Agent(cfg *gori.Config, name string, tools *gori.Registry, hc *http.Client) (*gori.Agent, error) {
	persona, ok := cfg.Agent(name)
	if !ok {
		return nil, fmt.Errorf("build: agent %q not defined", name)
	}
	pc, ok := cfg.Provider(persona.Provider)
	if !ok {
		return nil, fmt.Errorf("build: provider %q not defined", persona.Provider)
	}
	prov, err := Provider(pc, hc)
	if err != nil {
		return nil, err
	}
	agentTools := gori.NewRegistry()
	if tools != nil && len(persona.Tools) > 0 {
		for _, tn := range persona.Tools {
			t, ok := tools.Get(tn)
			if !ok {
				return nil, fmt.Errorf("build: agent %q references unknown tool %q", name, tn)
			}
			agentTools.Register(t)
		}
	}
	return &gori.Agent{
		Provider:           prov,
		Model:              persona.Model,
		System:             persona.System,
		Tools:              agentTools,
		Session:            gori.NewSession(),
		MaxTokens:          persona.MaxTokens,
		Temperature:        tempPtr(persona.Temperature),
		Thinking:           persona.ThinkingConfig(),
		ResponseModalities: persona.ResponseModalities,
	}, nil
}

// tempPtr maps a config temperature to the neutral *float64: 0 (the JSON
// zero/omitted value) means "use the provider default".
func tempPtr(t float64) *float64 {
	if t == 0 {
		return nil
	}
	return &t
}
