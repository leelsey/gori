// Package google implements gori.Provider against the Google Gemini
// generateContent API using only the standard library.
package google

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

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// Client is a Google Gemini provider. It implements gori.Provider.
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
func (c *Client) Name() string { return "google" }

// Capabilities reports supported features.
func (c *Client) Capabilities() gori.Capabilities {
	return gori.Capabilities{Streaming: true, Tools: true, Thinking: true, Images: true, Audio: true}
}

type apiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type apiFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type apiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type apiPart struct {
	Text             string               `json:"text,omitempty"`
	Thought          bool                 `json:"thought,omitempty"`
	FunctionCall     *apiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *apiFunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *apiInlineData       `json:"inlineData,omitempty"`
}

type apiContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []apiPart `json:"parts"`
}

type apiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type apiToolDecl struct {
	FunctionDeclarations []apiFunctionDecl `json:"functionDeclarations"`
}

type thinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
}

type genConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	ThinkingConfig     *thinkingConfig `json:"thinkingConfig,omitempty"`
	ResponseModalities []string        `json:"responseModalities,omitempty"`
}

type apiRequest struct {
	SystemInstruction *apiContent    `json:"systemInstruction,omitempty"`
	Contents          []apiContent   `json:"contents"`
	Tools             []apiToolDecl  `json:"tools,omitempty"`
	ToolConfig        *apiToolConfig `json:"toolConfig,omitempty"`
	GenerationConfig  *genConfig     `json:"generationConfig,omitempty"`
}

// apiToolConfig forces function calling when a specific tool is required
// (structured-output pattern). Mode "ANY" restricted to AllowedFunctionNames.
type apiToolConfig struct {
	FunctionCallingConfig apiFunctionCallingConfig `json:"functionCallingConfig"`
}

type apiFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

func buildContents(req gori.Request) []apiContent {
	idToName := map[string]string{}
	contents := make([]apiContent, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case gori.RoleSystem:
			continue // merged into systemInstruction by buildRequest, not a user turn
		case gori.RoleTool:
			parts := make([]apiPart, 0, len(m.Content))
			for _, c := range m.Content {
				tr, ok := c.(gori.ToolResult)
				if !ok {
					continue
				}
				resp := map[string]any{"result": tr.Content}
				if tr.IsError {
					resp = map[string]any{"error": tr.Content}
				}
				name := tr.Name
				if name == "" {
					name = idToName[tr.ToolUseID]
				}
				parts = append(parts, apiPart{FunctionResponse: &apiFunctionResponse{
					ID: tr.ToolUseID, Name: name, Response: resp,
				}})
			}
			if len(parts) == 0 {
				continue // Gemini rejects a Content with an empty parts array
			}
			contents = append(contents, apiContent{Role: "user", Parts: parts})
		case gori.RoleAssistant:
			parts := make([]apiPart, 0, len(m.Content))
			for _, c := range m.Content {
				switch v := c.(type) {
				case gori.Text:
					parts = append(parts, apiPart{Text: v.Text})
				case gori.Thinking:
					parts = append(parts, apiPart{Text: v.Text, Thought: true})
				case gori.Plan:
					parts = append(parts, apiPart{Text: strings.Join(v.Steps, "\n")})
				case gori.ToolUse:
					idToName[v.ID] = v.Name
					args := v.Input
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					parts = append(parts, apiPart{FunctionCall: &apiFunctionCall{ID: v.ID, Name: v.Name, Args: args}})
				case gori.Image:
					if v.URL == "" && len(v.Data) > 0 {
						parts = append(parts, apiPart{InlineData: &apiInlineData{
							MimeType: v.MediaType, Data: base64.StdEncoding.EncodeToString(v.Data),
						}})
					}
				case gori.Audio:
					if len(v.Data) > 0 {
						parts = append(parts, apiPart{InlineData: &apiInlineData{
							MimeType: v.MediaType, Data: base64.StdEncoding.EncodeToString(v.Data),
						}})
					}
				}
			}
			if len(parts) == 0 {
				continue // Gemini rejects a Content with an empty parts array
			}
			contents = append(contents, apiContent{Role: "model", Parts: parts})
		default:
			parts := make([]apiPart, 0, len(m.Content))
			for _, c := range m.Content {
				switch v := c.(type) {
				case gori.Text:
					parts = append(parts, apiPart{Text: v.Text})
				case gori.Plan:
					parts = append(parts, apiPart{Text: strings.Join(v.Steps, "\n")})
				case gori.Image:
					if v.URL == "" && len(v.Data) > 0 {
						parts = append(parts, apiPart{InlineData: &apiInlineData{
							MimeType: v.MediaType, Data: base64.StdEncoding.EncodeToString(v.Data),
						}})
					}
				case gori.Audio:
					if len(v.Data) > 0 {
						parts = append(parts, apiPart{InlineData: &apiInlineData{
							MimeType: v.MediaType, Data: base64.StdEncoding.EncodeToString(v.Data),
						}})
					}
				}
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, apiContent{Role: "user", Parts: parts})
		}
	}
	return contents
}

