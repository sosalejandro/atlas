package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The CLI acceptance layer: the same fixture, driven through the real binary,
// asserting the --json envelope a consumer parses.
//
// pipeline_test.go proves the library computes the right numbers. That is not
// the same claim as "the command a user runs prints them": the envelope is a
// public contract (docs/architecture.md §6 — additive within a major), and a
// field that stops being populated breaks every consumer without breaking a
// single library test. This layer is the only place that difference is
// visible.

// atlasBin is the binary built once for the package by TestMain.
var atlasBin string

// TestMain builds the CLI once. Building per test would triple the package's
// runtime for no additional coverage — the binary is the same binary.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "atlas-acceptance-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "acceptance: temp dir: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	atlasBin = filepath.Join(dir, "atlas")
	build := exec.Command("go", "build", "-o", atlasBin, "../../cmd/atlas")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "acceptance: go build ./cmd/atlas: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// envelope mirrors the shared --json envelope. It is redeclared here rather
// than imported because internal/cli's type is unexported and, more to the
// point, this layer is standing in for an EXTERNAL consumer: a test that
// shared the producer's struct could not notice a field being renamed.
type envelope struct {
	SchemaVersion string          `json:"schema_version"`
	Command       string          `json:"command"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"`
	Warnings      []string        `json:"warnings"`
	GeneratedAt   string          `json:"generated_at"`
}

// runAtlas invokes the binary and decodes the envelope, failing the test with
// the command's own stderr when it exits non-zero — the exit code is part of
// the contract for the gate verbs, and a bare "exit status 1" is not a
// diagnosis anyone can act on.
func runAtlas(t *testing.T, args ...string) envelope {
	t.Helper()
	cmd := exec.Command(atlasBin, append(args, "--json")...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("atlas %s: %v\nstderr:\n%s", strings.Join(args, " "), err, stderr.String())
	}

	var env envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("atlas %s: output is not a JSON envelope: %v\ngot:\n%s",
			strings.Join(args, " "), err, stdout.String())
	}
	if env.SchemaVersion != "v1" {
		t.Errorf("atlas %s: schema_version = %q, want %q", strings.Join(args, " "), env.SchemaVersion, "v1")
	}
	if _, err := time.Parse(time.RFC3339, env.GeneratedAt); err != nil {
		t.Errorf("atlas %s: generated_at %q is not RFC3339: %v", strings.Join(args, " "), env.GeneratedAt, err)
	}
	return env
}

