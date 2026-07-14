// Package rpc provides JSON-RPC 2.0 message transport plus a small Server and
// Client over newline-delimited framing. Pure standard library.
package rpc

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"sync"
)

// Transport carries framed JSON-RPC messages. Implementations are safe for one
// concurrent reader and one concurrent writer.
type Transport interface {
	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error
	Close() error
}

// streamTransport frames messages as newline-delimited JSON over an io stream.
type streamTransport struct {
	r   *bufio.Reader
	w   io.Writer
	wmu sync.Mutex
	c   io.Closer
}

// NewStreamTransport frames newline-delimited JSON over r/w. c (may be nil) is
// closed by Close.
func NewStreamTransport(r io.Reader, w io.Writer, c io.Closer) Transport {
	return &streamTransport{r: bufio.NewReader(r), w: w, c: c}
}

// Stdio returns a Transport over the process stdin/stdout (the MCP stdio model).
func Stdio() Transport { return NewStreamTransport(os.Stdin, os.Stdout, nil) }

func (t *streamTransport) ReadMessage() ([]byte, error) {
	for {
		line, err := t.r.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) > 0 {
			return trimmed, nil
		}
		if err != nil {
			return nil, err
		}
		// blank keep-alive line; keep reading
	}
}

func (t *streamTransport) WriteMessage(b []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	if _, err := t.w.Write(b); err != nil {
		return err
	}
	_, err := t.w.Write([]byte{'\n'})
	return err
}

func (t *streamTransport) Close() error {
	if t.c != nil {
		return t.c.Close()
	}
	return nil
}

// NewPipe returns two in-process transports wired to each other, for tests and
// running an MCP server and client in the same process. Each end's Close closes
// both the pipe it writes to (signalling the peer's reader) and its own reader,
// so Close also unblocks this end's own ReadMessage.
func NewPipe() (Transport, Transport) {
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	end1 := &streamTransport{r: bufio.NewReader(r1), w: w2, c: multiCloser{r1, w2}}
	end2 := &streamTransport{r: bufio.NewReader(r2), w: w1, c: multiCloser{r2, w1}}
	return end1, end2
}

// multiCloser closes several io.Closers, returning the first error.
type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var err error
	for _, c := range m {
		if e := c.Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}
