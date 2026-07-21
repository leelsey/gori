package gori

import (
	"context"
	"encoding/json"
	"fmt"
)

// Provider is an LLM backend. Implementations live under provider/* and adapt a
// neutral Request into their own wire format, isolating per-vendor differences.
type Provider interface {
	// Name identifies the provider, e.g. "anthropic".
	Name() string
	// Capabilities reports which optional features the provider supports.
	Capabilities() Capabilities
	// Complete runs a single non-streaming completion.
	Complete(ctx context.Context, req Request) (Response, error)
	// Stream runs a streaming completion, invoking fn for each event. The final
	// assembled Response is also returned. If fn returns an error, streaming
	// stops and that error is returned.
	Stream(ctx context.Context, req Request, fn func(StreamEvent) error) (Response, error)
}

// Capabilities describes a provider's optional feature support.
type Capabilities struct {
	Streaming bool
	Tools     bool
	Thinking  bool
	Images    bool
	Audio     bool
}

// ThinkingMode selects how a provider should allocate reasoning effort.
type ThinkingMode int

const (
	ThinkingOff    ThinkingMode = iota // no extended thinking
	ThinkingAuto                       // provider decides
	ThinkingBudget                     // bounded by Budget tokens
)

// ThinkingConfig configures extended thinking for a Request.
type ThinkingConfig struct {
	Mode   ThinkingMode
	Budget int // token budget when Mode == ThinkingBudget
}

// ToolDef is the provider-facing definition of a tool.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Request is a neutral completion request. Adapters translate it per provider.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []ToolDef
	// ToolChoice, when non-empty, forces the model to call the named tool
	// instead of choosing freely — the structured-output pattern (define one
	// tool whose schema is the desired shape, force it, read the tool input).
	// The named tool must appear in Tools. Empty leaves the choice to the model.
	ToolChoice string
	MaxTokens  int
	// Temperature is the sampling temperature. nil means "use the provider
	// default"; a non-nil pointer (including a pointer to 0) is forwarded so
	// deterministic decoding can be requested explicitly.
	Temperature *float64
	Thinking    ThinkingConfig
	// ResponseModalities opts into non-text output (e.g. "audio", "image") where
	// the provider supports it. Adapters that can request media output map this
	// to the provider's modality setting; others ignore it.
	ResponseModalities []string
}

// StopReason explains why a completion ended.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopOther     StopReason = "other"
)

// Usage reports token accounting for a completion. InputTokens is the full
// prompt size; CacheReadTokens/CacheWriteTokens are the cached portion of it
// where the provider reports one, not additional tokens.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	ThinkingTokens   int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Add accumulates o into u.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.ThinkingTokens += o.ThinkingTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
}

// Total returns input+output+thinking tokens. Cache tokens are excluded: they
// are a breakdown of InputTokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens + u.ThinkingTokens }

// String renders the usage on one line, omitting thinking and cache counts
// when zero.
func (u Usage) String() string {
	s := fmt.Sprintf("input %d, output %d", u.InputTokens, u.OutputTokens)
	if u.ThinkingTokens > 0 {
		s += fmt.Sprintf(", thinking %d", u.ThinkingTokens)
	}
	if u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
		s += fmt.Sprintf(", cache read %d, cache write %d", u.CacheReadTokens, u.CacheWriteTokens)
	}
	return s + fmt.Sprintf(", total %d", u.Total())
}

// Response is the result of a completion.
type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
}

// StreamEventType classifies a normalised streaming event.
type StreamEventType string

const (
	EventTextDelta     StreamEventType = "text_delta"
	EventThinkingDelta StreamEventType = "thinking_delta"
	EventToolStart     StreamEventType = "tool_start"
	EventToolDelta     StreamEventType = "tool_delta"
	EventToolStop      StreamEventType = "tool_stop"
	EventDone          StreamEventType = "done"
)

// StreamEvent is a single normalised incremental event from Stream.
type StreamEvent struct {
	Type     StreamEventType
	Text     string // for text_delta / thinking_delta
	ToolID   string // for tool_start
	ToolName string // for tool_start
	ToolArgs string // partial JSON for tool_delta
	Usage    *Usage // populated on done when available
}
