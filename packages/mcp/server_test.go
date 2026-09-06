package mcp

import (
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/store"
)

// newProtocolServer builds a server over an empty (migrated but never scanned)
// store. The protocol tests deliberately use one: the wire contract must hold
// before anybody has run `atlas scan`, which is exactly when an agent first
// meets this server.
func newProtocolServer(t *testing.T) *Server {
	t.Helper()
	return newServerOver(t, openTestStore(t))
}

func newServerOver(t *testing.T, s *store.Store) *Server {
	t.Helper()
	idx := FromStore(s)
	srv, err := New(Options{Graph: idx, Coverage: idx, Scorer: idx.Scorer()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func TestInitialize_NegotiatesAndAdvertisesTools(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	res := w.handshake()

	if res["protocolVersion"] != PreferredProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %q", res["protocolVersion"], PreferredProtocolVersion)
	}
	caps, ok := res["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result carries no capabilities: %v", res)
	}
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities = %v, want a tools capability", caps)
	}
	// A server that advertises resources or prompts it does not implement
	// makes a client issue requests that can only fail.
	for _, unsupported := range []string{"resources", "prompts", "sampling"} {
		if _, bad := caps[unsupported]; bad {
			t.Fatalf("capabilities advertise %q, which this server does not implement", unsupported)
		}
	}
	info, ok := res["serverInfo"].(map[string]any)
	if !ok || info["name"] == "" {
		t.Fatalf("serverInfo = %v, want a named server", res["serverInfo"])
	}
}

// The spec's rule is "respond with a version you DO support", not "fail". A
// client that asked for a future revision must be told what it can fall back
// to rather than being left to guess from an error.
func TestInitialize_UnknownVersionAnswersWithASupportedOne(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.sendJSON(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "1999-01-01"},
	})
	res, ok := w.recv()["result"].(map[string]any)
	if !ok {
		t.Fatal("initialize with an unknown version returned no result")
	}
	if res["protocolVersion"] != PreferredProtocolVersion {
		t.Fatalf("protocolVersion = %v, want the server's preferred %q",
			res["protocolVersion"], PreferredProtocolVersion)
	}
}

// An older client that names a revision we still speak must get that revision
// back, not ours: answering with a different string tells it to disconnect.
func TestInitialize_EchoesASupportedOlderVersion(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.sendJSON(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18"},
	})
	res, _ := w.recv()["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want the client's own 2025-06-18", res["protocolVersion"])
	}
}

func TestOperationBeforeInitializeIsRefused(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if got := rpcErrorCode(t, w.recv()); got != CodeInvalidRequest {
		t.Fatalf("tools/list before initialize = code %d, want %d", got, CodeInvalidRequest)
	}
}

// ping is the one request the spec allows either side to send at any time.
func TestPingIsAllowedBeforeInitialize(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	resp := w.recv()
	if _, bad := resp["error"]; bad {
		t.Fatalf("ping before initialize = %v, want an empty result", resp)
	}
}

func TestToolsList_EveryToolCarriesAUsableInputSchema(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.handshake()
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	res, _ := w.recv()["result"].(map[string]any)

	tools, ok := res["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list returned no tools: %v", res)
	}
	want := map[string]bool{
		"find_feature": false, "feature_surface": false, "symbol_info": false,
		"callers": false, "callees": false, "tests_covering": false, "coverage_for": false,
	}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		if _, expected := want[name]; !expected {
			t.Fatalf("tools/list advertises unexpected tool %q", name)
		}
		want[name] = true

		if desc, _ := tool["description"].(string); len(desc) < 20 {
			t.Errorf("%s: description = %q, too short to pick the tool from", name, desc)
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s: inputSchema is missing — an agent would have to guess arguments", name)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: inputSchema.type = %v, want \"object\"", name, schema["type"])
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("%s: inputSchema declares no properties", name)
		}
		for pname, praw := range props {
			p, _ := praw.(map[string]any)
			if p["type"] == nil {
				t.Errorf("%s.%s: property has no type", name, pname)
			}
			if d, _ := p["description"].(string); d == "" {
				t.Errorf("%s.%s: property has no description", name, pname)
			}
		}
		req, ok := schema["required"].([]any)
		if !ok || len(req) == 0 {
			t.Errorf("%s: inputSchema names no required argument", name)
		}
		// Read-only is a property of this whole surface; the hint is how a
		// client learns it without calling anything.
		ann, ok := tool["annotations"].(map[string]any)
		if !ok || ann["readOnlyHint"] != true || ann["destructiveHint"] != false {
			t.Errorf("%s: annotations = %v, want readOnlyHint true / destructiveHint false", name, ann)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tools/list omitted %q", name)
		}
	}
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.handshake()
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "resources/list"})
	if got := rpcErrorCode(t, w.recv()); got != CodeMethodNotFound {
		t.Fatalf("unknown method = code %d, want %d", got, CodeMethodNotFound)
	}
}