func (c *Client) buildRequest(req gori.Request) apiRequest {
	out := apiRequest{Contents: buildContents(req)}
	sys := req.System
	for _, m := range req.Messages {
		if m.Role == gori.RoleSystem {
			if t := m.Text(); t != "" {
				if sys != "" {
					sys += "\n\n"
				}
				sys += t
			}
		}
	}
	if sys != "" {
		out.SystemInstruction = &apiContent{Parts: []apiPart{{Text: sys}}}
	}
	if len(req.Tools) > 0 {
		decls := make([]apiFunctionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, apiFunctionDecl{Name: t.Name, Description: t.Description, Parameters: t.Schema})
		}
		out.Tools = []apiToolDecl{{FunctionDeclarations: decls}}
	}
	if req.ToolChoice != "" {
		out.ToolConfig = &apiToolConfig{FunctionCallingConfig: apiFunctionCallingConfig{
			Mode:                 "ANY",
			AllowedFunctionNames: []string{req.ToolChoice},
		}}
	}
	gc := &genConfig{MaxOutputTokens: req.MaxTokens}
	if req.Temperature != nil {
		gc.Temperature = req.Temperature
	}
	if req.Thinking.Mode != gori.ThinkingOff {
		tc := &thinkingConfig{IncludeThoughts: true}
		if req.Thinking.Mode == gori.ThinkingBudget && req.Thinking.Budget > 0 {
			b := req.Thinking.Budget
			tc.ThinkingBudget = &b
		}
		gc.ThinkingConfig = tc
	}
	for _, mod := range req.ResponseModalities {
		gc.ResponseModalities = append(gc.ResponseModalities, strings.ToUpper(mod))
	}
	out.GenerationConfig = gc
	return out
}

