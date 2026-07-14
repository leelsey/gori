// Package clibackend implements gori.Provider by shelling out to an external
// agentic CLI (e.g. codex, claude, agy). It treats the CLI as a text-in /
// text-out black box: the external tool runs its own agent loop and tools, so
// this provider reports no tool/thinking capability of its own.
//
// Security: the command and arguments come from configuration controlled by the
// operator, never from model output. Do not populate Config from untrusted input.
package clibackend

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/leelsey/gori"
)

// PromptVia selects how the rendered prompt is delivered to the CLI.
type PromptVia string

const (
	PromptViaStdin PromptVia = "stdin" // written to the process stdin (default)
	PromptViaArg   PromptVia = "arg"   // substituted into Args at Placeholder
)

// Config describes how to invoke an external agentic CLI.
type Config struct {
	Name        string    // gori provider name, e.g. "claude-cli"
	Command     string    // executable, e.g. "claude"
	Args        []string  // arguments; Placeholder is replaced when Via == arg
	Via         PromptVia // how to pass the prompt (default stdin)
	Placeholder string    // token in Args to replace with the prompt (default "{{prompt}}")
}

// Client is a CLI-backed provider. It implements gori.Provider.
type Client struct {
	cfg Config
}

var _ gori.Provider = (*Client)(nil)

// New returns a Client for the given configuration.
func New(cfg Config) *Client {
	if cfg.Via == "" {
		cfg.Via = PromptViaStdin
	}
	if cfg.Placeholder == "" {
		cfg.Placeholder = "{{prompt}}"
	}
	if cfg.Name == "" {
		cfg.Name = "cli:" + cfg.Command
	}
	return &Client{cfg: cfg}
}

// Name reports the provider name.
func (c *Client) Name() string { return c.cfg.Name }

// Capabilities reports supported features. CLI backends only stream text.
func (c *Client) Capabilities() gori.Capabilities {
	return gori.Capabilities{Streaming: true}
}

// renderPrompt flattens the request into a plain-text transcript.
func renderPrompt(req gori.Request) string {
	var b strings.Builder
	if req.System != "" {
		b.WriteString("[system] ")
		b.WriteString(req.System)
		b.WriteString("\n\n")
	}
	for _, m := range req.Messages {
		text := messageText(m)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s\n\n", m.Role, text)
	}
	return strings.TrimSpace(b.String())
}

// messageText renders a message's content as plain text, including tool results
// (which gori.Message.Text() omits, as they are not Text blocks).
func messageText(m gori.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		switch v := c.(type) {
		case gori.Text:
			b.WriteString(v.Text)
		case gori.Plan:
			b.WriteString(strings.Join(v.Steps, "\n"))
		case gori.ToolResult:
			b.WriteString(v.Content)
		}
	}
	return b.String()
}

// completeUTF8Prefix returns the length of the longest prefix of b that ends on
// a UTF-8 rune boundary, excluding a trailing incomplete multi-byte rune.
func completeUTF8Prefix(b []byte) int {
	i := 0
	for i < len(b) {
		if !utf8.FullRune(b[i:]) {
			break // incomplete rune at the tail; wait for more bytes
		}
		_, size := utf8.DecodeRune(b[i:])
		i += size
	}
	return i
}

func (c *Client) command(ctx context.Context, prompt string) (*exec.Cmd, bool) {
	args := make([]string, len(c.cfg.Args))
	usedArg := false
	for i, a := range c.cfg.Args {
		if c.cfg.Via == PromptViaArg && strings.Contains(a, c.cfg.Placeholder) {
			a = strings.ReplaceAll(a, c.cfg.Placeholder, prompt)
			usedArg = true
		}
		args[i] = a
	}
	cmd := exec.CommandContext(ctx, c.cfg.Command, args...)
	stdin := c.cfg.Via == PromptViaStdin || (c.cfg.Via == PromptViaArg && !usedArg)
	if stdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	return cmd, stdin
}

// Complete runs the CLI once and returns its stdout as the assistant message.
func (c *Client) Complete(ctx context.Context, req gori.Request) (gori.Response, error) {
	cmd, _ := c.command(ctx, renderPrompt(req))
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return gori.Response{}, ctx.Err() // cancelled/timed out: surface ctx, not "signal: killed"
		}
		return gori.Response{}, fmt.Errorf("clibackend %s: %v: %s", c.cfg.Command, err, strings.TrimSpace(errb.String()))
	}
	return gori.Response{
		Message:    gori.AssistantText(strings.TrimSpace(out.String())),
		StopReason: gori.StopEndTurn,
	}, nil
}

// Stream runs the CLI and forwards stdout to fn as text deltas.
func (c *Client) Stream(ctx context.Context, req gori.Request, fn func(gori.StreamEvent) error) (gori.Response, error) {
	cmd, _ := c.command(ctx, renderPrompt(req))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return gori.Response{}, err
	}
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return gori.Response{}, err
	}
	var full strings.Builder
	reader := bufio.NewReader(stdout)
	buf := make([]byte, 4096)
	var pending []byte
	emit := func(s string) error {
		full.WriteString(s)
		if ferr := fn(gori.StreamEvent{Type: gori.EventTextDelta, Text: s}); ferr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return ferr
		}
		return nil
	}
	for {
		n, rerr := reader.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			// Emit only the complete-UTF-8 prefix so a multi-byte rune split
			// across read boundaries is not corrupted into U+FFFD.
			if k := completeUTF8Prefix(pending); k > 0 {
				if err := emit(string(pending[:k])); err != nil {
					return gori.Response{}, err
				}
				pending = append(pending[:0], pending[k:]...)
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				// A genuine read failure is not end-of-output: surface it
				// rather than returning partial output as a completed answer.
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				if ctx.Err() != nil {
					return gori.Response{}, ctx.Err()
				}
				return gori.Response{}, fmt.Errorf("clibackend %s: read output: %v: %s", c.cfg.Command, rerr, strings.TrimSpace(errb.String()))
			}
			break
		}
	}
	if len(pending) > 0 {
		if err := emit(string(pending)); err != nil {
			return gori.Response{}, err
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return gori.Response{}, ctx.Err() // cancelled/timed out: surface ctx, not "signal: killed"
		}
		return gori.Response{}, fmt.Errorf("clibackend %s: %v: %s", c.cfg.Command, err, strings.TrimSpace(errb.String()))
	}
	if err := fn(gori.StreamEvent{Type: gori.EventDone}); err != nil {
		return gori.Response{}, err
	}
	return gori.Response{
		Message:    gori.AssistantText(strings.TrimSpace(full.String())),
		StopReason: gori.StopEndTurn,
	}, nil
}
