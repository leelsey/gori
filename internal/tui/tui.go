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
			if command(agent, line, out, color) {
				return nil
			}
			continue
		}

		fmt.Fprint(out, paint(color, "1;32", "gori ◂ "))
		msg, err := agent.StreamMessage(ctx, gori.UserText(line), func(ev gori.StreamEvent) error {
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

func command(agent *gori.Agent, line string, out io.Writer, color bool) (stop bool) {
	switch strings.Fields(line)[0] {
	case "/exit", "/quit":
		return true
	case "/reset":
		agent.Session = gori.NewSession()
		fmt.Fprintln(out, paint(color, "2", "(session reset)"))
	case "/help":
		fmt.Fprintln(out, paint(color, "2", "commands: /reset  /exit  /help"))
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
