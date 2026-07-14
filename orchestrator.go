package gori

import (
	"context"
	"fmt"
	"sync"
)

// Orchestrator coordinates a main agent and a set of named sub-agents. A host
// program (or the main agent itself, via delegation tools) drives sub-agents,
// which may run concurrently. Lifecycle events flow through the shared Bus.
type Orchestrator struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	roles  map[string]string
	main   string
	bus    *Bus
	usage  Usage
}

// NewOrchestrator returns an Orchestrator publishing events to bus (may be nil).
func NewOrchestrator(bus *Bus) *Orchestrator {
	return &Orchestrator{agents: map[string]*Agent{}, roles: map[string]string{}, bus: bus}
}

// addUsage accumulates a delegated sub-agent's token usage.
func (o *Orchestrator) addUsage(u Usage) {
	o.mu.Lock()
	o.usage.InputTokens += u.InputTokens
	o.usage.OutputTokens += u.OutputTokens
	o.usage.ThinkingTokens += u.ThinkingTokens
	o.mu.Unlock()
}

// Usage returns the cumulative token usage of all sub-agent delegations run via
// the delegation tools. A single Agent's own usage is on Agent.TotalUsage.
func (o *Orchestrator) Usage() Usage {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.usage
}

// Add registers an agent under name with role "main" or "sub"; the first, or
// any "main", becomes the entry point. Add takes ownership of a (it sets
// a.Name/a.Bus): never Add a running agent, and use Clone() to register the
// same agent twice.
func (o *Orchestrator) Add(name, role string, a *Agent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	a.Name = name
	if a.Bus == nil {
		a.Bus = o.bus
	}
	o.agents[name] = a
	o.roles[name] = role
	if role == "main" || o.main == "" {
		o.main = name
	}
}

// Get returns the named agent.
func (o *Orchestrator) Get(name string) (*Agent, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	a, ok := o.agents[name]
	return a, ok
}

// Main returns the entry-point agent, or nil if none is registered.
func (o *Orchestrator) Main() *Agent {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.agents[o.main]
}

// AsTool wraps a named sub-agent as a delegation Tool. Each invocation runs a
// fresh clone of the sub-agent, so concurrent delegations are safe.
func (o *Orchestrator) AsTool(name, description string) Tool {
	return TextTool(name, description, "task", "the task to delegate to this agent",
		func(ctx context.Context, task string) (string, error) {
			o.mu.RLock()
			sub := o.agents[name]
			o.mu.RUnlock()
			if sub == nil {
				return "", fmt.Errorf("orchestrator: unknown agent %q", name)
			}
			cl := sub.Clone()
			out, err := cl.Run(ctx, task)
			// Account usage even when the run failed: the provider still billed
			// every step that completed before the error.
			o.addUsage(cl.TotalUsage)
			if err != nil {
				return "", err
			}
			return out.Text(), nil
		})
}

// WireDelegation registers every sub-agent as a delegation tool on the main
// agent and enables concurrent tool execution, so the main model can hand tasks
// to sub-agents. descriptions optionally overrides the per-agent tool blurb.
func (o *Orchestrator) WireDelegation(descriptions map[string]string) error {
	o.mu.RLock()
	main := o.agents[o.main]
	subs := make([]string, 0, len(o.roles))
	for name, role := range o.roles {
		// never wire main onto itself (unbounded recursive self-delegation)
		if role == "sub" && name != o.main {
			subs = append(subs, name)
		}
	}
	o.mu.RUnlock()

	if main == nil {
		return fmt.Errorf("orchestrator: no main agent")
	}
	if main.Tools == nil {
		main.Tools = NewRegistry()
	}
	for _, name := range subs {
		desc := descriptions[name]
		if desc == "" {
			desc = "Delegate a task to the " + name + " agent."
		}
		main.Tools.Register(o.AsTool(name, desc))
	}
	main.ParallelTools = true
	return nil
}

// Run executes the main agent with input.
func (o *Orchestrator) Run(ctx context.Context, input string) (Message, error) {
	main := o.Main()
	if main == nil {
		return Message{}, fmt.Errorf("orchestrator: no main agent")
	}
	return main.Run(ctx, input)
}
