package gori

import (
	"context"
	"fmt"
	"sync"
)

// defaultMaxSteps bounds the ReAct loop so a misbehaving model cannot spin
// forever on tool calls.
const defaultMaxSteps = 8

// Agent runs a Provider against a Session, executing any tools the model calls
// in a reason-act-observe loop until the model produces a final answer.
// One Run/Stream at a time (Clone for parallel work); deadlines come from ctx;
// history grows unbounded unless KeepLast is set.
type Agent struct {
	Provider  Provider
	Model     string
	System    string
	Tools     *Registry
	Session   *Session
	MaxTokens int
	// Temperature is the sampling temperature; nil uses the provider default.
	Temperature *float64
	Thinking    ThinkingConfig
	// ResponseModalities opts into non-text output (e.g. "audio", "image").
	ResponseModalities []string
	MaxSteps           int
	// KeepLast > 0 trims the Session after every Run/Stream (see Session.Trim).
	KeepLast int

	// Multi-agent fields (optional):
	Name          string // identifies this agent on the Bus
	Bus           *Bus   // when set, lifecycle events are published here
	ParallelTools bool

	// TotalUsage is the cumulative provider usage (input+output+thinking tokens)
	// summed across the provider calls of the most recent Run/Stream.
	TotalUsage Usage
	// SessionUsage accumulates usage across every Run/Stream of this agent;
	// reset only by Clone.
	SessionUsage Usage
	// StepUsage records the usage of each provider call in the most recent
	// Run/Stream, in order.
	StepUsage []Usage
}

func (a *Agent) ensure() {
	if a.Session == nil {
		a.Session = NewSession()
	}
}

func (a *Agent) maxSteps() int {
	if a.MaxSteps > 0 {
		return a.MaxSteps
	}
	return defaultMaxSteps
}

func (a *Agent) emit(ctx context.Context, kind string, data any) {
	if a.Bus != nil {
		a.Bus.Publish(ctx, Event{Topic: a.Name, Agent: a.Name, Kind: kind, Data: data})
	}
}

// Clone returns a shallow copy of the agent with a fresh Session, safe to run
// concurrently with the original. Provider, Tools and Bus are reused (all are
// safe for concurrent use).
func (a *Agent) Clone() *Agent {
	c := *a
	c.Session = NewSession()
	c.TotalUsage = Usage{}
	c.SessionUsage = Usage{}
	c.StepUsage = nil
	if len(a.ResponseModalities) > 0 {
		c.ResponseModalities = append([]string(nil), a.ResponseModalities...)
	}
	return &c
}

func (a *Agent) request() Request {
	var defs []ToolDef
	if a.Tools != nil {
		defs = a.Tools.Defs()
	}
	return Request{
		Model:              a.Model,
		System:             a.System,
		Messages:           a.Session.History(),
		Tools:              defs,
		MaxTokens:          a.MaxTokens,
		Temperature:        a.Temperature,
		Thinking:           a.Thinking,
		ResponseModalities: a.ResponseModalities,
	}
}

func (a *Agent) execTool(ctx context.Context, u ToolUse) (res ToolResult) {
	defer func() { res.Name = u.Name }() // stamp the tool name on every return path
	if a.Tools == nil {
		return ToolResult{ToolUseID: u.ID, Content: fmt.Sprintf("no tools registered (requested %q)", u.Name), IsError: true}
	}
	t, ok := a.Tools.Get(u.Name)
	if !ok {
		return ToolResult{ToolUseID: u.ID, Content: fmt.Sprintf("unknown tool %q", u.Name), IsError: true}
	}
	// a tool panic must not crash the agent (notably under ParallelTools)
	defer func() {
		if r := recover(); r != nil {
			res = ToolResult{ToolUseID: u.ID, Content: fmt.Sprintf("tool panicked: %v", r), IsError: true}
		}
	}()
	out, err := t.Execute(ctx, u.Input)
	if err != nil {
		return ToolResult{ToolUseID: u.ID, Content: err.Error(), IsError: true}
	}
	return ToolResult{ToolUseID: u.ID, Content: out}
}

// observe runs the tool calls in resp and returns the tool-role message to
// append, plus whether the loop should continue. With ParallelTools the calls
// run concurrently.
func (a *Agent) observe(ctx context.Context, resp Response) (Message, bool) {
	uses := resp.Message.ToolUses()
	if len(uses) == 0 || !executableStop(resp.StopReason) {
		return Message{}, false
	}
	results := make([]Content, len(uses))
	if a.ParallelTools && len(uses) > 1 {
		var wg sync.WaitGroup
		for i, u := range uses {
			wg.Add(1)
			go func(i int, u ToolUse) {
				defer wg.Done()
				a.emit(ctx, "tool", u.Name)
				results[i] = a.execTool(ctx, u)
			}(i, u)
		}
		wg.Wait()
	} else {
		for i, u := range uses {
			a.emit(ctx, "tool", u.Name)
			results[i] = a.execTool(ctx, u)
		}
	}
	return Message{Role: RoleTool, Content: results}, true
}

