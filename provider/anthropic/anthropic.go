// Package anthropic implements gori.Provider against the Anthropic Messages API
// using only the standard library.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/httpretry"
	"github.com/leelsey/gori/internal/sse"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultVersion   = "2023-06-01"
	defaultMaxTokens = 4096
)

// Client is an Anthropic Messages API provider. It implements gori.Provider.
type Client struct {
	apiKey  string
	baseURL string
	version string
	http    *http.Client
	retry   httpretry.Policy
}

var _ gori.Provider = (*Client)(nil)

// New returns a Client authenticating with apiKey.
func New(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		version: defaultVersion,
		http:    &http.Client{},
		retry:   httpretry.Default(),
	}
}

// WithBaseURL overrides the API base URL (useful for tests and proxies).
func (c *Client) WithBaseURL(u string) *Client { c.baseURL = strings.TrimRight(u, "/"); return c }

// WithHTTPClient overrides the underlying *http.Client.
func (c *Client) WithHTTPClient(h *http.Client) *Client { c.http = h; return c }

// WithRetry sets the retry policy (Attempts <= 1 disables retries).
func (c *Client) WithRetry(p httpretry.Policy) *Client { c.retry = p; return c }

// WithoutRetry disables retries.
func (c *Client) WithoutRetry() *Client { c.retry = httpretry.Policy{}; return c }

// Name reports the provider name.
func (c *Client) Name() string { return "anthropic" }

