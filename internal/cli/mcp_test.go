package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// mcpFixture is a tempdir repo root plus .atlas/atlas.db, with the package
// singletons pointed at it. Mirrors reportFixture; the singletons make
// t.Parallel() unsafe here (see NewRootCmd's note).
type mcpFixture struct {
	root   string
	dbPath string
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	dbPath := filepath.Join(dir, ".atlas", "atlas.db")
	loaded = Config{repoRoot: dir, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}
	return &mcpFixture{root: dir, dbPath: dbPath}
}

// seed writes one feature whose annotated test reaches one production symbol,
// so a tools/call over the CLI has something real to answer with.
func (f *mcpFixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Features().Upsert(ctx, store.Feature{
		ID: "billing.invoice", Title: "Invoice generation", Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	end := 60
	implID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "internal/billing.Invoice", Kind: shared.KindFunc,
		FilePath: "internal/billing/invoice.go", Line: 42, EndLine: &end,
	})
	if err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: "billing.invoice", SymbolID: implID,
		Role: store.RoleImpl, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatalf("link feature symbol: %v", err)
	}
}

// runMCPSession drives `atlas mcp` end to end: framed requests on the
// command's stdin, framed responses on its stdout. This is the only test that
// proves the CLI wiring — a server that works in packages/mcp and is not
// reachable from the binary is not delivered.
func runMCPSession(t *testing.T, fix *mcpFixture, requests ...any) []map[string]any {
	t.Helper()
	var stdin bytes.Buffer
	for _, req := range requests {
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		stdin.Write(raw)
		stdin.WriteString("\n")
	}

	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetIn(&stdin)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"mcp", "--db-path", fix.dbPath})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("atlas mcp: %v (stderr %q)", err, stderr.String())
	}

	var out []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout carries a non-MCP line %q: %v", line, err)
		}
		out = append(out, msg)
	}
	return out
}

func TestMCP_Registered(t *testing.T) {
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "mcp" {
			return
		}
	}
	t.Fatal("atlas mcp is not registered on the root command")
}

func TestMCP_ServesAHandshakeAndAToolCallOverStdio(t *testing.T) {
	fix := newMCPFixture(t)
	fix.seed(t)

	responses := runMCPSession(t, fix,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": "2025-11-25"}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": "find_feature", "arguments": map[string]any{"query": "invoice"}}},
	)
	// The notification must not have produced a frame.
	if len(responses) != 3 {
		t.Fatalf("got %d response frames, want 3 (the notification must not be answered): %v", len(responses), responses)
	}

	init, _ := responses[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-11-25" {
		t.Errorf("protocolVersion = %v, want 2025-11-25", init["protocolVersion"])
	}
	list, _ := responses[1]["result"].(map[string]any)
	if tools, _ := list["tools"].([]any); len(tools) == 0 {
		t.Errorf("tools/list over the CLI returned nothing: %v", list)
	}

	call, _ := responses[2]["result"].(map[string]any)
	sc, ok := call["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call result carries no structuredContent: %v", responses[2])
	}
	feats, _ := sc["features"].([]any)
	if len(feats) != 1 {
		t.Fatalf("find_feature over the CLI = %v, want the seeded feature", sc)
	}
	if got, _ := feats[0].(map[string]any); got["id"] != "billing.invoice" {
		t.Errorf("feature id = %v, want billing.invoice", feats[0])
	}
}

// The working tree is what the agent will actually open. A file that never
// existed cannot be trusted for line numbers, and the answer has to say so
// rather than let the agent cite a span into nothing.
func TestMCP_ReportsIndexFreshnessAgainstTheWorkingTree(t *testing.T) {
	fix := newMCPFixture(t)
	fix.seed(t)

	responses := runMCPSession(t, fix,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": "symbol_info",
				"arguments": map[string]any{"qualified_name": "internal/billing.Invoice"}}},
	)
	res, _ := responses[1]["result"].(map[string]any)
	sc, _ := res["structuredContent"].(map[string]any)
	fresh, ok := sc["index_freshness"].(map[string]any)
	if !ok {
		t.Fatalf("symbol_info over the CLI carries no index_freshness: %v", sc)
	}
	bad, _ := fresh["untrustworthy_files"].([]any)
	if len(bad) != 1 {
		t.Fatalf("untrustworthy_files = %v, want the one file that is not in the working tree", fresh)
	}
}

// `atlas mcp` never serves under --json: stdout IS the protocol stream, and a
// JSON envelope on it is exactly the "anything that is not a valid MCP
// message" the spec forbids. The flag describes the server instead.
func TestMCP_JSONDescribesTheServerWithoutServing(t *testing.T) {
	fix := newMCPFixture(t)
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdin, stdout, stderr bytes.Buffer
	// A frame that WOULD be answered if the command served, so a regression
	// that serves under --json shows up as extra output.
	stdin.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	root.SetIn(&stdin)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"mcp", "--json", "--db-path", fix.dbPath})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("atlas mcp --json: %v (stderr %q)", err, stderr.String())
	}

	var env struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Result        struct {
			Transport        string   `json:"transport"`
			ProtocolVersions []string `json:"protocol_versions"`
			ReadOnly         bool     `json:"read_only"`
			Tools            []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("--json output is not a single envelope: %v (%q)", err, stdout.String())
	}
	if env.SchemaVersion != schemaVersion || env.Command != "mcp" {
		t.Errorf("envelope = %s/%s, want %s/mcp", env.SchemaVersion, env.Command, schemaVersion)
	}
	if len(env.Result.Tools) != 7 {
		t.Errorf("described %d tools, want 7", len(env.Result.Tools))
	}
	if !env.Result.ReadOnly {
		t.Error("read_only = false; this server must describe itself as read-only")
	}
	if env.Result.Transport != "stdio" {
		t.Errorf("transport = %q, want stdio", env.Result.Transport)
	}
	if len(env.Result.ProtocolVersions) == 0 {
		t.Error("protocol_versions is empty; a client cannot tell what this server speaks")
	}
}
