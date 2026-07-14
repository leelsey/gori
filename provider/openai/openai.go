// Package openai implements gori.Provider against the OpenAI Chat Completions
// API using only the standard library.
//
// Chat Completions is used (rather than the newer Responses API) for its stable,
// well-understood request/response shape. Reasoning effort is supported via the
// reasoning_effort parameter, but reasoning text is not surfaced by this endpoint.
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/httpretry"
	"github.com/leelsey/gori/internal/sse"
)

const defaultBaseURL = "https://api.openai.com/v1"

// Client is an OpenAI Chat Completions provider. It implements gori.Provider.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	retry   httpretry.Policy
}

var _ gori.Provider = (*Client)(nil)

// New returns a Client authenticating with apiKey.
func New(apiKey string) *Client {
	return &Client{apiKey: apiKey, baseURL: defaultBaseURL, http: &http.Client{}, retry: httpretry.Default()}
}

// WithBaseURL overrides the API base URL.
func (c *Client) WithBaseURL(u string) *Client { c.baseURL = strings.TrimRight(u, "/"); return c }

// WithHTTPClient overrides the underlying *http.Client.
func (c *Client) WithHTTPClient(h *http.Client) *Client { c.http = h; return c }

// WithRetry sets the retry policy (Attempts <= 1 disables retries).
func (c *Client) WithRetry(p httpretry.Policy) *Client { c.retry = p; return c }

// WithoutRetry disables retries.
func (c *Client) WithoutRetry() *Client { c.retry = httpretry.Policy{}; return c }

// Name reports the provider name.
func (c *Client) Name() string { return "openai" }

// Capabilities reports supported features.
func (c *Client) Capabilities() gori.Capabilities {
	return gori.Capabilities{Streaming: true, Tools: true, Images: true, Audio: true}
}

type apiFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type apiTool struct {
	Type     string  `json:"type"`
	Function apiFunc `json:"function"`
}

type apiToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type apiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"`
	ToolCalls  []apiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type audioOut struct {
	Voice  string `json:"voice"`
	Format string `json:"format"`
}

type apiRequest struct {
	Model               string         `json:"model"`
	Messages            []apiMessage   `json:"messages"`
	Tools               []apiTool      `json:"tools,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	ReasoningEffort     string         `json:"reasoning_effort,omitempty"`
	Modalities          []string       `json:"modalities,omitempty"`
	Audio               *audioOut      `json:"audio,omitempty"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *streamOptions `json:"stream_options,omitempty"`
}

func userContent(cs []gori.Content) any {
	hasNonText := false
	for _, c := range cs {
		if _, ok := c.(gori.Text); !ok {
			if _, ok := c.(gori.Plan); !ok {
				hasNonText = true
			}
		}
	}
	if !hasNonText {
		var b strings.Builder
		for _, c := range cs {
			switch v := c.(type) {
			case gori.Text:
				b.WriteString(v.Text)
			case gori.Plan:
				b.WriteString(strings.Join(v.Steps, "\n"))
			}
		}
		return b.String()
	}
	parts := make([]any, 0, len(cs))
	for _, c := range cs {
		switch v := c.(type) {
		case gori.Text:
			parts = append(parts, map[string]any{"type": "text", "text": v.Text})
		case gori.Plan:
			parts = append(parts, map[string]any{"type": "text", "text": strings.Join(v.Steps, "\n")})
		case gori.Image:
			url := v.URL
			if url == "" {
				url = fmt.Sprintf("data:%s;base64,%s", v.MediaType, base64.StdEncoding.EncodeToString(v.Data))
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		case gori.Audio:
			if len(v.Data) == 0 {
				continue
			}
			parts = append(parts, map[string]any{"type": "input_audio", "input_audio": map[string]any{
				"data": base64.StdEncoding.EncodeToString(v.Data), "format": audioFormat(v.MediaType),
			}})
		}
	}
	if len(parts) == 0 {
		return "" // avoid emitting "content": [], which OpenAI rejects
	}
	return parts
}

func audioFormat(mediaType string) string {
	switch mediaType {
	case "audio/wav", "audio/x-wav":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	default:
		if i := strings.LastIndex(mediaType, "/"); i >= 0 {
			return mediaType[i+1:]
		}
		return mediaType
	}
}

func buildMessages(req gori.Request) []apiMessage {
	msgs := make([]apiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, apiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case gori.RoleSystem:
			if s := m.Text(); s != "" { // skip empty: some servers reject "" content
				msgs = append(msgs, apiMessage{Role: "system", Content: s})
			}
		case gori.RoleTool:
			for _, c := range m.Content {
				if tr, ok := c.(gori.ToolResult); ok {
					content := tr.Content
					if tr.IsError {
						content = "Error: " + content // OpenAI tool messages have no native is_error flag
					}
					msgs = append(msgs, apiMessage{Role: "tool", ToolCallID: tr.ToolUseID, Content: content})
				}
			}
		case gori.RoleAssistant:
			am := apiMessage{Role: "assistant"}
			var text strings.Builder
			for _, c := range m.Content {
				switch v := c.(type) {
				case gori.Text:
					text.WriteString(v.Text)
				case gori.ToolUse:
					args := string(v.Input)
					if args == "" {
						args = "{}"
					}
					tc := apiToolCall{ID: v.ID, Type: "function"}
					tc.Function.Name = v.Name
					tc.Function.Arguments = args
					am.ToolCalls = append(am.ToolCalls, tc)
				}
			}
			if text.Len() > 0 {
				am.Content = text.String()
			} else if len(am.ToolCalls) == 0 {
				am.Content = "" // assistant needs content or tool_calls; force the field
			}
			msgs = append(msgs, am)
		default:
			msgs = append(msgs, apiMessage{Role: "user", Content: userContent(m.Content)})
		}
	}
	return msgs
}

