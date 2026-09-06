package mcp

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

// A client that speaks the spec's newline framing must get newline-framed
// answers, and a client that speaks LSP-style Content-Length headers must get
// those back. Replying in the wrong framing is invisible to the server and
// fatal to the client, which is why the choice is asserted rather than assumed.
func TestConn_LineFramedRoundTrip(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	var out bytes.Buffer
	c := newConn(in, &out)

	for _, want := range []string{`"id":1`, `"id":2`} {
		msg, err := c.readMessage()
		if err != nil {
			t.Fatalf("readMessage: %v", err)
		}
		if !strings.Contains(string(msg), want) {
			t.Fatalf("readMessage = %s, want it to contain %s", msg, want)
		}
	}
	if _, err := c.readMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("readMessage after last frame = %v, want io.EOF", err)
	}

	if err := c.writeMessage([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
	if got := out.String(); got != `{"ok":true}`+"\n" {
		t.Fatalf("written frame = %q, want a newline-terminated line", got)
	}
}

func TestConn_HeaderFramedRoundTrip(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":7,"method":"ping"}`
	in := strings.NewReader("Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
	var out bytes.Buffer
	c := newConn(in, &out)

	msg, err := c.readMessage()
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if string(msg) != body {
		t.Fatalf("readMessage = %s, want %s", msg, body)
	}

	if err := c.writeMessage([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
	want := "Content-Length: 11\r\n\r\n" + `{"ok":true}`
	if out.String() != want {
		t.Fatalf("written frame = %q, want %q", out.String(), want)
	}
}

// A declared length larger than the cap must be refused before it is
// allocated: the framing layer is the only thing standing between a hostile
// or broken client and an out-of-memory kill of the whole agent session.
func TestConn_RejectsOversizedContentLength(t *testing.T) {
	in := strings.NewReader("Content-Length: 99999999999\r\n\r\n")
	c := newConn(in, io.Discard)
	if _, err := c.readMessage(); err == nil {
		t.Fatal("readMessage accepted an oversized Content-Length; want an error")
	}
}

// The same cap has to hold for the newline framing, where there is no
// declared length to check: a peer that never sends a newline must be refused
// at the cap rather than accumulated until the process dies.
func TestConn_RejectsOversizedLineFrame(t *testing.T) {
	c := newConn(strings.NewReader(strings.Repeat("x", maxMessageBytes+10)), io.Discard)
	if _, err := c.readMessage(); err == nil {
		t.Fatal("readMessage accepted a frame larger than the cap; want an error")
	}
}

func TestConn_SkipsBlankLinesBetweenFrames(t *testing.T) {
	in := strings.NewReader("\n\n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	c := newConn(in, io.Discard)
	if _, err := c.readMessage(); err != nil {
		t.Fatalf("readMessage: %v", err)
	}
}

// The spec forbids embedded newlines in a stdio frame. Any payload carrying a
// literal newline (a doc comment, a multi-line reason) must come out escaped,
// not split across two frames.
func TestEncodeMessage_NeverEmitsEmbeddedNewlines(t *testing.T) {
	raw, err := encodeMessage(map[string]any{"text": "line one\nline two", "html": "<a>&</a>"})
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}
	if bytes.ContainsRune(raw, '\n') {
		t.Fatalf("encoded message contains a literal newline: %q", raw)
	}
	// HTML escaping is off so file paths, generics and channel directions stay
	// readable to the agent rather than arriving as < soup.
	if !bytes.Contains(raw, []byte("<a>&</a>")) {
		t.Fatalf("encoded message = %s, want unescaped HTML characters", raw)
	}
}
