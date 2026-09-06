package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxMessageBytes caps one frame, and is enforced BEFORE the bytes are held.
//
// Without it, a `Content-Length: 99999999999` header — from a hostile client or
// a desynchronised stream — becomes a 100 GB allocation and an OOM kill of the
// agent's whole session, and a peer that simply never sends a newline does the
// same thing to the line framing. 8 MiB is far above any real tools/call: the
// largest thing this server ever RECEIVES is a qualified name.
const maxMessageBytes = 8 << 20

// framing is how messages are delimited on the wire.
//
// The MCP stdio transport is newline-delimited, and that is what this server
// speaks by default and what any spec-conformant client will send. It also
// accepts LSP-style `Content-Length` headers, because that framing is what
// several editor-embedded MCP hosts inherited from their language-server
// plumbing and a server that refuses it simply hangs at initialize with no
// diagnostic anywhere.
//
// The reply framing MIRRORS the request's rather than being configured. A
// mismatch is invisible from this side — the bytes go out fine — and total
// from the client's: it waits forever for a header that never comes.
type framing int

const (
	framingLine framing = iota
	framingHeader
)

// conn is one stdio message channel. Not safe for concurrent use, which is
// fine and deliberate: the server handles one request at a time (see
// Server.Serve).
type conn struct {
	r       *bufio.Reader
	w       io.Writer
	framing framing
}

func newConn(r io.Reader, w io.Writer) *conn {
	return &conn{r: bufio.NewReader(r), w: w}
}

// readMessage returns the next frame's payload, and records which framing the
// peer used so writeMessage can answer in kind.
//
// Returns io.EOF when the client closed stdin, which is the spec's shutdown
// signal, not an error.
func (c *conn) readMessage() ([]byte, error) {
	line, err := c.readNonBlankLine()
	if err != nil {
		return nil, err
	}
	length, isHeader, err := parseContentLength(line)
	if err != nil {
		return nil, err
	}
	if !isHeader {
		c.framing = framingLine
		return []byte(line), nil
	}
	c.framing = framingHeader
	return c.readHeaderBody(length)
}

// readNonBlankLine skips blank lines between frames. Some clients pad with a
// stray CRLF after a message; treating that as an empty frame would produce a
// spurious parse error on every message.
//
// A final line with no trailing newline is still returned — the EOF surfaces
// on the NEXT call — so a client that writes its last frame and closes does
// not lose it.
func (c *conn) readNonBlankLine() (string, error) {
	for {
		line, err := c.readLine()
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed != "" {
			return trimmed, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			return "", err
		}
	}
}

// readLine reads one newline-terminated line, refusing to hold more than
// maxMessageBytes.
//
// bufio.Reader.ReadString would happily grow to whatever the peer sends before
// anyone gets to check the length, which makes a cap applied afterwards a
// statement about what is accepted rather than about what is allocated.
// ReadSlice returns bufio.ErrBufferFull instead, giving us a place to stop.
func (c *conn) readLine() (string, error) {
	var buf bytes.Buffer
	for {
		chunk, err := c.r.ReadSlice('\n')
		if buf.Len()+len(chunk) > maxMessageBytes {
			return "", fmt.Errorf("mcp: frame exceeds the %d byte cap", maxMessageBytes)
		}
		buf.Write(chunk)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buf.String(), io.EOF
			}
			return buf.String(), fmt.Errorf("mcp: read frame: %w", err)
		}
		return buf.String(), nil
	}
}

// parseContentLength inspects a frame's first line. A line that starts with
// `Content-Length:` opens a header block; anything else IS the message.
func parseContentLength(line string) (length int, isHeader bool, err error) {
	const prefix = "content-length:"
	if !strings.HasPrefix(strings.ToLower(line), prefix) {
		return 0, false, nil // the line IS the message; readLine already bounded it
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(line[len(prefix):]))
	if convErr != nil {
		return 0, true, fmt.Errorf("mcp: malformed Content-Length %q: %w", line, convErr)
	}
	if n < 0 || n > maxMessageBytes {
		return 0, true, fmt.Errorf("mcp: Content-Length %d outside 0..%d", n, maxMessageBytes)
	}
	return n, true, nil
}

// readHeaderBody consumes the rest of the header block and then exactly
// `length` bytes of payload.
//
// The remaining header lines go through readLine for the same reason the first
// one did: an endless header line is the same denial of service as an endless
// message, and the blank-line terminator may never arrive.
func (c *conn) readHeaderBody(length int) ([]byte, error) {
	for {
		line, err := c.readLine()
		if strings.TrimRight(line, "\r\n") == "" {
			break // blank line terminates the header block
		}
		if err != nil {
			return nil, err
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return nil, fmt.Errorf("mcp: read %d byte frame body: %w", length, err)
	}
	return body, nil
}

// writeMessage emits one frame in whatever framing the peer last used.
func (c *conn) writeMessage(payload []byte) error {
	var err error
	if c.framing == framingHeader {
		_, err = fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
	} else {
		_, err = fmt.Fprintf(c.w, "%s\n", payload)
	}
	if err != nil {
		return fmt.Errorf("mcp: write frame: %w", err)
	}
	return nil
}