// Capabilities reports supported features.
func (c *Client) Capabilities() gori.Capabilities {
	return gori.Capabilities{Streaming: true, Tools: true, Thinking: true, Images: true}
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type apiRequest struct {
	Model       string         `json:"model"`
	MaxTokens   int            `json:"max_tokens"`
	System      string         `json:"system,omitempty"`
	Messages    []apiMessage   `json:"messages"`
	Tools       []apiTool      `json:"tools,omitempty"`
	ToolChoice  any            `json:"tool_choice,omitempty"`
	Thinking    map[string]any `json:"thinking,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
}

func wireRole(r gori.Role) string {
	if r == gori.RoleAssistant {
		return "assistant"
	}
	return "user"
}

func contentBlocks(cs []gori.Content) []any {
	blocks := make([]any, 0, len(cs))
	for _, c := range cs {
		switch v := c.(type) {
		case gori.Text:
			if v.Text == "" {
				continue // Anthropic rejects empty text blocks
			}
			blocks = append(blocks, map[string]any{"type": "text", "text": v.Text})
		case gori.Plan:
			blocks = append(blocks, map[string]any{"type": "text", "text": strings.Join(v.Steps, "\n")})
		case gori.Thinking:
			b := map[string]any{"type": "thinking", "thinking": v.Text}
			if v.Signature != "" {
				b["signature"] = v.Signature
			}
			blocks = append(blocks, b)
		case gori.ToolUse:
			input := v.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": v.ID, "name": v.Name, "input": input})
		case gori.ToolResult:
			content := v.Content
			if content == "" {
				content = "(no output)" // Anthropic rejects empty tool_result content
			}
			b := map[string]any{"type": "tool_result", "tool_use_id": v.ToolUseID, "content": content}
			if v.IsError {
				b["is_error"] = true
			}
			blocks = append(blocks, b)
		case gori.Image:
			if v.URL != "" {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": v.URL}})
			} else {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": v.MediaType, "data": v.Data,
				}})
			}
		}
	}
	return blocks
}

func (c *Client) buildRequest(req gori.Request, stream bool) apiRequest {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	out := apiRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    req.System,
		Messages:  make([]apiMessage, 0, len(req.Messages)),
		Stream:    stream,
	}
	for _, m := range req.Messages {
		if m.Role == gori.RoleSystem {
			if s := m.Text(); s != "" {
				if out.System != "" {
					out.System += "\n\n"
				}
				out.System += s
			}
			continue
		}
		blocks := contentBlocks(m.Content)
		if len(blocks) == 0 {
			continue // Anthropic rejects messages with an empty content array
		}
		out.Messages = append(out.Messages, apiMessage{Role: wireRole(m.Role), Content: blocks})
	}
	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out.Tools = append(out.Tools, apiTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	if req.ToolChoice != "" {
		out.ToolChoice = map[string]any{"type": "tool", "name": req.ToolChoice}
	}
	thinking := req.Thinking.Mode != gori.ThinkingOff
	if thinking {
		budget := req.Thinking.Budget
		if budget <= 0 {
			budget = 2048
		} else if budget < 1024 {
			budget = 1024 // Anthropic's minimum thinking budget
		}
		out.Thinking = map[string]any{"type": "enabled", "budget_tokens": budget}
		// Anthropic requires max_tokens > thinking budget; ensure headroom.
		if out.MaxTokens <= budget {
			out.MaxTokens = budget + defaultMaxTokens
		}
	}
	// Temperature is incompatible with thinking; only send it otherwise.
	if !thinking && req.Temperature != nil {
		out.Temperature = req.Temperature
	}
	return out
}

func (c *Client) post(ctx context.Context, body apiRequest) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := httpretry.Do(ctx, c.http, c.retry, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", c.version)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) // bounded: a hostile server cannot balloon the error
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return resp, nil
}

type respBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type apiResponse struct {
	ID         string      `json:"id"`
	Content    []respBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func stopReason(s string) gori.StopReason {
	switch s {
	case "end_turn", "stop_sequence":
		return gori.StopEndTurn
	case "tool_use":
		return gori.StopToolUse
	case "max_tokens":
		return gori.StopMaxTokens
	default:
		return gori.StopOther
	}
}

func (r apiResponse) toResponse() gori.Response {
	var content []gori.Content
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			content = append(content, gori.Text{Text: b.Text})
		case "thinking":
			content = append(content, gori.Thinking{Text: b.Thinking, Signature: b.Signature})
		case "tool_use":
			content = append(content, gori.ToolUse{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	return gori.Response{
		Message:    gori.Message{Role: gori.RoleAssistant, Content: content},
		StopReason: stopReason(r.StopReason),
		// input_tokens excludes cached tokens; normalise to the full prompt size.
		Usage: gori.Usage{
			InputTokens:      r.Usage.InputTokens + r.Usage.CacheReadInputTokens + r.Usage.CacheCreationInputTokens,
			OutputTokens:     r.Usage.OutputTokens,
			CacheReadTokens:  r.Usage.CacheReadInputTokens,
			CacheWriteTokens: r.Usage.CacheCreationInputTokens,
		},
	}
}

// checkContent rejects content the Messages API cannot carry (audio, non-text
// system blocks) rather than silently dropping it.
func checkContent(req gori.Request) error {
	if err := gori.SystemTextOnly(req.Messages); err != nil {
		return fmt.Errorf("anthropic: %w", err)
	}
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if _, ok := c.(gori.Audio); ok {
				return fmt.Errorf("anthropic: audio content is not supported")
			}
		}
	}
	return nil
}

// Complete runs a non-streaming completion.
func (c *Client) Complete(ctx context.Context, req gori.Request) (gori.Response, error) {
	if err := checkContent(req); err != nil {
		return gori.Response{}, err
	}
	resp, err := c.post(ctx, c.buildRequest(req, false))
	if err != nil {
		return gori.Response{}, err
	}
	defer httpretry.DrainClose(resp.Body)
	var parsed apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return gori.Response{}, fmt.Errorf("anthropic: decode response: %w", err)
	}
	return parsed.toResponse(), nil
}

type blockAcc struct {
	typ       string
	text      strings.Builder
	signature string
	toolID    string
	toolName  string
	toolJSON  strings.Builder
}

func (a *blockAcc) content() gori.Content {
	switch a.typ {
	case "text":
		if a.text.Len() == 0 {
			return nil
		}
		return gori.Text{Text: a.text.String()}
	case "thinking":
		return gori.Thinking{Text: a.text.String(), Signature: a.signature}
	case "tool_use":
		input := json.RawMessage(a.toolJSON.String())
		if len(input) == 0 || !json.Valid(input) {
			input = json.RawMessage("{}")
		}
		return gori.ToolUse{ID: a.toolID, Name: a.toolName, Input: input}
	default:
		return nil // redacted_thinking / unknown blocks carry no re-sendable content
	}
}

// Stream runs a streaming completion, normalising Anthropic SSE events.
func (c *Client) Stream(ctx context.Context, req gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	if err := checkContent(req); err != nil {
		return gori.Response{}, err
	}
	resp, err := c.post(ctx, c.buildRequest(req, true))
	if err != nil {
		return gori.Response{}, err
	}
	defer resp.Body.Close()

	scanner := sse.NewScanner(resp.Body)
	blocks := map[int]*blockAcc{}
	maxIdx := -1
	out := gori.Response{Message: gori.Message{Role: gori.RoleAssistant}}

	sawStop := false
	for {
		ev, serr := scanner.Next()
		if serr == io.EOF {
			// no terminal signal (message_stop or stop_reason) ⇒ truncated
			if !sawStop && out.StopReason == "" {
				return gori.Response{}, fmt.Errorf("anthropic: stream ended unexpectedly (no stop_reason or message_stop)")
			}
			break
		}
		if serr != nil {
			return gori.Response{}, serr
		}
		if ev.Data == "" {
			continue
		}
		switch ev.Type {
		case "error":
			var e struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(ev.Data), &e)
			return gori.Response{}, fmt.Errorf("anthropic: stream error: %s: %s", e.Error.Type, e.Error.Message)
		case "content_block_start":
			var e struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
				return gori.Response{}, err
			}
			// merge with any delta-recovery accumulator; replacing would drop text
			acc := blocks[e.Index]
			if acc == nil {
				acc = &blockAcc{}
				blocks[e.Index] = acc
			}
			acc.typ = e.ContentBlock.Type
			acc.toolID = e.ContentBlock.ID
			acc.toolName = e.ContentBlock.Name
			if e.Index > maxIdx {
				maxIdx = e.Index
			}
			if acc.typ == "tool_use" {
				if err := fn(gori.StreamEvent{Type: gori.EventToolStart, ToolID: acc.toolID, ToolName: acc.toolName}); err != nil {
					return gori.Response{}, err
				}
			}
		case "content_block_delta":
			var e struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					Signature   string `json:"signature"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
				return gori.Response{}, err
			}
			acc := blocks[e.Index]
			if acc == nil {
				// start frame lost: recover text/thinking (tool_use needs id/name,
				// a bare signature would be invalid to resend)
				switch e.Delta.Type {
				case "text_delta":
					acc = &blockAcc{typ: "text"}
				case "thinking_delta":
					acc = &blockAcc{typ: "thinking"}
				default:
					continue
				}
				blocks[e.Index] = acc
				if e.Index > maxIdx {
					maxIdx = e.Index
				}
			}
			switch e.Delta.Type {
			case "text_delta":
				acc.text.WriteString(e.Delta.Text)
				if err := fn(gori.StreamEvent{Type: gori.EventTextDelta, Text: e.Delta.Text}); err != nil {
					return gori.Response{}, err
				}
			case "thinking_delta":
				acc.text.WriteString(e.Delta.Thinking)
				if err := fn(gori.StreamEvent{Type: gori.EventThinkingDelta, Text: e.Delta.Thinking}); err != nil {
					return gori.Response{}, err
				}
			case "signature_delta":
				acc.signature += e.Delta.Signature
			case "input_json_delta":
				acc.toolJSON.WriteString(e.Delta.PartialJSON)
				if err := fn(gori.StreamEvent{Type: gori.EventToolDelta, ToolArgs: e.Delta.PartialJSON}); err != nil {
					return gori.Response{}, err
				}
			}
		case "content_block_stop":
			var e struct {
				Index int `json:"index"`
			}
			_ = json.Unmarshal([]byte(ev.Data), &e)
			if acc := blocks[e.Index]; acc != nil && acc.typ == "tool_use" {
				if err := fn(gori.StreamEvent{Type: gori.EventToolStop, ToolID: acc.toolID}); err != nil {
					return gori.Response{}, err
				}
			}
		case "message_delta":
			var e struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
				return gori.Response{}, err
			}
			if e.Delta.StopReason != "" {
				out.StopReason = stopReason(e.Delta.StopReason)
			}
			if e.Usage.OutputTokens > 0 {
				out.Usage.OutputTokens = e.Usage.OutputTokens
			}
		case "message_start":
			var e struct {
				Message struct {
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &e); err == nil {
				u := e.Message.Usage
				out.Usage.InputTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
				out.Usage.CacheReadTokens = u.CacheReadInputTokens
				out.Usage.CacheWriteTokens = u.CacheCreationInputTokens
			}
		case "message_stop":
			sawStop = true // terminal frame; assembly happens after the loop
		}
	}

	for i := 0; i <= maxIdx; i++ {
		if acc := blocks[i]; acc != nil {
			if c := acc.content(); c != nil {
				out.Message.Content = append(out.Message.Content, c)
			}
		}
	}
	if err := fn(gori.StreamEvent{Type: gori.EventDone, Usage: &out.Usage}); err != nil {
		return gori.Response{}, err
	}
	return out, nil
}
