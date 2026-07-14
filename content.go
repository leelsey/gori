package gori

import "encoding/json"

// Role identifies the author of a message.
type Role string

const (
	// RoleSystem messages are text-only: every vendor's system prompt accepts
	// text and nothing else, so provider adapters return an error (via
	// SystemTextOnly) rather than silently drop other content kinds.
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Content is a single part of a message. Each provider adapter is responsible
// for marshalling concrete Content types into its own wire format; the canonical
// JSON form used for sessions and tests lives in message.go. Adapters must
// return an error for content kinds their API cannot carry — never drop them
// silently (see e.g. the Anthropic adapter's audio rejection).
type Content interface {
	Kind() string
}

// Text is plain text content.
type Text struct{ Text string }

func (Text) Kind() string { return "text" }

// Thinking is model reasoning. Signature carries a provider-specific opaque
// token (e.g. Anthropic's thinking signature) that must be echoed back verbatim.
type Thinking struct {
	Text      string
	Signature string
}

func (Thinking) Kind() string { return "thinking" }

// Plan is a structured list of steps a model intends to take.
type Plan struct{ Steps []string }

func (Plan) Kind() string { return "plan" }

// Image is image content, supplied either inline (Data) or by URL.
type Image struct {
	MediaType string
	Data      []byte
	URL       string
}

func (Image) Kind() string { return "image" }

// Audio is audio content supplied inline.
type Audio struct {
	MediaType string
	Data      []byte
}

func (Audio) Kind() string { return "audio" }

// ToolUse is a model request to invoke a tool. Input is the raw JSON arguments.
type ToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
}

func (ToolUse) Kind() string { return "tool_use" }

// ToolResult is the outcome of executing a ToolUse, referenced by ToolUseID.
type ToolResult struct {
	ToolUseID string
	Name      string // tool name; lets providers (e.g. Google) match a result without the prior turn
	Content   string
	IsError   bool
}

func (ToolResult) Kind() string { return "tool_result" }
