package gori

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the JSON configuration for a Gori deployment: a set of provider
// backends and a set of agent personas. It is pure data; constructing concrete
// providers from it lives outside this package to avoid an import cycle.
type Config struct {
	DefaultAgent string            `json:"default_agent,omitempty"`
	Providers    []ProviderConfig  `json:"providers"`
	Agents       []PersonaConfig   `json:"agents"`
	MCPServers   []MCPServerConfig `json:"mcp_servers,omitempty"`
	A2AAgents    []A2AAgentConfig  `json:"a2a_agents,omitempty"`
	Bus          string            `json:"bus,omitempty"`           // network bus hub URL to bridge events to
	BusTokenEnv  string            `json:"bus_token_env,omitempty"` // env var holding the hub bearer token (value never stored)
}

// MCPServerConfig declares an external MCP server to connect to over stdio; its
// tools become available to agents.
type MCPServerConfig struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// A2AAgentConfig declares a remote A2A agent to call; it is exposed to local
// agents as a delegation tool.
type A2AAgentConfig struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// ProviderConfig declares one LLM backend.
type ProviderConfig struct {
	Name      string `json:"name"`                  // logical name referenced by personas
	Type      string `json:"type"`                  // anthropic|openai|google|cli
	APIKeyEnv string `json:"api_key_env,omitempty"` // env var holding the API key
	APIKeyCmd string `json:"api_key_cmd,omitempty"` // command printing the API key (alternative to api_key_env)
	BaseURL   string `json:"base_url,omitempty"`
	// CLI backend fields (type == "cli"):
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	PromptVia string   `json:"prompt_via,omitempty"` // stdin|arg
}

// PersonaConfig declares one agent persona.
type PersonaConfig struct {
	Name           string   `json:"name"`
	Provider       string   `json:"provider"` // references ProviderConfig.Name
	Model          string   `json:"model"`
	System         string   `json:"system,omitempty"`
	Role           string   `json:"role,omitempty"`        // main|sub
	Description    string   `json:"description,omitempty"` // shown to the main agent when wired as a delegation tool
	Tools          []string `json:"tools,omitempty"`
	MaxTokens      int      `json:"max_tokens,omitempty"`
	Temperature    float64  `json:"temperature,omitempty"`
	Thinking       string   `json:"thinking,omitempty"` // off|auto|budget
	ThinkingBudget int      `json:"thinking_budget,omitempty"`
	// ResponseModalities opts into non-text output (e.g. "audio", "image").
	ResponseModalities []string `json:"response_modalities,omitempty"`
}

// ThinkingConfig converts the persona's thinking settings to a ThinkingConfig.
func (p PersonaConfig) ThinkingConfig() ThinkingConfig {
	switch strings.ToLower(p.Thinking) {
	case "auto":
		return ThinkingConfig{Mode: ThinkingAuto}
	case "budget":
		return ThinkingConfig{Mode: ThinkingBudget, Budget: p.ThinkingBudget}
	default:
		return ThinkingConfig{Mode: ThinkingOff}
	}
}

// LoadConfig reads and validates a JSON config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("gori: parse config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks referential integrity of the config.
func (c *Config) Validate() error {
	provs := map[string]bool{}
	provType := map[string]string{}
	for _, p := range c.Providers {
		if p.Name == "" || p.Type == "" {
			return fmt.Errorf("gori: provider missing name or type")
		}
		if provs[p.Name] {
			return fmt.Errorf("gori: duplicate provider %q", p.Name)
		}
		switch p.Type {
		case "cli":
			if p.Command == "" {
				return fmt.Errorf("gori: cli provider %q missing command", p.Name)
			}
			if p.PromptVia != "" && p.PromptVia != "stdin" && p.PromptVia != "arg" {
				return fmt.Errorf("gori: cli provider %q has invalid prompt_via %q (want stdin or arg)", p.Name, p.PromptVia)
			}
		case "anthropic", "openai", "google":
			if p.APIKeyEnv == "" && p.APIKeyCmd == "" && p.BaseURL == "" {
				return fmt.Errorf("gori: provider %q needs api_key_env, api_key_cmd or base_url", p.Name)
			}
		default:
			return fmt.Errorf("gori: provider %q has unknown type %q", p.Name, p.Type)
		}
		provs[p.Name] = true
		provType[p.Name] = p.Type
	}
	names := map[string]bool{}
	for _, a := range c.Agents {
		if a.Name == "" {
			return fmt.Errorf("gori: agent missing name")
		}
		if names[a.Name] {
			return fmt.Errorf("gori: duplicate agent %q", a.Name)
		}
		names[a.Name] = true
		if !provs[a.Provider] {
			return fmt.Errorf("gori: agent %q references unknown provider %q", a.Name, a.Provider)
		}
		if provType[a.Provider] == "cli" && len(a.Tools) > 0 {
			return fmt.Errorf("gori: agent %q uses cli provider %q, which has no tool support; remove its tools", a.Name, a.Provider)
		}
	}
	if c.DefaultAgent != "" && !names[c.DefaultAgent] {
		return fmt.Errorf("gori: default_agent %q not defined", c.DefaultAgent)
	}
	for _, m := range c.MCPServers {
		if m.Name == "" || m.Command == "" {
			return fmt.Errorf("gori: mcp_server missing name or command")
		}
	}
	a2aNames := map[string]bool{}
	for _, a := range c.A2AAgents {
		if a.Name == "" || a.URL == "" {
			return fmt.Errorf("gori: a2a_agent missing name or url")
		}
		if a2aNames[a.Name] {
			return fmt.Errorf("gori: duplicate a2a_agent %q", a.Name)
		}
		if names[a.Name] {
			return fmt.Errorf("gori: a2a_agent %q collides with an agent name", a.Name)
		}
		a2aNames[a.Name] = true
	}
	return nil
}

// Provider returns the named provider config.
func (c *Config) Provider(name string) (ProviderConfig, bool) {
	for _, p := range c.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderConfig{}, false
}

// Agent returns the named persona config.
func (c *Config) Agent(name string) (PersonaConfig, bool) {
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return PersonaConfig{}, false
}

// Save validates the config and writes it as indented JSON to path, creating
// parent directories. The file is written 0600 since it may reference
// secret-fetching commands. It refuses to write an invalid config.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Providers == nil {
		c.Providers = []ProviderConfig{}
	}
	if c.Agents == nil {
		c.Agents = []PersonaConfig{}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	// Write to a randomly-named temp file in the same dir then rename, so a crash
	// can't truncate the config, and a pre-planted symlink/file at a predictable
	// path can't be followed or leak a looser mode. os.CreateTemp uses O_EXCL and
	// creates the file 0600.
	f, err := os.CreateTemp(dir, ".gori-config-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
