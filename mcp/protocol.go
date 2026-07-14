// Package mcp implements the Model Context Protocol (spec 2025-11-25) over the
// stdio transport, using only the standard library. Gori can act as an MCP
// server (exposing its tools and agents) and as an MCP client (consuming an
// external server's tools as gori.Tool values).
package mcp

import "encoding/json"

// ProtocolVersion is the MCP spec revision this implementation prefers. The
// client accepts whatever revision the server negotiates (the subset it speaks —
// initialize, tools/list, tools/call — is wire-identical across published
// revisions); the server echoes a requested version only when it appears in
// supportedVersions, offering ProtocolVersion otherwise.
const ProtocolVersion = "2025-11-25"

// supportedVersions lists the MCP spec revisions the server will agree to when a
// client requests them, preferred first.
var supportedVersions = []string{ProtocolVersion}

func versionSupported(v string) bool {
	for _, s := range supportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// Implementation identifies a client or server.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolsCapability advertises tool support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerCapabilities is the server's advertised capability set.
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ClientCapabilities is the client's advertised capability set.
type ClientCapabilities struct{}

// InitializeParams is sent by the client to begin a session.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Implementation     `json:"clientInfo"`
}

// InitializeResult is the server's handshake response.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
}

// Tool is an MCP tool definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ListToolsResult is the tools/list response. NextCursor, when non-empty, means
// more tools are available and the client should re-request with that cursor.
type ListToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// CallToolParams is the tools/call request.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Content is one block of a tool result. Only text is fully modelled; non-text
// blocks (image/audio/resource) carry their type and mime so the client can
// surface them rather than silently dropping them.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"` // base64 for image/audio blocks
}

// CallToolResult is the tools/call response.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

func textResult(s string, isErr bool) CallToolResult {
	return CallToolResult{Content: []Content{{Type: "text", Text: s}}, IsError: isErr}
}