// TestAcceptance_CLI_TheFixtureEndToEnd runs the sequence a user runs — scan,
// cov sync, doctor, audit — against the fixture and asserts what each verb
// reports.
//
// One test rather than four because the verbs are not independent: cov sync
// has nothing to attribute until scan has run, and doctor has nothing to
// assess until both have. Splitting them would mean either four scans or a
// shared fixture database, and a shared mutable database between tests is how
// a suite starts depending on its own ordering.
func TestAcceptance_CLI_TheFixtureEndToEnd(t *testing.T) {
	db := filepath.Join(t.TempDir(), "atlas.db")

	// --- scan ------------------------------------------------------------
	scan := runAtlas(t, "scan", "--root", fixtureDir, "--db-path", db)
	if scan.Command != "scan" {
		t.Errorf("command = %q, want %q", scan.Command, "scan")
	}
	var scanRes struct {
		SymbolsInserted     int `json:"symbols_inserted"`
		EdgesInserted       int `json:"edges_inserted"`
		AnnotationsInserted int `json:"annotations_inserted"`
	}
	decodeResult(t, scan, &scanRes)
	// Measured against this fixture: seven declarations plus two test
	// functions plus shipping.New's package-qualified sibling — see
	// TestAcceptance_CollidingDeclarationsBothSurvive for why the count is
	// what it is rather than seven.
	if scanRes.SymbolsInserted != 10 {
		t.Errorf("symbols_inserted = %d, want 10", scanRes.SymbolsInserted)
	}
	// Five, not four, since issue #87 moved Go call resolution onto
	// go/packages. The fifth is shipping.TestOrderTotalLight ->
	// shipping.Order.Total: the test calls New(4).Total(), a method on the
	// RESULT of a call, and the AST resolver only ever walked selector
	// chains rooted at an identifier, so it saw nothing there to look up.
	// The type checker knows the receiver is *shipping.Order and binds it
	// to the package-qualified id the collision forced that method into.
	if scanRes.EdgesInserted != 5 {
		t.Errorf("edges_inserted = %d, want 5", scanRes.EdgesInserted)
	}
	if scanRes.AnnotationsInserted != 5 {
		t.Errorf("annotations_inserted = %d, want 5", scanRes.AnnotationsInserted)
	}
	// The collision has to be REPORTED, not just survived. A user whose two
	// contexts both declare Order.Total needs to know one of them is now
	// indexed under a package-qualified id, because that is the id their
	// annotations and traces have to use.
	if !containsSubstring(scan.Warnings, "symbol name collision: Order.Total") {
		t.Errorf("scan did not warn about the Order.Total collision; warnings: %v", scan.Warnings)
	}

	// --- cov sync --------------------------------------------------------
	sync := runAtlas(t, "cov", "sync", "--framework", "go-cover",
		"--input", filepath.Join(fixtureDir, profileName), "--db-path", db)
	if sync.Command != "cov.sync" {
		t.Errorf("command = %q, want %q", sync.Command, "cov.sync")
	}
	var syncRes struct {
		Attribution struct {
			FilesMatched      int `json:"files_matched"`
			FilesUnmatched    int `json:"files_unmatched"`
			StmtsAttributed   int `json:"stmts_attributed"`
			StmtsUnattributed int `json:"stmts_unattributed"`
			Gaps              []struct {
				Path   string `json:"path"`
				Stmts  int    `json:"stmts"`
				Reason string `json:"reason"`
			} `json:"gaps"`
		} `json:"attribution"`
	}
	decodeResult(t, sync, &syncRes)
	if syncRes.Attribution.StmtsAttributed != 11 || syncRes.Attribution.StmtsUnattributed != 3 {
		t.Errorf("attribution = %d attributed / %d unattributed, want 11/3",
			syncRes.Attribution.StmtsAttributed, syncRes.Attribution.StmtsUnattributed)
	}
	// The gap has to reach the user's terminal, not just the database. This
	// is the difference between a tool that reports its blind spot and one
	// that has one.
	if len(syncRes.Attribution.Gaps) != 1 || syncRes.Attribution.Gaps[0].Stmts != 3 {
		t.Errorf("gaps = %+v, want one gap of 3 statements", syncRes.Attribution.Gaps)
	}

	// --- doctor ----------------------------------------------------------
	doctor := runAtlas(t, "doctor", "--root", fixtureDir, "--db-path", db)
	var doctorRes struct {
		Checks []struct {
			Name     string `json:"name"`
			Severity string `json:"severity"`
			Finding  string `json:"finding"`
		} `json:"checks"`
		Worst string `json:"worst"`
	}
	decodeResult(t, doctor, &doctorRes)
	sev := map[string]string{}
	finding := map[string]string{}
	for _, c := range doctorRes.Checks {
		sev[c.Name] = c.Severity
		finding[c.Name] = c.Finding
	}
	// The index was just written from this tree, so freshness and linkage
	// must both be clean. If they are not, the fixture is lying to every
	// other assertion in this package.
	for _, name := range []string{"index.freshness", "feature.linkage", "store.schema"} {
		if sev[name] != "ok" {
			t.Errorf("doctor check %q = %q, want ok: %s", name, sev[name], finding[name])
		}
	}
	// And attribution must WARN, because 3 of 14 statements really are
	// unplaceable here. A doctor that reported ok would be the bug: the
	// whole value of the check is that it notices the blind spot the ingest
	// already told it about.
	if sev["coverage.attribution"] != "warn" {
		t.Errorf("doctor coverage.attribution = %q, want warn (the fixture has a real 3-statement blind spot): %s",
			sev["coverage.attribution"], finding["coverage.attribution"])
	}
	if doctorRes.Worst != "warn" {
		t.Errorf("doctor worst = %q, want warn", doctorRes.Worst)
	}

	// --- audit -----------------------------------------------------------
	audit := runAtlas(t, "audit", "--db-path", db)
	var auditRes struct {
		Features []struct {
			FeatureID string  `json:"feature_id"`
			Score     float64 `json:"score"`
		} `json:"features"`
	}
	decodeResult(t, audit, &auditRes)
	scores := map[string]float64{}
	for _, f := range auditRes.Features {
		scores[f.FeatureID] = f.Score
	}
	for _, want := range []string{"checkout.total", "checkout.pay", "shipping.quote"} {
		if _, ok := scores[want]; !ok {
			t.Errorf("audit did not score feature %q; scored: %v", want, scores)
		}
	}
	// checkout.total is fully covered and checkout.pay is not called at all,
	// so the ordering is a fact about the fixture rather than a threshold
	// anyone tuned. An audit that ranks them the other way round is scoring
	// something other than coverage.
	if scores["checkout.total"] <= scores["checkout.pay"] {
		t.Errorf("checkout.total scored %.1f and checkout.pay %.1f; the covered feature must outrank the uncovered one",
			scores["checkout.total"], scores["checkout.pay"])
	}
}

