package gori

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

// Tool is an action the model can invoke. Schema returns a JSON Schema object
// describing the tool's input; Execute runs it and returns a string result.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// ToolFunc adapts a plain function into a Tool. NewTool is the idiomatic
// constructor; the exported fields remain for struct-literal construction.
type ToolFunc struct {
	NameVal        string
	DescriptionVal string
	SchemaVal      json.RawMessage
	Fn             func(ctx context.Context, input json.RawMessage) (string, error)
}

// NewTool builds a Tool from a name, description, JSON schema (nil for a tool
// without parameters) and the function that executes it.
func NewTool(name, description string, schema json.RawMessage, fn func(ctx context.Context, input json.RawMessage) (string, error)) ToolFunc {
	return ToolFunc{NameVal: name, DescriptionVal: description, SchemaVal: schema, Fn: fn}
}

// TextTool builds a Tool taking a single required string argument named argName
// (documented by argDescription when non-empty) and passing its value to fn —
// the shape shared by every delegation wrapper (orchestrator sub-agents, remote
// A2A agents, MCP-exposed agents).
func TextTool(name, description, argName, argDescription string, fn func(ctx context.Context, text string) (string, error)) ToolFunc {
	arg := map[string]any{"type": "string"}
	if argDescription != "" {
		arg["description"] = argDescription
	}
	schema, _ := json.Marshal(map[string]any{
		"type":       "object",
		"properties": map[string]any{argName: arg},
		"required":   []string{argName},
	})
	return ToolFunc{
		NameVal:        name,
		DescriptionVal: description,
		SchemaVal:      schema,
		Fn: func(ctx context.Context, input json.RawMessage) (string, error) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(input, &fields); err != nil {
				return "", err
			}
			var text string
			if raw, ok := fields[argName]; ok {
				if err := json.Unmarshal(raw, &text); err != nil {
					return "", err
				}
			}
			return fn(ctx, text)
		},
	}
}

func (t ToolFunc) Name() string            { return t.NameVal }
func (t ToolFunc) Description() string     { return t.DescriptionVal }
func (t ToolFunc) Schema() json.RawMessage { return t.SchemaVal }
func (t ToolFunc) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return t.Fn(ctx, input)
}

// Registry is a concurrency-safe set of tools keyed by name.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds or replaces a tool. It works on a zero-value Registry, so
// &Registry{} is as usable as NewRegistry().
func (r *Registry) Register(tools ...Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]Tool)
	}
	for _, t := range tools {
		r.tools[t.Name()] = t
	}
}

// Get returns the tool registered under name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all tools sorted by name.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}

// Defs returns provider-facing definitions for all registered tools.
func (r *Registry) Defs() []ToolDef {
	tools := r.List()
	defs := make([]ToolDef, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return defs
}