func reasoningEffort(t gori.ThinkingConfig) string {
	switch t.Mode {
	case gori.ThinkingOff:
		return ""
	case gori.ThinkingBudget:
		switch {
		case t.Budget <= 0:
			return "medium"
		case t.Budget < 2048:
			return "low"
		case t.Budget < 8192:
			return "medium"
		default:
			return "high"
		}
	default:
		return "medium"
	}
}

// isReasoningModel reports whether model is an OpenAI reasoning model.
// Heuristic: the o-series prefixes (o1/o3/o4) — covers o1-mini/o3-mini/o4-mini
// and deliberately excludes gpt-4o, which begins with "gpt".
func isReasoningModel(model string) bool {
	return strings.HasPrefix(model, "o1") ||
		strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4")
}

func (c *Client) buildRequest(req gori.Request, stream bool) apiRequest {
	out := apiRequest{
		Model:               req.Model,
		Messages:            buildMessages(req),
		MaxCompletionTokens: req.MaxTokens,
		Stream:              stream,
	}
	// reasoning_effort is only valid for reasoning models; temperature != 1 is
	// rejected by them. Gate each by model family so non-reasoning models (gpt-4o)
	// don't 400.
	reasoning := isReasoningModel(req.Model)
	if reasoning && req.Thinking.Mode != gori.ThinkingOff {
		out.ReasoningEffort = reasoningEffort(req.Thinking)
	}
	if !reasoning && req.Temperature != nil {
		out.Temperature = req.Temperature
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, apiTool{Type: "function", Function: apiFunc{
			Name: t.Name, Description: t.Description, Parameters: t.Schema,
		}})
	}
	for _, mod := range req.ResponseModalities {
		if mod == "audio" {
			out.Modalities = []string{"text", "audio"}
			out.Audio = &audioOut{Voice: "alloy", Format: "wav"}
		}
	}
	if stream {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return out
}

func (c *Client) post(ctx context.Context, body apiRequest) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := httpretry.Do(ctx, c.http, c.retry, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("authorization", "Bearer "+c.apiKey)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) // bounded: a hostile server cannot balloon the error
		resp.Body.Close()
		return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return resp, nil
}

func stopReason(s string) gori.StopReason {
	switch s {
	case "stop":
		return gori.StopEndTurn
	case "tool_calls":
		return gori.StopToolUse
	case "length":
		return gori.StopMaxTokens
	default:
		return gori.StopOther
	}
}

