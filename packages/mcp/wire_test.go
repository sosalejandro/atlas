package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/store"
)

// openTestStore mirrors packages/store's unexported test helper; the audit and
// sprintplan packages inline the same three lines for the same reason.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// wire drives a Server the way a real MCP client does: framed bytes in one
// pipe, framed bytes out another. Testing the handler functions directly would
// prove nothing about the wire format, and the wire format is the whole
// contract with a client we cannot run here.
type wire struct {
	t     *testing.T
	toS   *io.PipeWriter
	fromS *bufio.Reader
	done  chan error

	waitOnce sync.Once
	serveErr error
}

func startWire(t *testing.T, srv *Server) *wire {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	w := &wire{t: t, toS: inW, fromS: bufio.NewReader(outR), done: make(chan error, 1)}

	go func() {
		err := srv.Serve(context.Background(), inR, outW)
		_ = outW.Close()
		w.done <- err
	}()
	t.Cleanup(func() { _ = w.shutdown() })
	return w
}

// shutdown closes the server's stdin and waits for Serve to return, exactly
// as the spec's stdio shutdown sequence has a client do. Idempotent so a test
// can assert on the exit and still let t.Cleanup run.
func (w *wire) shutdown() error {
	w.waitOnce.Do(func() {
		_ = w.toS.Close()
		select {
		case w.serveErr = <-w.done:
		case <-time.After(5 * time.Second):
			w.t.Error("Serve did not return after stdin closed")
		}
	})
	return w.serveErr
}

func (w *wire) send(raw string) {
	w.t.Helper()
	if _, err := io.WriteString(w.toS, raw+"\n"); err != nil {
		w.t.Fatalf("send %s: %v", raw, err)
	}
}

func (w *wire) sendJSON(v any) {
	w.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		w.t.Fatalf("marshal request: %v", err)
	}
	w.send(string(raw))
}

// recv reads exactly one framed response. A test that expects no response
// asserts on the NEXT response's id instead of waiting for a timeout, so a
// stray extra frame fails the test loudly rather than passing quietly.
func (w *wire) recv() map[string]any {
	w.t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := w.fromS.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			w.t.Fatalf("recv: %v (partial %q)", r.err, r.line)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(r.line), &out); err != nil {
			w.t.Fatalf("recv: response is not JSON: %v (%q)", err, r.line)
		}
		if out["jsonrpc"] != "2.0" {
			w.t.Fatalf("recv: jsonrpc = %v, want \"2.0\" (%q)", out["jsonrpc"], r.line)
		}
		return out
	case <-time.After(5 * time.Second):
		w.t.Fatal("recv: timed out waiting for a response frame")
		return nil
	}
}

// handshake runs initialize + notifications/initialized and returns the
// initialize result, so the operation-phase tests do not each repeat it.
func (w *wire) handshake() map[string]any {
	w.t.Helper()
	w.sendJSON(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": PreferredProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "wire_test", "version": "0"},
		},
	})
	resp := w.recv()
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	res, ok := resp["result"].(map[string]any)
	if !ok {
		w.t.Fatalf("initialize returned no result: %v", resp)
	}
	return res
}

// call runs one tools/call and returns the CallToolResult.
func (w *wire) call(id int, name string, args map[string]any) map[string]any {
	w.t.Helper()
	w.sendJSON(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	resp := w.recv()
	if e, bad := resp["error"]; bad {
		w.t.Fatalf("tools/call %s returned a protocol error: %v", name, e)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		w.t.Fatalf("tools/call %s returned no result: %v", name, resp)
	}
	return res
}

// structured pulls the structuredContent object out of a CallToolResult and
// fails when the call reported isError.
func (w *wire) structured(res map[string]any) map[string]any {
	w.t.Helper()
	if res["isError"] == true {
		w.t.Fatalf("tool reported isError: %v", res["content"])
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		w.t.Fatalf("result carries no structuredContent: %v", res)
	}
	return sc
}

func rpcErrorCode(t *testing.T, resp map[string]any) int {
	t.Helper()
	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no error object: %v", resp)
	}
	code, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("error carries no numeric code: %v", e)
	}
	return int(code)
}
