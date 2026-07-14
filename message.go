package gori

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message is a single turn in a conversation: an author role and ordered content.
type Message struct {
	Role    Role
	Content []Content
}

// UserText builds a user message from a plain string.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []Content{Text{Text: s}}}
}

// AssistantText builds an assistant message from a plain string.
func AssistantText(s string) Message {
	return Message{Role: RoleAssistant, Content: []Content{Text{Text: s}}}
}

// Text returns the concatenation of all Text blocks in the message.
func (m Message) Text() string {
	var b strings.Builder
	for _, c := range m.Content {
		if t, ok := c.(Text); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// SystemTextOnly returns an error naming the first non-Text content block found
// in a RoleSystem message. System prompts are text-only on every provider, so
// adapters reject such messages instead of silently dropping content.
func SystemTextOnly(msgs []Message) error {
	for _, m := range msgs {
		if m.Role != RoleSystem {
			continue
		}
		for _, c := range m.Content {
			if _, ok := c.(Text); !ok {
				return fmt.Errorf("system messages are text-only (got %T)", c)
			}
		}
	}
	return nil
}

// ToolUses returns every ToolUse block in the message.
func (m Message) ToolUses() []ToolUse {
	var out []ToolUse
	for _, c := range m.Content {
		if t, ok := c.(ToolUse); ok {
			out = append(out, t)
		}
	}
	return out
}

// contentEnvelope is the canonical, self-describing JSON form of a Content.
// It is used for session persistence and tests, and is independent of any
// provider's wire format.
type contentEnvelope struct {
	Kind      string          `json:"kind"`
	Text      string          `json:"text,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Steps     []string        `json:"steps,omitempty"`
	MediaType string          `json:"media_type,omitempty"`
	Data      []byte          `json:"data,omitempty"`
	URL       string          `json:"url,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// MarshalJSON encodes a Message in Gori's canonical format.
func (m Message) MarshalJSON() ([]byte, error) {
	envs := make([]contentEnvelope, 0, len(m.Content))
	for _, c := range m.Content {
		var e contentEnvelope
		e.Kind = c.Kind()
		switch v := c.(type) {
		case Text:
			e.Text = v.Text
		case Thinking:
			e.Text, e.Signature = v.Text, v.Signature
		case Plan:
			e.Steps = v.Steps
		case Image:
			e.MediaType, e.Data, e.URL = v.MediaType, v.Data, v.URL
		case Audio:
			e.MediaType, e.Data = v.MediaType, v.Data
		case ToolUse:
			input := v.Input
			if len(input) == 0 || !json.Valid(input) {
				input = json.RawMessage("{}") // never let a truncated/invalid tool call break Marshal/Session.Save
			}
			e.ID, e.Name, e.Input = v.ID, v.Name, input
		case ToolResult:
			e.ToolUseID, e.Name, e.Text, e.IsError = v.ToolUseID, v.Name, v.Content, v.IsError
		default:
			return nil, fmt.Errorf("gori: unknown content kind %q", c.Kind())
		}
		envs = append(envs, e)
	}
	return json.Marshal(struct {
		Role    Role              `json:"role"`
		Content []contentEnvelope `json:"content"`
	}{m.Role, envs})
}

// UnmarshalJSON decodes a Message from Gori's canonical format.
func (m *Message) UnmarshalJSON(b []byte) error {
	var raw struct {
		Role    Role              `json:"role"`
		Content []contentEnvelope `json:"content"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.Content = make([]Content, 0, len(raw.Content))
	for _, e := range raw.Content {
		switch e.Kind {
		case "text":
			m.Content = append(m.Content, Text{Text: e.Text})
		case "thinking":
			m.Content = append(m.Content, Thinking{Text: e.Text, Signature: e.Signature})
		case "plan":
			m.Content = append(m.Content, Plan{Steps: e.Steps})
		case "image":
			m.Content = append(m.Content, Image{MediaType: e.MediaType, Data: e.Data, URL: e.URL})
		case "audio":
			m.Content = append(m.Content, Audio{MediaType: e.MediaType, Data: e.Data})
		case "tool_use":
			m.Content = append(m.Content, ToolUse{ID: e.ID, Name: e.Name, Input: e.Input})
		case "tool_result":
			m.Content = append(m.Content, ToolResult{ToolUseID: e.ToolUseID, Name: e.Name, Content: e.Text, IsError: e.IsError})
		default:
			return fmt.Errorf("gori: unknown content kind %q", e.Kind)
		}
	}
	return nil
}