type apiResponse struct {
	Choices []struct {
		Message struct {
			Content   string        `json:"content"`
			ToolCalls []apiToolCall `json:"tool_calls"`
			Audio     *struct {
				Data       string `json:"data"`
				Transcript string `json:"transcript"`
			} `json:"audio"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete runs a non-streaming completion.
func (c *Client) Complete(ctx context.Context, req gori.Request) (gori.Response, error) {
	if err := gori.SystemTextOnly(req.Messages); err != nil {
		return gori.Response{}, fmt.Errorf("openai: %w", err)
	}
	resp, err := c.post(ctx, c.buildRequest(req, false))
	if err != nil {
		return gori.Response{}, err
	}
	defer httpretry.DrainClose(resp.Body)
	var parsed apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return gori.Response{}, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return gori.Response{}, fmt.Errorf("openai: empty choices")
	}
	ch := parsed.Choices[0]
	var content []gori.Content
	if ch.Message.Content != "" {
		content = append(content, gori.Text{Text: ch.Message.Content})
	}
	if ch.Message.Audio != nil {
		if raw, err := base64.StdEncoding.DecodeString(ch.Message.Audio.Data); err == nil && len(raw) > 0 {
			content = append(content, gori.Audio{MediaType: "audio/wav", Data: raw})
		}
		if ch.Message.Audio.Transcript != "" {
			content = append(content, gori.Text{Text: ch.Message.Audio.Transcript})
		}
	}
	for _, tc := range ch.Message.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		content = append(content, gori.ToolUse{ID: tc.ID, Name: tc.Function.Name, Input: input})
	}
	return gori.Response{
		Message:    gori.Message{Role: gori.RoleAssistant, Content: content},
		StopReason: stopReason(ch.FinishReason),
		Usage:      gori.Usage{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens},
	}, nil
}

type toolAcc struct {
	id      string
	name    string
	args    strings.Builder
	started bool
}

// Stream runs a streaming completion, normalising OpenAI SSE chunks.
func (c *Client) Stream(ctx context.Context, req gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	if err := gori.SystemTextOnly(req.Messages); err != nil {
		return gori.Response{}, fmt.Errorf("openai: %w", err)
	}
	resp, err := c.post(ctx, c.buildRequest(req, true))
	if err != nil {
		return gori.Response{}, err
	}
	defer resp.Body.Close()

	scanner := sse.NewScanner(resp.Body)
	var text strings.Builder
	tools := map[int]*toolAcc{}
	order := []int{}
	out := gori.Response{Message: gori.Message{Role: gori.RoleAssistant}}

	for {
		ev, serr := scanner.Next()
		if serr == io.EOF {
			break
		}
		if serr != nil {
			return gori.Response{}, serr
		}
		if ev.Data == "" || ev.Data == "[DONE]" {
			if ev.Data == "[DONE]" {
				break
			}
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string        `json:"content"`
					ToolCalls []apiToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			return gori.Response{}, err
		}
		if chunk.Error != nil {
			return gori.Response{}, fmt.Errorf("openai: stream error: %s: %s", chunk.Error.Type, chunk.Error.Message)
		}
		if chunk.Usage != nil {
			out.Usage = gori.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			text.WriteString(ch.Delta.Content)
			if err := fn(gori.StreamEvent{Type: gori.EventTextDelta, Text: ch.Delta.Content}); err != nil {
				return gori.Response{}, err
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			acc := tools[idx]
			if acc == nil {
				acc = &toolAcc{}
				tools[idx] = acc
				order = append(order, idx)
			}
			// frozen after tool_start so start/stop/ToolUse ids never diverge
			if tc.ID != "" && !acc.started {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			// args may arrive before id/name; a delta must never precede the start
			if tc.Function.Arguments != "" {
				acc.args.WriteString(tc.Function.Arguments)
			}
			// start on name alone (some servers never send an id — synthesise one)
			if !acc.started && acc.name != "" {
				acc.started = true // emit tool_start once
				if acc.id == "" {
					acc.id = fmt.Sprintf("call_%d", idx)
				}
				if err := fn(gori.StreamEvent{Type: gori.EventToolStart, ToolID: acc.id, ToolName: acc.name}); err != nil {
					return gori.Response{}, err
				}
				if acc.args.Len() > 0 { // flush any args buffered before the start
					if err := fn(gori.StreamEvent{Type: gori.EventToolDelta, ToolArgs: acc.args.String()}); err != nil {
						return gori.Response{}, err
					}
				}
			} else if acc.started && tc.Function.Arguments != "" {
				if err := fn(gori.StreamEvent{Type: gori.EventToolDelta, ToolArgs: tc.Function.Arguments}); err != nil {
					return gori.Response{}, err
				}
			}
		}
		if ch.FinishReason != "" {
			out.StopReason = stopReason(ch.FinishReason)
		}
	}

	// no finish_reason ⇒ truncated, regardless of how the stream ended
	if out.StopReason == "" {
		return gori.Response{}, fmt.Errorf("openai: stream ended unexpectedly (no finish_reason)")
	}

	if text.Len() > 0 {
		out.Message.Content = append(out.Message.Content, gori.Text{Text: text.String()})
	}
	for _, idx := range order {
		acc := tools[idx]
		if acc.name == "" {
			continue // nameless fragments cannot form a call; nothing was emitted
		}
		if acc.id == "" {
			// Servers that omit ids still need the call answered; synthesise a
			// stable id so the tool_result can be paired on the next turn.
			acc.id = fmt.Sprintf("call_%d", idx)
		}
		input := json.RawMessage(acc.args.String())
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		out.Message.Content = append(out.Message.Content, gori.ToolUse{ID: acc.id, Name: acc.name, Input: input})
		if err := fn(gori.StreamEvent{Type: gori.EventToolStop, ToolID: acc.id}); err != nil {
			return gori.Response{}, err
		}
	}
	if err := fn(gori.StreamEvent{Type: gori.EventDone, Usage: &out.Usage}); err != nil {
		return gori.Response{}, err
	}
	return out, nil
}