func (c *Client) post(ctx context.Context, model, method, query string, body apiRequest) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/models/%s:%s", c.baseURL, model, method)
	if query != "" {
		url += "?" + query
	}
	resp, err := httpretry.Do(ctx, c.http, c.retry, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-goog-api-key", c.apiKey)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) // bounded: a hostile server cannot balloon the error
		resp.Body.Close()
		return nil, fmt.Errorf("google: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return resp, nil
}

type apiResponse struct {
	Candidates []struct {
		Content      apiContent `json:"content"`
		FinishReason string     `json:"finishReason"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func finishReason(s string, hasTool bool) gori.StopReason {
	if hasTool {
		return gori.StopToolUse
	}
	switch s {
	case "STOP":
		return gori.StopEndTurn
	case "MAX_TOKENS":
		return gori.StopMaxTokens
	default:
		return gori.StopOther
	}
}

// inlineToContent converts an inline-data response part into Image or Audio.
func inlineToContent(d *apiInlineData) gori.Content {
	raw, err := base64.StdEncoding.DecodeString(d.Data)
	if err != nil || len(raw) == 0 {
		return nil
	}
	if strings.HasPrefix(d.MimeType, "audio/") {
		return gori.Audio{MediaType: d.MimeType, Data: raw}
	}
	return gori.Image{MediaType: d.MimeType, Data: raw}
}

// Complete runs a non-streaming completion.
func (c *Client) Complete(ctx context.Context, req gori.Request) (gori.Response, error) {
	if err := gori.SystemTextOnly(req.Messages); err != nil {
		return gori.Response{}, fmt.Errorf("google: %w", err)
	}
	resp, err := c.post(ctx, req.Model, "generateContent", "", c.buildRequest(req))
	if err != nil {
		return gori.Response{}, err
	}
	defer httpretry.DrainClose(resp.Body)
	var parsed apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return gori.Response{}, fmt.Errorf("google: decode response: %w", err)
	}
	if len(parsed.Candidates) == 0 {
		return gori.Response{}, fmt.Errorf("google: empty candidates")
	}
	cand := parsed.Candidates[0]
	var content []gori.Content
	hasTool := false
	callIdx := 0
	var thinking strings.Builder
	var text strings.Builder
	for _, p := range cand.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			hasTool = true
			id := p.FunctionCall.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", callIdx)
			}
			callIdx++
			input := p.FunctionCall.Args
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			content = append(content, gori.ToolUse{ID: id, Name: p.FunctionCall.Name, Input: input})
		case p.InlineData != nil:
			if c := inlineToContent(p.InlineData); c != nil {
				content = append(content, c)
			}
		case p.Thought && p.Text != "":
			thinking.WriteString(p.Text)
		case p.Text != "":
			text.WriteString(p.Text)
		}
	}
	var head []gori.Content
	if thinking.Len() > 0 {
		head = append(head, gori.Thinking{Text: thinking.String()})
	}
	if text.Len() > 0 {
		head = append(head, gori.Text{Text: text.String()})
	}
	content = append(head, content...)
	return gori.Response{
		Message:    gori.Message{Role: gori.RoleAssistant, Content: content},
		StopReason: finishReason(cand.FinishReason, hasTool),
		Usage: gori.Usage{
			InputTokens:    parsed.UsageMetadata.PromptTokenCount,
			OutputTokens:   parsed.UsageMetadata.CandidatesTokenCount,
			ThinkingTokens: parsed.UsageMetadata.ThoughtsTokenCount,
		},
	}, nil
}

// Stream runs a streaming completion over streamGenerateContent (alt=sse).
func (c *Client) Stream(ctx context.Context, req gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	if err := gori.SystemTextOnly(req.Messages); err != nil {
		return gori.Response{}, fmt.Errorf("google: %w", err)
	}
	resp, err := c.post(ctx, req.Model, "streamGenerateContent", "alt=sse", c.buildRequest(req))
	if err != nil {
		return gori.Response{}, err
	}
	defer resp.Body.Close()

	scanner := sse.NewScanner(resp.Body)
	var text, thinking strings.Builder
	var tools []gori.ToolUse
	var media []gori.Content
	callIdx := 0
	out := gori.Response{Message: gori.Message{Role: gori.RoleAssistant}}
	finish := ""

	for {
		ev, serr := scanner.Next()
		if serr == io.EOF {
			// no finishReason ⇒ truncated
			if finish == "" {
				return gori.Response{}, fmt.Errorf("google: stream ended unexpectedly (no finishReason)")
			}
			break
		}
		if serr != nil {
			return gori.Response{}, serr
		}
		if ev.Data == "" {
			continue
		}
		var chunk apiResponse
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			return gori.Response{}, err
		}
		if chunk.Error != nil {
			return gori.Response{}, fmt.Errorf("google: stream error: %s: %s", chunk.Error.Status, chunk.Error.Message)
		}
		// promptFeedback with no candidates is a content block, not a truncation
		if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" {
			return gori.Response{}, fmt.Errorf("google: prompt blocked: %s", chunk.PromptFeedback.BlockReason)
		}
		if chunk.UsageMetadata.PromptTokenCount > 0 {
			out.Usage.InputTokens = chunk.UsageMetadata.PromptTokenCount
		}
		if chunk.UsageMetadata.CandidatesTokenCount > 0 {
			out.Usage.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
		}
		if chunk.UsageMetadata.ThoughtsTokenCount > 0 {
			out.Usage.ThinkingTokens = chunk.UsageMetadata.ThoughtsTokenCount
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		cand := chunk.Candidates[0]
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", callIdx)
				}
				callIdx++
				input := p.FunctionCall.Args
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				tools = append(tools, gori.ToolUse{ID: id, Name: p.FunctionCall.Name, Input: input})
				if err := fn(gori.StreamEvent{Type: gori.EventToolStart, ToolID: id, ToolName: p.FunctionCall.Name}); err != nil {
					return gori.Response{}, err
				}
				if err := fn(gori.StreamEvent{Type: gori.EventToolDelta, ToolArgs: string(input)}); err != nil {
					return gori.Response{}, err
				}
				if err := fn(gori.StreamEvent{Type: gori.EventToolStop, ToolID: id}); err != nil {
					return gori.Response{}, err
				}
			case p.InlineData != nil:
				if c := inlineToContent(p.InlineData); c != nil {
					media = append(media, c)
				}
			case p.Thought && p.Text != "":
				thinking.WriteString(p.Text)
				if err := fn(gori.StreamEvent{Type: gori.EventThinkingDelta, Text: p.Text}); err != nil {
					return gori.Response{}, err
				}
			case p.Text != "":
				text.WriteString(p.Text)
				if err := fn(gori.StreamEvent{Type: gori.EventTextDelta, Text: p.Text}); err != nil {
					return gori.Response{}, err
				}
			}
		}
	}

	if thinking.Len() > 0 {
		out.Message.Content = append(out.Message.Content, gori.Thinking{Text: thinking.String()})
	}
	if text.Len() > 0 {
		out.Message.Content = append(out.Message.Content, gori.Text{Text: text.String()})
	}
	out.Message.Content = append(out.Message.Content, media...)
	for _, t := range tools {
		out.Message.Content = append(out.Message.Content, t)
	}
	out.StopReason = finishReason(finish, len(tools) > 0)
	if err := fn(gori.StreamEvent{Type: gori.EventDone, Usage: &out.Usage}); err != nil {
		return gori.Response{}, err
	}
	return out, nil
}