// TestAcceptance_CLI_ScanOmitsTheIngestFeatureCounts is a characterisation
// test for a DEFECT, not a specification of desired behaviour.
//
// `atlas scan --json` reports features_materialized, feature_symbols_linked
// AND orphan_annotations_skipped as 0 on every run, including runs where the
// ingest it describes produced all three. The cause is in
// internal/cli/scan.go: scanResult embeds initResult but is built field by
// field from store.IngestStats, and those three fields are never copied, so
// they keep their zero value. `atlas init` copies all three from the same
// stats struct, which is what makes the divergence provable rather than
// merely suspected.
//
// All THREE are named here on purpose. An earlier version of this test named
// only the first two, which meant a change that wired those up and left the
// orphan count behind would have gone green with the defect still in the
// envelope.
//
// It is pinned rather than fixed because internal/cli is outside this
// change's ownership (see the batch brief). Pinned rather than left silent
// because the envelope is a published contract: a CI gate written against
// features_materialized > 0 fails forever and looks like a repo problem.
//
// When someone copies the three fields across, this test fails. That failure
// is the fix landing, and the right response is to delete this test.
func TestAcceptance_CLI_ScanOmitsTheIngestFeatureCounts(t *testing.T) {
	// A purpose-built tree rather than shopfixture: the shared fixture holds
	// no orphan annotation, so orphan_annotations_skipped is 0 there for both
	// verbs and the third field could not be told apart from the other two.
	// The dangling annotation at the end of app.go is the orphan -- there is
	// no declaration after it for the ingest to attach it to.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/omitfx\n\ngo 1.25\n")
	writeFile(t, filepath.Join(root, "app", "app.go"), `package app

// Greet says hello.
//
// @atlas:feature greet.hello
func Greet() string {
	return "hi"
}

// @atlas:feature orphan.dangling
`)

	type counts struct {
		FeaturesMaterialized     int `json:"features_materialized"`
		FeatureSymbolsLinked     int `json:"feature_symbols_linked"`
		OrphanAnnotationsSkipped int `json:"orphan_annotations_skipped"`
	}

	var fromInit, fromScan counts
	decodeResult(t, runAtlas(t, "init", "--root", root,
		"--db-path", filepath.Join(t.TempDir(), "init.db")), &fromInit)
	decodeResult(t, runAtlas(t, "scan", "--root", root,
		"--db-path", filepath.Join(t.TempDir(), "scan.db")), &fromScan)

	// The fixture has to exercise all three counters, or this test would pass
	// by measuring nothing.
	if fromInit.FeaturesMaterialized == 0 || fromInit.FeatureSymbolsLinked == 0 ||
		fromInit.OrphanAnnotationsSkipped == 0 {
		t.Fatalf("the fixture no longer produces all three counts (init reported %+v); "+
			"this test can say nothing about the reporting defect", fromInit)
	}

	for _, f := range []struct {
		name string
		got  int
	}{
		{"features_materialized", fromScan.FeaturesMaterialized},
		{"feature_symbols_linked", fromScan.FeatureSymbolsLinked},
		{"orphan_annotations_skipped", fromScan.OrphanAnnotationsSkipped},
	} {
		if f.got != 0 {
			t.Errorf("scan now reports %s=%d over a tree where init reports %+v. "+
				"The defect in internal/cli/scan.go looks fixed - delete this test.",
				f.name, f.got, fromInit)
		}
	}
}

// writeFile writes content at path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// decodeResult unmarshals the envelope's result into v.
func decodeResult(t *testing.T, env envelope, v any) {
	t.Helper()
	if len(env.Result) == 0 {
		t.Fatalf("envelope for %q carries no result", env.Command)
	}
	if err := json.Unmarshal(env.Result, v); err != nil {
		t.Fatalf("decode result of %q: %v\ngot:\n%s", env.Command, err, env.Result)
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
