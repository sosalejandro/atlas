package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Protocol revisions this server speaks.
//
// Both are handshake-based (initialize / notifications/initialized). The
// 2026-07-28 revision replaced that handshake with per-request version
// declaration and a server/discover RPC; supporting it is a separate change,
// and until then a client that asks for it is answered with the newest
// revision here, which is exactly what the spec's negotiation rule prescribes.
const (
	PreferredProtocolVersion = "2025-11-25"
	fallbackProtocolVersion  = "2025-06-18"
)

var supportedProtocolVersions = []string{PreferredProtocolVersion, fallbackProtocolVersion}

// Options configures a Server. Graph, Coverage and Scorer are required; a nil
// one is a programming error and New says so rather than panicking on the
// first tools/call, which would surface to the user as a dead MCP server with
// no message anywhere.
type Options struct {
	// Name and Version identify this server to the client. Both default.
	Name    string
	Version string

	Graph     GraphIndex
	Coverage  CoverageIndex
	Scorer    Scorer
	Freshness FreshnessFunc
	Limits    Limits
}

// Server is one MCP stdio session.
//
// It handles requests strictly one at a time. Concurrency would buy nothing
// here — the store is a single SQLite connection and an agent's calls are
// serialised by its own turn structure — while costing a write lock on the
// output stream and a whole class of interleaving bugs in a protocol where a
// half-written frame corrupts every message after it.
type Server struct {
	tools   []tool
	byName  map[string]tool
	name    string
	version string

	initialized bool
	negotiated  string
}

// New validates the options and builds the tool catalog once.
func New(opts Options) (*Server, error) {
	switch {
	case opts.Graph == nil:
		return nil, fmt.Errorf("mcp: Options.Graph is required")
	case opts.Coverage == nil:
		return nil, fmt.Errorf("mcp: Options.Coverage is required")
	case opts.Scorer == nil:
		return nil, fmt.Errorf("mcp: Options.Scorer is required")
	}
	ts := &toolset{
		graph:     opts.Graph,
		coverage:  opts.Coverage,
		scorer:    opts.Scorer,
		freshness: opts.Freshness,
		limits:    opts.Limits.withDefaults(),
	}
	s := &Server{
		tools:   ts.catalog(),
		byName:  map[string]tool{},
		name:    orDefault(opts.Name, "atlas"),
		version: orDefault(opts.Version, "dev"),
	}
	for _, t := range s.tools {
		s.byName[t.Name] = t
	}
	return s, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Serve reads framed messages from in and writes framed responses to out
// until in reaches EOF.
//
// EOF is the spec's shutdown signal — the client closes the server's stdin and
// waits for the process to exit — so it returns nil, not an error. A framing
// error, by contrast, means the stream is desynchronised and every subsequent
// byte is untrustworthy: there is no way to resynchronise a length-prefixed
// stream, so Serve stops rather than emitting confident nonsense.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	c := newConn(in, out)
	for {
		if err := ctx.Err(); err != nil {
			return nil // cancelled by the caller: an orderly stop, not a failure
		}
		raw, err := c.readMessage()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		resp, answer := s.handle(ctx, raw)
		if !answer {
			continue // a notification: the spec forbids a response
		}
		payload, err := encodeMessage(resp)
		if err != nil {
			return err
		}
		if err := c.writeMessage(payload); err != nil {
			return err
		}
	}
}

// handle turns one inbound frame into at most one response. The bool is false
// for notifications, which must not be answered at all.
func (s *Server) handle(ctx context.Context, raw []byte) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		// No id could be recovered, so the response carries an explicit null,
		// and the loop continues: one malformed frame is not a reason to make
		// the agent restart the server.
		return errorFor(nil, CodeParseError, "malformed JSON frame: "+err.Error(), nil), true
	}
	if req.JSONRPC != jsonRPCVersion {
		if req.isNotification() {
			return rpcResponse{}, false
		}
		return errorFor(req.ID, CodeInvalidRequest,
			fmt.Sprintf("jsonrpc must be %q, got %q", jsonRPCVersion, req.JSONRPC), nil), true
	}
	if req.isNotification() {
		s.handleNotification(req)
		return rpcResponse{}, false
	}
	return s.dispatch(ctx, req), true
}

// handleNotification consumes the notifications this server cares about and
// ignores the rest. Ignoring is correct, not lazy: an unknown notification is
// explicitly not an error in JSON-RPC, and answering one would put an
// uncorrelatable frame on the stream.
func (s *Server) handleNotification(req rpcRequest) {
	if req.Method == "notifications/initialized" {
		s.initialized = true
	}
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "ping":
		// Allowed in any phase, per the lifecycle spec, and answered with an
		// empty result object.
		return resultFor(req.ID, map[string]any{})
	}
	// Everything else belongs to the operation phase. Answering it before the
	// handshake would mean answering with capabilities that were never
	// negotiated.
	if !s.initialized {
		return errorFor(req.ID, CodeInvalidRequest,
			"server not initialized: send `initialize` and the `notifications/initialized` notification first", nil)
	}
	switch req.Method {
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return errorFor(req.ID, CodeMethodNotFound, "unknown method: "+req.Method, nil)
	}
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// handleInitialize negotiates the protocol version and declares capabilities.
//
// Version negotiation follows the spec's rule literally: echo the client's
// version when it is one we speak, otherwise answer with the latest we do
// speak and let the client decide whether it can live with that. Erroring
// instead would leave a client with nothing to fall back to.
//
// Only `tools` is declared. Advertising resources or prompts we do not
// implement would make a conforming client issue requests that can only fail.
// `listChanged` is false because the catalog is fixed at startup.
func (s *Server) handleInitialize(req rpcRequest) rpcResponse {
	var params initializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errorFor(req.ID, CodeInvalidParams, "initialize: params must be an object: "+err.Error(), nil)
		}
	}
	s.negotiated = negotiateVersion(params.ProtocolVersion)

	return resultFor(req.ID, map[string]any{
		"protocolVersion": s.negotiated,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    s.name,
			"title":   "Atlas code intelligence",
			"version": s.version,
		},
		"instructions": serverInstructions,
	})
}

