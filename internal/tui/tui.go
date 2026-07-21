// Package tui is a lightweight, standard-library-only interactive terminal
// session for the gori CLI. It lives under internal/ and is imported only by
// cmd/gori, so library consumers of github.com/leelsey/gori never pull it in.
package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/leelsey/gori"
)

// Run drives a line-based session until /exit or EOF, colourised only on a
// terminal. onFinal (optional) receives each completed assistant message.
func Run(ctx context.Context, agent *gori.Agent, in io.Reader, out io.Writer, onFinal func(gori.Message)) error {
	color := isTTY(out)
	fmt.Fprintln(out, paint(color, "1", "gori interactive")+" — /help for commands, /exit to quit")

	// Subscribe to the agent's lifecycle events for the /debug trace; the
	// buffered channel is drained after each turn, so nothing blocks.
	createdBus := agent.Bus == nil
	if createdBus {
		agent.Bus = gori.NewBus()
	}
	events, unsub := agent.Bus.Subscribe("*")
	defer func() {
		unsub()
		if createdBus {
			agent.Bus.Close()
			agent.Bus = nil
		}
	}()
	debug := false
	var pending []string
	// collect drains buffered events without printing; called from the stream
	// callback during a turn so a tool-heavy step cannot overflow the
	// subscription buffer, and again when the turn ends.
	collect := func() {
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				if l := traceLine(ev); l != "" {
					pending = append(pending, l)
				}
			default:
				return
			}
		}
	}
	dropSeen := agent.Bus.Dropped()
	flushTrace := func() {
		collect()
		if debug {
			for _, l := range pending {
				fmt.Fprintln(out, paint(color, "2", "("+l+")"))
			}
		}
		pending = pending[:0]
		// a rising drop count means the subscription buffer overflowed mid-turn
		if d := agent.Bus.Dropped(); d != dropSeen {
			if debug {
				fmt.Fprintf(out, "%s\n", paint(color, "2", fmt.Sprintf("(trace incomplete: %d event(s) dropped)", d-dropSeen)))
			}
			dropSeen = d
		}
	}

	lines, errc := readLines(ctx, in)
	for {
		fmt.Fprint(out, "\n"+paint(color, "1;36", "you ▸ "))
		var raw string
		select {
		case <-ctx.Done():
			fmt.Fprintln(out)
			return ctx.Err() // signal/timeout: stop even while idle at the prompt
		case l, ok := <-lines:
			if !ok {
				if err := <-errc; err != nil && err != io.EOF {
					return err
				}
				return nil // clean EOF (Ctrl-D)
			}
			raw = l
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if command(agent, line, out, color, &debug) {
				return nil
			}
			continue
		}

		fmt.Fprint(out, paint(color, "1;32", "gori ◂ "))
		msg, err := agent.StreamMessage(ctx, gori.UserText(line), func(ev gori.StreamEvent) error {
			collect()
			switch ev.Type {
			case gori.EventTextDelta:
				fmt.Fprint(out, ev.Text)
			case gori.EventThinkingDelta:
				fmt.Fprint(out, paint(color, "2", ev.Text))
			case gori.EventToolStart:
				fmt.Fprint(out, paint(color, "2", "\n(running "+ev.ToolName+"…)\n"))
			}
			return nil
		})
		fmt.Fprintln(out)
		flushTrace()
		if err != nil {
			fmt.Fprintln(out, paint(color, "31", "error: "+err.Error()))
			if ctx.Err() != nil {
				return ctx.Err() // context done (e.g. --timeout): stop, don't re-prompt forever
			}
			continue
		}
		if onFinal != nil {
			onFinal(msg)
		}
	}
}

// traceLine renders one /debug trace line; conversation-content kinds (start,
// message, done) return "" — they are already visible as the dialogue itself.
func traceLine(ev gori.Event) string {
	switch d := ev.Data.(type) {
	case gori.StepEvent:
		return fmt.Sprintf("step %d: %s — %s", d.Step, d.StopReason, d.Usage)
	case gori.ToolCallEvent:
		return "tool " + d.Name + " " + trunc(string(d.Input), 120)
	case gori.ToolResultEvent:
		if d.IsError {
			return "tool " + d.Name + " → error: " + trunc(d.Content, 200)
		}
		return "tool " + d.Name + " → " + trunc(d.Content, 200)
	case string:
		if ev.Kind == "error" {
			return "error: " + trunc(d, 200)
		}
	}
	return ""
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s) // cut on a rune boundary, not mid-character
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// maxLineBytes bounds a single input line so a newline-less stream cannot grow
// memory without limit (matching the old bufio.Scanner buffer cap).
const maxLineBytes = 1 << 20

// readLines delivers lines on a channel so the caller can select on ctx; lines
// over maxLineBytes error, and errc receives the terminal error (io.EOF when
// clean) before the channel closes.
func readLines(ctx context.Context, in io.Reader) (<-chan string, <-chan error) {
	lines := make(chan string)
	errc := make(chan error, 1)
	go func() {
		defer close(lines)
		r := bufio.NewReader(in)
		var sb strings.Builder
		for {
			chunk, err := r.ReadSlice('\n')
			sb.Write(chunk)
			// +1 allows the trailing newline: the bound applies to line content.
			if sb.Len() > maxLineBytes+1 {
				errc <- fmt.Errorf("input line exceeds %d bytes", maxLineBytes)
				return
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if s := sb.String(); s != "" {
				sb.Reset()
				select {
				case lines <- s:
				case <-ctx.Done():
					errc <- ctx.Err()
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	return lines, errc
}

func command(agent *gori.Agent, line string, out io.Writer, color bool, debug *bool) (stop bool) {
	switch strings.Fields(line)[0] {
	case "/exit", "/quit":
		return true
	case "/reset":
		agent.Session = gori.NewSession()
		agent.TotalUsage, agent.SessionUsage, agent.StepUsage = gori.Usage{}, gori.Usage{}, nil
		fmt.Fprintln(out, paint(color, "2", "(session reset)"))
	case "/usage":
		fmt.Fprintln(out, paint(color, "2", "(last run: "+agent.TotalUsage.String()+")"))
		fmt.Fprintln(out, paint(color, "2", "(session:  "+agent.SessionUsage.String()+")"))
	case "/debug":
		*debug = !*debug
		if *debug {
			fmt.Fprintln(out, paint(color, "2", "(debug on — per-turn step/tool trace)"))
		} else {
			fmt.Fprintln(out, paint(color, "2", "(debug off)"))
		}
	case "/help":
		fmt.Fprintln(out, paint(color, "2", "commands: /usage  /debug  /reset  /exit  /help"))
	default:
		fmt.Fprintln(out, paint(color, "2", "(unknown command "+strings.Fields(line)[0]+")"))
	}
	return false
}

func paint(color bool, code, s string) string {
	if !color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