// Run appends input as a user message and drives the ReAct loop to completion,
// returning the model's final message.
func (a *Agent) Run(ctx context.Context, input string) (Message, error) {
	return a.RunMessage(ctx, UserText(input))
}

// RunMessage is like Run but takes a full Message, allowing multimodal input
// (text plus Image/Audio content) to be supplied.
func (a *Agent) RunMessage(ctx context.Context, msg Message) (Message, error) {
	a.ensure()
	if err := ctx.Err(); err != nil {
		return Message{}, err // don't poison the session with an un-answered turn
	}
	a.TotalUsage = Usage{}
	a.StepUsage = nil
	a.emit(ctx, "start", msg.Text())
	a.Session.Append(msg)
	return a.loop(ctx)
}

// executableStop reports whether tool calls are safe to execute: tool_use and
// end_turn only (OpenAI-compatible servers emit "stop" with tool calls); any
// other stop may carry incomplete input. observe and answerTruncatedTools are
// exact complements through this predicate.
func executableStop(r StopReason) bool { return r == StopToolUse || r == StopEndTurn }

// answerTruncatedTools answers tool calls observe refused to execute with
// synthetic error results, keeping the session valid so the model can retry.
func (a *Agent) answerTruncatedTools(resp Response) (Message, bool) {
	if executableStop(resp.StopReason) {
		return Message{}, false
	}
	uses := resp.Message.ToolUses()
	if len(uses) == 0 {
		return Message{}, false
	}
	results := make([]Content, len(uses))
	for i, u := range uses {
		results[i] = ToolResult{ToolUseID: u.ID, Name: u.Name, Content: fmt.Sprintf("tool call not executed: response stopped with reason %q, input may be incomplete", resp.StopReason), IsError: true}
	}
	return Message{Role: RoleTool, Content: results}, true
}

func (a *Agent) loop(ctx context.Context) (Message, error) {
	return a.drive(ctx, func(ctx context.Context) (Response, error) {
		return a.Provider.Complete(ctx, a.request())
	})
}

// drive runs the reason-act-observe loop, fetching each model turn via step —
// the single implementation behind both Run (Complete) and Stream.
func (a *Agent) drive(ctx context.Context, step func(context.Context) (Response, error)) (Message, error) {
	// trim on every exit so repeatedly failing runs stay bounded too
	if a.KeepLast > 0 {
		defer a.Session.Trim(a.KeepLast)
	}
	var last Message
	for i := 0; i < a.maxSteps(); i++ {
		if err := ctx.Err(); err != nil {
			a.emit(ctx, "error", err.Error())
			return last, err
		}
		resp, err := step(ctx)
		if err != nil {
			a.emit(ctx, "error", err.Error())
			return last, err
		}
		a.TotalUsage.Add(resp.Usage)
		a.SessionUsage.Add(resp.Usage)
		a.StepUsage = append(a.StepUsage, resp.Usage)
		a.Session.Append(resp.Message)
		last = resp.Message
		a.emit(ctx, "message", resp.Message.Text())
		toolMsg, cont := a.observe(ctx, resp)
		if !cont {
			if recovered, ok := a.answerTruncatedTools(resp); ok {
				a.Session.Append(recovered)
				continue // truncated tool call: let the model retry with a fresh budget
			}
			a.emit(ctx, "done", last.Text())
			return last, nil
		}
		a.Session.Append(toolMsg)
	}
	a.emit(ctx, "error", "max steps reached")
	return last, fmt.Errorf("gori: max steps (%d) reached", a.maxSteps())
}

// Stream is like Run but streams incremental events through fn as they arrive.
// Tool execution happens between streamed steps.
func (a *Agent) Stream(ctx context.Context, input string, fn func(StreamEvent) error) (Message, error) {
	return a.StreamMessage(ctx, UserText(input), fn)
}

// StreamMessage is like Stream but takes a full (possibly multimodal) Message.
func (a *Agent) StreamMessage(ctx context.Context, msg Message, fn func(StreamEvent) error) (Message, error) {
	a.ensure()
	if err := ctx.Err(); err != nil {
		return Message{}, err // don't poison the session with an un-answered turn
	}
	a.TotalUsage = Usage{}
	a.StepUsage = nil
	a.emit(ctx, "start", msg.Text())
	a.Session.Append(msg)
	return a.streamLoop(ctx, fn)
}

func (a *Agent) streamLoop(ctx context.Context, fn func(StreamEvent) error) (Message, error) {
	return a.drive(ctx, func(ctx context.Context) (Response, error) {
		return a.Provider.Stream(ctx, a.request(), fn)
	})
}