func negotiateVersion(requested string) string {
	for _, v := range supportedProtocolVersions {
		if requested == v {
			return v
		}
	}
	return PreferredProtocolVersion
}

// serverInstructions is the standing context the client passes to its model.
// It is short on purpose — it costs tokens on every session — and says only
// the two things that change how the answers should be read.
const serverInstructions = "Atlas exposes this repository's feature/symbol graph, call edges and test attribution, " +
	"read-only. Two things govern how far to trust an answer. First, `surface_source` on a feature's implementation " +
	"surface ranks the derivation from execution evidence (`dynamic`) down to a human's annotation alone " +
	"(`direct-links`). Second, a `truncated` block means rows were withheld and a `no_data` block means atlas has " +
	"not been given the data yet — neither is evidence that nothing exists."

func (s *Server) handleToolsList(req rpcRequest) rpcResponse {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t.descriptor())
	}
	// No nextCursor: the catalog is seven entries and fits in one page.
	return resultFor(req.ID, map[string]any{"tools": out})
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, req rpcRequest) rpcResponse {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorFor(req.ID, CodeInvalidParams,
			"tools/call: params must be an object with `name` and `arguments`", nil)
	}
	if params.Name == "" {
		return errorFor(req.ID, CodeInvalidParams, "tools/call: `name` is required", nil)
	}
	t, ok := s.byName[params.Name]
	if !ok {
		return errorFor(req.ID, CodeInvalidParams,
			fmt.Sprintf("Unknown tool: %s", params.Name),
			map[string]any{"available": s.toolNames()})
	}
	args, err := parseToolArgs(params.Name, params.Arguments)
	if err != nil {
		return errorFor(req.ID, CodeInvalidParams, err.Error(), nil)
	}

	result, err := t.Handle(ctx, args)
	return s.respondToCall(req, result, err)
}

// respondToCall maps a handler's outcome onto the right channel.
//
//   - argError  -> -32602. The CALL was wrong; the model must fix its arguments.
//   - answerError -> isError result. The QUESTION was wrong; the model must ask
//     a different one, and can only do that if it can read the explanation.
//   - anything else -> -32603. Not something the agent can repair.
func (s *Server) respondToCall(req rpcRequest, result any, err error) rpcResponse {
	var argErr argError
	if errors.As(err, &argErr) {
		return errorFor(req.ID, CodeInvalidParams, argErr.Error(), nil)
	}
	var ansErr answerError
	if errors.As(err, &ansErr) {
		return resultFor(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": ansErr.Error()}},
			"isError": true,
		})
	}
	if err != nil {
		return errorFor(req.ID, CodeInternalError, "atlas: "+err.Error(), nil)
	}
	return s.successResult(req, result)
}

// successResult emits the result twice: as structuredContent, and serialised
// into a text block.
//
// The duplication is the spec's own recommendation and it is load-bearing in
// practice — most clients shipping today render only `content`, so a
// structured-only answer shows the model an empty tool result.
func (s *Server) successResult(req rpcRequest, result any) rpcResponse {
	text, err := encodeMessage(result)
	if err != nil {
		return errorFor(req.ID, CodeInternalError, "atlas: encode tool result: "+err.Error(), nil)
	}
	return resultFor(req.ID, map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": result,
		"isError":           false,
	})
}

// ToolDescriptor is one advertised tool, as plain data.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Description is what this server would advertise, without serving anything.
type Description struct {
	Server           string           `json:"server"`
	Version          string           `json:"version"`
	Transport        string           `json:"transport"`
	ProtocolVersions []string         `json:"protocol_versions"`
	ReadOnly         bool             `json:"read_only"`
	Limits           Limits           `json:"limits"`
	Tools            []ToolDescriptor `json:"tools"`
}

// Describe returns the server's catalog without opening a session.
//
// It exists because `atlas mcp` cannot honour the global --json envelope while
// serving: stdout IS the protocol stream, and a JSON envelope written onto it
// is precisely the "anything that is not a valid MCP message" the stdio
// transport forbids. Describing the server is the useful thing --json can mean
// here, and it is what an operator wiring the server into a client config
// actually wants to see.
func (s *Server) Describe(limits Limits) Description {
	out := Description{
		Server:           s.name,
		Version:          s.version,
		Transport:        "stdio",
		ProtocolVersions: supportedProtocolVersions,
		ReadOnly:         true,
		Limits:           limits.withDefaults(),
	}
	for _, t := range s.tools {
		out.Tools = append(out.Tools, ToolDescriptor{
			Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: t.InputSchema,
		})
	}
	return out
}

func (s *Server) toolNames() []string {
	out := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t.Name)
	}
	return out
}
