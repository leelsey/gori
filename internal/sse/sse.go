// Package sse parses Server-Sent Events streams using only the standard library.
// It uses a bufio.Reader rather than bufio.Scanner so individual data lines are
// not bounded by the scanner token-size limit (provider streams can carry large
// base64 payloads).
package sse

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// maxEventBytes caps the accumulated data of a single SSE event, guarding against
// a malformed or unbounded stream exhausting memory.
const maxEventBytes = 16 << 20 // 16 MiB

// Event is a single server-sent event.
type Event struct {
	Type  string // the "event:" field, empty when absent
	Data  string // concatenated "data:" fields, joined by newlines
	ID    string // the "id:" field, empty when absent
	Retry string // the "retry:" field (reconnection time, ms), empty when absent
}

// Scanner reads Events from an SSE stream.
type Scanner struct {
	r       *bufio.Reader
	bomDone bool
}

// NewScanner returns a Scanner reading from r.
func NewScanner(r io.Reader) *Scanner {
	return &Scanner{r: bufio.NewReader(r)}
}

// Next returns the next event. It returns io.EOF when the stream is exhausted
// with no further complete event.
func (s *Scanner) Next() (Event, error) {
	var ev Event
	var data []string
	var size int
	dispatch := func() (Event, bool) {
		if len(data) == 0 {
			ev.Type = "" // WHATWG: an empty data buffer fires nothing and resets the type
			return Event{}, false
		}
		ev.Data = strings.Join(data, "\n")
		return ev, true
	}
	for {
		line, err := s.readLine()
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if e, ok := dispatch(); ok {
					return e, nil
				}
			case strings.HasPrefix(line, ":"):
				// comment line, ignore
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					ev.Type = value
				case "data":
					size += len(value) + 1 // include the '\n' join separator in the cap
					if size > maxEventBytes {
						return Event{}, fmt.Errorf("sse: event exceeds %d bytes", maxEventBytes)
					}
					data = append(data, value)
				case "id":
					if !strings.ContainsRune(value, '\x00') { // WHATWG: ignore an id containing NUL
						ev.ID = value
					}
				case "retry":
					ev.Retry = value
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				if e, ok := dispatch(); ok {
					return e, nil
				}
			}
			return Event{}, err
		}
	}
}

// stripBOM discards a single leading UTF-8 BOM at the start of the stream, as the
// SSE spec requires, so the first field name is not corrupted by a BOM prefix.
func (s *Scanner) stripBOM() {
	if s.bomDone {
		return
	}
	s.bomDone = true
	if b, err := s.r.Peek(3); err == nil && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = s.r.Discard(3)
	}
}

// readLine reads one SSE line, treating LF, CR, or CRLF as the terminator (per
// the SSE spec) and returning the content with a single normalised '\n' appended
// when a terminator was found (empty + io.EOF at end of stream). It bounds the
// line to maxEventBytes so a single newline-less line cannot force an arbitrarily
// large allocation.
func (s *Scanner) readLine() (string, error) {
	s.stripBOM()
	var sb strings.Builder
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return sb.String(), err
		}
		switch b {
		case '\n':
			sb.WriteByte('\n')
			return sb.String(), nil
		case '\r':
			if nb, err := s.r.ReadByte(); err == nil && nb != '\n' {
				_ = s.r.UnreadByte() // lone CR: put back the non-LF byte
			}
			sb.WriteByte('\n') // normalise CR / CRLF to LF
			return sb.String(), nil
		default:
			if sb.Len() >= maxEventBytes {
				return "", fmt.Errorf("sse: line exceeds %d bytes", maxEventBytes)
			}
			sb.WriteByte(b)
		}
	}
}