func TestUnknownToolIsInvalidParams(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.handshake()
	w.sendJSON(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "rm_rf", "arguments": map[string]any{}},
	})
	resp := w.recv()
	if got := rpcErrorCode(t, resp); got != CodeInvalidParams {
		t.Fatalf("unknown tool = code %d, want %d", got, CodeInvalidParams)
	}
	e, _ := resp["error"].(map[string]any)
	if msg, _ := e["message"].(string); !strings.Contains(msg, "rm_rf") {
		t.Errorf("error message = %q, want it to name the tool the client asked for", msg)
	}
}

func TestMalformedJSONIsAParseErrorWithNullID(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.send(`{"jsonrpc":"2.0","id":1,"method":`)
	resp := w.recv()
	if got := rpcErrorCode(t, resp); got != CodeParseError {
		t.Fatalf("malformed frame = code %d, want %d", got, CodeParseError)
	}
	if v, present := resp["id"]; !present || v != nil {
		t.Fatalf("parse-error id = %v (present=%v), want an explicit null", v, present)
	}
	// The connection must survive: one bad frame is not a reason to make the
	// agent restart the server.
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"})
	if got := w.recv()["id"]; got != float64(2) {
		t.Fatalf("after a parse error, next response id = %v, want 2", got)
	}
}

func TestWrongJSONRPCVersionIsInvalidRequest(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.send(`{"jsonrpc":"1.0","id":1,"method":"ping"}`)
	if got := rpcErrorCode(t, w.recv()); got != CodeInvalidRequest {
		t.Fatalf("jsonrpc 1.0 = code %d, want %d", got, CodeInvalidRequest)
	}
}

func TestMalformedToolParamsAreInvalidParams(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.handshake()

	cases := []struct {
		name   string
		params any
	}{
		{"params is not an object", "find_feature"},
		{"arguments is not an object", map[string]any{"name": "find_feature", "arguments": []any{"x"}}},
		{"tool name missing", map[string]any{"arguments": map[string]any{}}},
		{"required argument missing", map[string]any{"name": "symbol_info", "arguments": map[string]any{}}},
		{"required argument wrong type", map[string]any{"name": "symbol_info", "arguments": map[string]any{"qualified_name": 7}}},
		{"limit is not a number", map[string]any{"name": "find_feature", "arguments": map[string]any{"query": "x", "limit": "many"}}},
	}
	for i, tc := range cases {
		w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 100 + i, "method": "tools/call", "params": tc.params})
		if got := rpcErrorCode(t, w.recv()); got != CodeInvalidParams {
			t.Errorf("%s: code = %d, want %d", tc.name, got, CodeInvalidParams)
		}
	}
}

// A notification carries no id and MUST NOT be answered. Asserting on the id
// of the NEXT response catches a stray frame immediately; a timeout-based
// assertion would only make the suite slow.
func TestNotificationsAreNeverAnswered(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.handshake()
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 1}})
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "method": "no/such/notification"})
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "ping"})
	if got := w.recv()["id"]; got != float64(9) {
		t.Fatalf("next response id = %v, want 9 — a notification was answered", got)
	}
}

// Ids are opaque: a client that uses strings must get its own string back,
// byte for byte, or it cannot correlate the response.
func TestRequestIDIsEchoedVerbatim(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.handshake()
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": "req-abc", "method": "tools/list"})
	if got := w.recv()["id"]; got != "req-abc" {
		t.Fatalf("id = %v, want the string \"req-abc\"", got)
	}
}

// The first thing an agent does against a fresh checkout is ask a question.
// An empty answer reads as "there is nothing"; the truth is "nothing is
// indexed yet", and only one of those two prompts the agent to run a scan.
func TestUnscannedStoreReturnsNoDataNotAnEmptyList(t *testing.T) {
	w := startWire(t, newProtocolServer(t))
	w.handshake()

	sc := w.structured(w.call(2, "find_feature", map[string]any{"query": "checkout"}))
	nd, ok := sc["no_data"].(map[string]any)
	if !ok {
		t.Fatalf("find_feature on an unscanned store = %v, want a no_data envelope", sc)
	}
	if run, _ := nd["run"].(string); !strings.Contains(run, "atlas scan") && !strings.Contains(run, "atlas init") {
		t.Errorf("no_data.run = %q, want the command that would produce the data", run)
	}
	if _, bad := sc["features"]; bad {
		t.Errorf("no_data result also carries a features list: %v — an agent will read the empty list", sc)
	}
}

// The server writes only MCP messages to stdout; anything else corrupts the
// stream. Serve returning cleanly on EOF is what lets a client shut it down by
// closing stdin, as the spec's shutdown sequence requires.
func TestServeReturnsCleanlyOnEOF(t *testing.T) {
	srv := newProtocolServer(t)
	w := startWire(t, srv)
	w.handshake()
	if err := w.shutdown(); err != nil {
		t.Fatalf("Serve after stdin close = %v, want nil", err)
	}
}
