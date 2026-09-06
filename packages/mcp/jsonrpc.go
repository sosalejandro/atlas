package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// jsonRPCVersion is the only "jsonrpc" value MCP accepts. A frame carrying
// anything else is rejected rather than tolerated: a client that thinks it is
// speaking 1.0 has different semantics for notifications and batches, and
// answering it as if it were 2.0 produces a subtly wrong conversation instead
// of an obvious one.
const jsonRPCVersion = "2.0"

// JSON-RPC 2.0 error codes, plus how this server maps MCP's two failure
// classes onto them.
//
// The split matters to an agent. A PROTOCOL error (these codes) says "the call
// itself was wrong" — bad method, bad arguments — and the model must change
// the call. A TOOL error (CallToolResult.IsError) says "the call was fine, the
// answer is a failure" — no such feature, no such symbol — and the model must
// change its question. Collapsing the two into one channel is what makes an
// agent retry the same malformed call forever.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	// CodeInvalidParams also covers an unknown tool name. That is not this
	// server's invention: the MCP tools specification's own worked example of
	// a protocol error is `{"code": -32602, "message": "Unknown tool: ..."}`.
	CodeInvalidParams = -32602
	CodeInternalError = -32603
)

// rpcRequest is an inbound frame. ID is kept as raw JSON because JSON-RPC ids
// are opaque — string, number, or null — and a client that sent "req-abc"
// cannot correlate a response that came back as 0.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the frame must NOT be answered.
//
// A JSON-RPC notification is a request with no "id" member. An explicit null
// id is treated the same way here: there is no value to correlate a response
// against, so replying would put an unmatched frame on a stream the client
// parses strictly.
func (r rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || bytes.Equal(bytes.TrimSpace(r.ID), []byte("null"))
}

// rpcResponse is an outbound frame. Exactly one of Result / Error is set.
//
// ID is not omitempty: a parse error has no id to echo and the spec requires
// an explicit null there, which an omitted field would not produce.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// nullID is the id used when a frame could not be parsed far enough to recover
// the client's own.
var nullID = json.RawMessage("null")

func resultFor(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: jsonRPCVersion, ID: idOrNull(id), Result: result}
}

func errorFor(id json.RawMessage, code int, msg string, data any) rpcResponse {
	return rpcResponse{
		JSONRPC: jsonRPCVersion,
		ID:      idOrNull(id),
		Error:   &rpcError{Code: code, Message: msg, Data: data},
	}
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return nullID
	}
	return id
}

// encodeMessage serialises one frame.
//
// Two deviations from json.Marshal's defaults, both forced by the transport:
//
//   - HTML escaping is off. Marshal would turn `<-chan T` and `a && b` into
//     < soup, which an agent then has to un-mangle to cite source.
//   - The encoder's trailing newline is stripped, because the framing layer
//     owns the delimiter and a stdio frame MUST NOT contain a newline of its
//     own. (Values with embedded newlines are still safe: the encoder escapes
//     those inside the string, it does not emit them raw.)
func encodeMessage(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("mcp: encode message: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
