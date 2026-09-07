package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	"github.com/sosalejandro/atlas/packages/redact"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// IngestStats records what Ingest wrote. Useful for the `atlas scan`
// command's terminal summary line ("symbols: 1342  edges: 4571 ...") and
// for tests that need to assert on side-effect shape.
type IngestStats struct {
	SymbolsInserted                  int           `json:"symbols_inserted"`
	EdgesInserted                    int           `json:"edges_inserted"`
	AnnotationsInserted              int           `json:"annotations_inserted"`
	FileHashesUpserted               int           `json:"file_hashes_upserted"`
	PatternMatchesSet                int           `json:"pattern_matches_set"`
	FeaturesMaterialized             int           `json:"features_materialized"`
	FeatureSymbolsLinked             int           `json:"feature_symbols_linked"`
	OrphanAnnotationsSkipped         int           `json:"orphan_annotations_skipped"`
	TestAnnotationsWithoutImplSymbol int           `json:"test_annotations_without_impl_symbol"`
	SymbolsPruned                    int           `json:"symbols_pruned"`
	FilesScanned                     int           `json:"files_scanned"`
	FilesSkipped                     int           `json:"files_skipped"`
	SkippedFilesRecorded             int           `json:"skipped_files_recorded"`
	Duration                         time.Duration `json:"duration"`

	// Redactions names every credential this ingest replaced with a
	// placeholder before writing it. Empty on a clean scan.
	//
	// It is a list rather than a count because a count answers the wrong
	// question: "atlas changed 3 of your values" is not actionable, and the
	// operator needs to know WHICH file the credential is still sitting in.
	Redactions []Redaction `json:"redactions,omitempty"`
}

// Redaction records one credential a write path replaced on the way into
// the store, described without reproducing it.
//
// Everything here is safe to print and to put in a JSON envelope: the rule
// that fired and a digest, never the secret. It is the same evidence
// `atlas security` reports for a credential already stored, which is
// deliberate -- an operator should not have to learn two vocabularies for
// "there is a credential in your source".
type Redaction struct {
	Table  string `json:"table"`
	Column string `json:"column"`

	// Where locates the row in the operator's terms -- a repo-relative
	// "file:line", or an operation ref -- not by surrogate id. The point of
	// reporting a redaction at all is that the credential is STILL in the
	// source file, and the operator has to go and rotate it.
	Where string `json:"where"`

	// Kind is the redact rule that fired, and Digest the first 12 hex
	// characters of SHA-256 over what was removed. The same credential in
	// nine places carries the same digest, so it reads as one leak.
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
}

// redactForStore runs a value bound for a registered TEXT column through
// packages/redact, returning the text to store and what was replaced.
//
// This is where secret detection stops being a report about a store that
// already leaked and becomes a property of the write. packages/redact owns
// the decision -- which columns may be rewritten lives in its registry, not
// here -- so this function is only the wiring and the accounting.
//
// A non-redactable column comes back untouched with no findings; see
// redact.Field for why that is the right asymmetry rather than a gap.
func redactForStore(
	ctx context.Context, logger shared.Logger, table, column, where, value string,
) (string, []Redaction) {
	res := redact.Field(table, column, value)
	if !res.Redacted() {
		return value, nil
	}
	out := make([]Redaction, 0, len(res.Findings))
	for _, f := range res.Findings {
		out = append(out, Redaction{
			Table: table, Column: column, Where: where,
			Kind: string(f.Kind), Digest: f.Digest,
		})
		logger.Warn(ctx,
			"store: replaced a credential with a placeholder before storing it",
			"table", table, "column", column, "where", where,
			"rule", string(f.Kind), "digest", f.Digest)
	}
	return res.Text, out
}

// IngestOptions carries the scan-time facts the index itself does not
// record. Everything here is optional: an ingest without it writes the same
// rows, just with less to say about them.
type IngestOptions struct {
	// GeneratedGlobs is `scan.generated` exactly as the scan that produced
	// the index ran with, in configured order.
	//
	// It exists so the exclusion ledger can name WHICH glob claimed a file.
	// codeindex.Index reports the rule ("generated-glob") but not the
	// pattern, and the pattern is the actionable half — it is the line of
	// `.atlas.yaml` an operator edits when a hand-written file disappeared
	// from the index. Callers that leave it empty still get the rule; they
	// just get no pattern with it.
	GeneratedGlobs []string

	// rowAtATime forces the pre-batching symbol and edge writers: one
	// prepared statement per row, exactly as this package wrote them
	// before issue #109.
	//
	// It is unexported because no caller outside this package has any
	// business choosing, and it exists for one reason: the batched writer's
	// central risk is that it hands out DIFFERENT surrogate ids than the
	// loop it replaced, and the only honest way to test that is to run both
	// against the same index and diff the tables. A golden list of expected
	// ids would prove the batched path self-consistent and nothing more.
	//
	// See TestIngestBatch_AssignsTheSameSurrogateIDs. The row-at-a-time
	// writers are kept for it, not as a fallback — the batched path is the
	// one that runs.
	rowAtATime bool
}

// Ingest writes an entire codeindex.Index into the store as one transaction.
//
// Idempotency contract:
//
//   - symbols.qualified_name is UNIQUE; INSERT OR IGNORE.
//   - edges has a composite UNIQUE on (from, to, kind, file, line); INSERT
//     OR IGNORE. The in-memory Graph does not carry per-edge file/line, so
//     Ingest uses the From symbol's position — predictable + dedupable.
//   - annotations has a UNIQUE on (file_path, line, kind); INSERT ... ON
//     CONFLICT DO UPDATE refreshes value + parsed_at.
//   - file_hashes is upserted on file_path.
//   - skipped_files is REPLACED wholesale with idx.SkippedFiles. It is the
//     one table here that is not additive, because it records the current
//     exclusion set rather than an accumulating history: a file that no
//     longer matches any rule has to leave it.
//
// Re-Ingesting the same Index produces zero net row changes for symbols
// and edges; annotation rows get refreshed parsed_at; file_hashes get
// refreshed last_scanned.
//
// File-hash optimization: if a file_hashes row already exists with a
// matching content_hash, the symbols/edges for that file are NOT touched
// (Phase 1's codeindex doesn't carry per-symbol provenance fine enough
// for partial re-ingest, so the conservative choice is to skip the file
// entirely). Files that are not yet in file_hashes are always processed.
//
// Statement shape (issue #109): every ADDITIVE write here goes out as a
// multi-row VALUES statement chunked against a conservative bound-parameter
// ceiling — symbols, edges, annotations, file hashes — and the two per-row
// reads it used to do (the unchanged-file check and the symbol id lookup)
// are chunked `IN` queries. A profile found 46.6% of the ingest was SQLite
// re-parsing the same INSERT text once per row. The sqlc-generated Queries
// still carry everything that is genuinely per-row: the position UPDATE,
// the pattern-match UPDATE, and feature materialisation.
//
// The batched writes preserve the loop's row order exactly, because
// surrogate ids are handed out in insertion order and a reordered batch
// would renumber the graph silently. See writeSymbolsBatched.
//
// opts is variadic so the existing two-argument call sites keep compiling —
// they lose nothing but the glob names on the exclusion ledger. Pass at most
// one; anything past the first is a caller bug and is ignored.
func (s *Store) Ingest(ctx context.Context, idx *codeindex.Index, opts ...IngestOptions) (*IngestStats, error) {
	if idx == nil {
		return nil, fmt.Errorf("store ingest: nil index")
	}
	var opt IngestOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	start := time.Now()
	stats := &IngestStats{}

	// 1. Compute the set of files whose hash hasn't changed since the last
	// scan — we'll skip writing symbols/edges for those.
	//
	// One chunked read rather than a Get per file: this ran before the tx
	// opened and cost 661 prepared statements on this repository before it
	// had written a single row.
	scannedPaths := slices.Sorted(maps.Keys(idx.FileHashes))
	stats.FilesScanned = len(scannedPaths)
	unchanged, err := storedContentHashesMatch(ctx, s.conn, scannedPaths, idx.FileHashes)
	if err != nil {
		return nil, err
	}
	stats.FilesSkipped = len(unchanged)

	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store ingest: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	// 2. Upsert symbols (skip those declared in unchanged files).
	//
	// freshByFile records what this scan saw per file so step 2b can prune
	// the rows it no longer produces; rescanned is the set of files this scan
	// actually re-read (walked and changed), which bounds that pruning.
	freshByFile := map[string]map[string]bool{}
	rescanned := map[string]bool{}
	for path := range idx.FileHashes {
		if !unchanged[path] {
			rescanned[path] = true
		}
	}
	// A scan run without --hash-files carries no FileHashes; fall back to the
	// files the index produced symbols for, so pruning still works there.
	for _, sym := range idx.Symbols {
		if p := sym.Position.Path; p != "" && !unchanged[p] {
			rescanned[p] = true
		}
	}
	// The loop below SORTS the index; it writes nothing. That separation is
	// what makes batching safe: whichever writer runs, it receives `writes`
	// in index order and emits its VALUES tuples in that same order, so the
	// surrogate ids come out in the same sequence either way. See the head
	// comment on ingest_batch_test.go for why that is the one property here
	// worth a dedicated test.
	//
	// Symbols with no file path are dropped rather than written and filtered
	// later: the schema's file_path is NOT NULL, and recording their qn → 0
	// mapping would have the edge pass insert an edge with to_symbol_id = 0
	// → FOREIGN KEY violation. They are the synthetic route and endpoint
	// vertices the graph walk invents and never gives a source location.
	symbolIDByQualifiedName := make(map[shared.SymbolID]int64, len(idx.Symbols))
	carried := make([]string, 0, len(idx.Symbols)) // in unchanged files: id lookup only
	writes := make([]symbolWrite, 0, len(idx.Symbols))
	for _, sym := range idx.Symbols {
		if sym.ID == "" {
			continue
		}
		path := sym.Position.Path
		if unchanged[path] {
			// Even when skipping inserts, we still need the surrogate id
			// for edge writes — look it up.
			carried = append(carried, string(sym.ID))
			continue
		}
		if path != "" {
			if freshByFile[path] == nil {
				freshByFile[path] = map[string]bool{}
			}
			freshByFile[path][string(sym.ID)] = true
		}
		w, ok := prepareSymbolWrite(ctx, s.logger, sym)
		if !ok {
			continue
		}
		writes = append(writes, w)
	}
	// The carried ids, in one chunked read instead of a SELECT per symbol.
	// Lookups consume no rowids, so hoisting them all in front of the
	// inserts cannot move an id.
	if err := loadSymbolIDs(ctx, tx, carried, symbolIDByQualifiedName); err != nil {
		return nil, err
	}
	insertedSymbols, err := writeSymbols(ctx, tx, qtx, writes, symbolIDByQualifiedName, opt.rowAtATime)
	if err != nil {
		return nil, err
	}
	stats.SymbolsInserted = insertedSymbols

	// 2b. Prune the symbols a rescanned file no longer declares (renamed,
	// deleted, or moved elsewhere). See pruneStaleSymbolsTx.
	pruned, err := pruneStaleSymbolsTx(ctx, qtx, freshByFile, rescanned)
	if err != nil {
		return nil, err
	}
	stats.SymbolsPruned = pruned

	// 3. Upsert edges. Skip edges where either endpoint lives in an unchanged
	// file — the existing rows are already authoritative. Also skip edges
	// whose endpoints have a zero surrogate id (position-less symbols that
	// got filtered above) — writing to_symbol_id = 0 is a FOREIGN KEY error.
	if idx.Graph != nil {
		edgeWrites := make([]edgeWrite, 0, len(idx.Graph.Edges))
		for _, e := range idx.Graph.Edges {
			fromID, ok := symbolIDByQualifiedName[e.From]
			if !ok || fromID == 0 {
				continue
			}
			toID, ok := symbolIDByQualifiedName[e.To]
			if !ok || toID == 0 {
				continue
			}
			fromNode, hasFrom := idx.Graph.Nodes[e.From]
			if !hasFrom {
				continue
			}
			path := fromNode.Position.Path
			// Prefer the per-edge line emitted by the sub-scanner (e.g.
			// scanner.py records the actual import / call-site line). When
			// the sub-scanner did not supply one (Line == 0, the wire's
			// zero-value), fall back to the from-symbol's declaration
			// line — which preserves pre-fix behaviour for the TS + Go
			// scanners that don't yet populate per-edge lines.
			//
			// This fix addresses the bug where every Python import edge
			// reported line=1 because the FROM symbol of an import is the
			// module (declared at line 1) regardless of where the import
			// statement actually appears.
			line := e.Line
			if line <= 0 {
				line = fromNode.Position.Line
			}
			if unchanged[path] {
				continue
			}
			if path == "" {
				// Synthetic nodes (route:, endpoint:) carry no file
				// position — skip rather than violate NOT NULL.
				continue
			}
			if line <= 0 {
				line = 1
			}
			kind := NormalizeEdgeKind(e.Kind)
			// Meta carries an opaque kind-specific qualifier — today
			// only Python `import` edges populate it with a scope tag
			// (issue #16). Normalisation drops unknown values to ""
			// (NULL) so a future scanner that emits a Meta value we
			// don't recognise here can't pollute the column.
			meta := NormalizeEdgeMeta(kind, e.Meta)
			// Tier and Ambiguous ride through untouched (issue #146).
			// Nothing between the scanner and this line may infer,
			// upgrade or supply a tier: the scanner is the only layer
			// that knows which mechanism ran, and a tier invented here
			// would be a claim about work nobody did. An edge that
			// arrives without one fails the ingest by name rather than
			// landing as a plausible-looking row.
			if err := requireTier(fromID, toID, e.Tier); err != nil {
				return nil, fmt.Errorf("ingest %s:%d: %w", path, line, err)
			}
			edgeWrites = append(edgeWrites, edgeWrite{
				fromID: fromID, toID: toID, kind: string(kind),
				path: path, line: int64(line), meta: metaParam(meta),
				tier: string(e.Tier), ambiguous: boolToInt(e.Ambiguous),
			})
		}
		insertedEdges, err := writeEdges(ctx, tx, qtx, edgeWrites, opt.rowAtATime)
		if err != nil {
			return nil, err
		}
		stats.EdgesInserted = insertedEdges
	}

	// 4. Upsert raw annotations (skip those whose file is unchanged — same
	// content means same line numbers means same rows already exist).
	//
	// Two annotations can legitimately land on the same (file, line, kind)
	// — the same comment carrying two directives, or a fixture repeating
	// one. Inside a single multi-row statement SQLite resolves that the way
	// a sequence of single-row upserts would: the later tuple wins. That is
	// asserted rather than assumed, in TestIngestBatch_AnnotationLastValueWins.
	annWrites := make([]annotationWrite, 0, len(idx.Annotations))
	for _, ann := range idx.Annotations {
		path := ann.Position.Path
		if unchanged[path] {
			continue
		}
		if !schemaAnnotationKinds[ann.Kind] {
			continue
		}
		src := ann.Source
		if !schemaAnnotationSources[src] {
			src = shared.SourceAtlas
		}
		value := ann.Raw
		if value == "" && len(ann.IDs) > 0 {
			value = strings.Join(ann.IDs, " ")
		}
		value, reds := redactForStore(ctx, s.logger, "annotations", "value",
			fmt.Sprintf("%s:%d", path, ann.Position.Line), value)
		stats.Redactions = append(stats.Redactions, reds...)
		annWrites = append(annWrites, annotationWrite{
			path: path, line: int64(ann.Position.Line),
			kind: string(ann.Kind), value: value, source: string(src),
		})
	}
	if err := upsertAnnotations(ctx, tx, annWrites); err != nil {
		return nil, err
	}
	stats.AnnotationsInserted = len(annWrites)

	// 4.5. Persist per-symbol pattern matches from codeindex/patterns.
	// Matches live alongside the symbol row in the pattern_matches JSON
	// column (Phase 6f). Skipped when the symbol lives in an unchanged file
	// — the persisted JSON is already current.
	for sym, matches := range idx.PatternMatches {
		id, ok := symbolIDByQualifiedName[sym]
		if !ok {
			// The recogniser surfaced a hit for a symbol the Go scanner
			// didn't emit (rare — would happen for package-scope calls
			// using the synthetic "file:Lnnn" handle). Skip rather than
			// fabricate a synthetic symbol row here.
			continue
		}
		// Find the symbol's file via its node so we can honour the
		// unchanged-file skip.
		var symFile string
		if idx.Graph != nil {
			if node, hasNode := idx.Graph.Nodes[sym]; hasNode {
				symFile = node.Position.Path
			}
		}
		if symFile != "" && unchanged[symFile] {
			continue
		}
		if len(matches) == 0 {
			continue
		}
		b, jerr := json.Marshal(matches)
		if jerr != nil {
			return nil, fmt.Errorf("store ingest patterns marshal %q: %w", sym, jerr)
		}
		val, reds := redactForStore(ctx, s.logger, "symbols", "pattern_matches",
			string(sym), string(b))
		stats.Redactions = append(stats.Redactions, reds...)
		if err := qtx.SetSymbolPatternMatches(ctx, sqlc.SetSymbolPatternMatchesParams{
			PatternMatches: &val,
			ID:             id,
		}); err != nil {
			return nil, fmt.Errorf("store ingest patterns %q: %w", sym, err)
		}
		stats.PatternMatchesSet++
	}

	// 4.6. Materialize features from feature/contract annotations.
	//
	// Annotations are the SINGLE source of truth for feature membership in
	// Atlas v1 — the legacy testreg YAML registries are reference-only
	// post-Phase-9. Each `@atlas:feature <id>` or `@testreg <id>` annotation
	// upserts an `features` row (id-as-title default; pre-seeded titles are
	// preserved by INSERT OR IGNORE) and links it to the symbol whose
	// declaration follows the annotation in the same file.
	//
	// Multi-id annotations like `// @testreg meals.log-create meals.history #mocked`
	// produce one feature row per id and one feature_symbols link row per
	// (id, symbol) pair, all attached to the same containing symbol.
	//
	// Orphan annotations (no symbol within the LookupAtPosition window —
	// typical for .md files, package-doc comments, end-of-file markers)
	// are skipped silently — the annotation row still exists, but no
	// feature row is created. This is intentional: annotations on non-code
	// files are legitimate but cannot be materialized without a symbol to
	// anchor on, and we'd rather have no link than a phantom one.
	for _, ann := range idx.Annotations {
		if ann.Kind != shared.AnnFeature && ann.Kind != shared.AnnContract {
			continue
		}
		// Skip annotations whose file was deemed unchanged AND whose
		// annotation row was therefore not refreshed — on a re-ingest those
		// features/links already exist from the prior pass.
		//
		// Note: we cannot short-circuit the WHOLE annotation here because a
		// brand-new feature annotation could appear in an OTHERWISE unchanged
		// file (e.g. someone edits a comment-only block; mtime changes but
		// our hash check might still mark it unchanged in edge cases). The
		// belt-and-braces story is "let the loop run; INSERT OR IGNORE makes
		// re-inserts free". So we DO NOT skip on unchanged here.
		ids := extractFeatureIDsFromAnnotation(ann)
		if len(ids) == 0 {
			continue
		}

		// Resolve the symbol this annotation attaches to. Must go through
		// the in-flight tx (qtx) — the symbol may have been inserted in
		// step 2 of *this* tx and is not yet visible on the bare *sql.DB.
		//
		// Annotations on non-code files (or file positions with no
		// declaration in the next 30 lines) are orphans — we skip them
		// silently. No feature row, no link row. The annotation row stays.
		//
		// Special case for FE/mobile test files (apps/**/__tests__/*.test.tsx,
		// co-located *.test.ts, etc.): the TS scanner never emits symbols for
		// test files (DEFAULT_SKIP_DIRS excludes them and there are no
		// test-function AST patterns). When LookupSymbolAtOrAfterLine returns
		// no rows for a recognised test-file path, we fall back to the impl
		// file (e.g. "LoginPage.tsx" for "LoginPage.test.tsx") and link with
		// role=test so audit's coverageSignal can credit the feature without
		// inflating the impl-symbol denominator (wantedSymbolIDs skips test
		// role). If the impl file also has no symbol, we record the miss in
		// TestAnnotationsWithoutImplSymbol (not OrphanAnnotationsSkipped) so
		// callers can distinguish the two failure modes.
		symRow, err := qtx.LookupSymbolAtOrAfterLine(ctx, sqlc.LookupSymbolAtOrAfterLineParams{
			FilePath:     ann.Position.Path,
			Line:         int64(ann.Position.Line),
			MaxLookahead: defaultPositionLookahead,
		})
		role := RoleImpl
		if errors.Is(err, sql.ErrNoRows) {
			// No symbol in the annotation's own file. Check if this is a
			// test file and attempt impl-file fallback.
			if isTestFilePath(ann.Position.Path) {
				implPath := implFileForTestFile(ann.Position.Path)
				if implPath != "" {
					// Try to find ANY symbol in the impl file (line 1, large
					// lookahead covers the whole file in practice).
					implRow, implErr := qtx.LookupSymbolAtOrAfterLine(ctx, sqlc.LookupSymbolAtOrAfterLineParams{
						FilePath:     implPath,
						Line:         1,
						MaxLookahead: 1000000,
					})
					if implErr == nil {
						symRow = implRow
						role = RoleTest
						err = nil
					}
				}
			}
			if err != nil {
				// Still no symbol found after optional impl-file fallback.
				if isTestFilePath(ann.Position.Path) {
					stats.TestAnnotationsWithoutImplSymbol++
				} else {
					stats.OrphanAnnotationsSkipped++
				}
				continue
			}
		}
		if err != nil {
			return nil, fmt.Errorf("store ingest feature-materialize lookup %q L%d: %w",
				ann.Position.Path, ann.Position.Line, err)
		}
		symID := symRow.ID

		featureKind := FeatureKindFeature
		if ann.Kind == shared.AnnContract {
			featureKind = FeatureKindContract
			role = RoleContract
		}

		for _, fid := range ids {
			if err := qtx.EnsureFeature(ctx, sqlc.EnsureFeatureParams{
				ID:    fid,
				Title: fid,
				Kind:  string(featureKind),
			}); err != nil {
				return nil, fmt.Errorf("store ingest feature-materialize upsert %q: %w", fid, err)
			}
			stats.FeaturesMaterialized++

			if err := qtx.LinkFeatureSymbol(ctx, sqlc.LinkFeatureSymbolParams{
				FeatureID: fid,
				SymbolID:  symID,
				Role:      string(role),
				Source:    string(SourceAnnotation),
			}); err != nil {
				return nil, fmt.Errorf("store ingest feature-materialize link %q→%d: %w", fid, symID, err)
			}
			stats.FeatureSymbolsLinked++
		}
	}

	// 5. Upsert file_hashes (always — even unchanged files get last_scanned
	// refreshed so the cache TTL stays warm).
	//
	// In sorted path order, which is a behaviour change worth naming: this
	// used to range over a Go map, so on a first scan a brand-new file's
	// rowid depended on the runtime's randomised iteration order and two
	// ingests of one index produced two different `file_hashes` tables.
	// Nothing reads that rowid today, but "reproducible" should not have an
	// asterisk on it. See TestIngestBatch_FileHashesAreWrittenInPathOrder.
	hashWrites := make([]fileHashWrite, 0, len(scannedPaths))
	for _, path := range scannedPaths {
		fh := idx.FileHashes[path]
		hashWrites = append(hashWrites, fileHashWrite{
			path: path, hash: fh.SHA256, mtime: fh.ModTime, lastScanned: fh.LastScanned,
		})
	}
	if err := upsertFileHashes(ctx, tx, hashWrites); err != nil {
		return nil, err
	}
	stats.FileHashesUpserted = len(hashWrites)

	// 6. Replace the exclusion ledger with what THIS scan declined to index.
	//
	// Replaced, not merged: a file that stopped matching a rule has to
	// vanish from it. Inside the same transaction as everything above, so
	// the ledger and the index it explains commit together — a ledger that
	// survived a failed ingest would describe a scan that never happened.
	ledger := skippedLedgerRows(idx.SkippedFiles, opt.GeneratedGlobs, start.UTC())
	for i := range ledger {
		var reds []Redaction
		ledger[i].Detail, reds = redactForStore(ctx, s.logger, "skipped_files", "detail",
			ledger[i].FilePath, ledger[i].Detail)
		stats.Redactions = append(stats.Redactions, reds...)
	}
	recorded, err := replaceSkippedLedgerTx(ctx, qtx, ledger)
	if err != nil {
		return nil, fmt.Errorf("store ingest skipped ledger: %w", err)
	}
	stats.SkippedFilesRecorded = recorded

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store ingest: commit: %w", err)
	}

	stats.Duration = time.Since(start)
	return stats, nil
}

// updateSymbolPositionSQL refreshes a known symbol's location in place. Kept
// as raw SQL rather than a sqlc query because sqlc's sqlite grammar (v1.31.x)
// garbles a multi-column UPDATE ... WHERE — the same class of bug already
// documented for FindByPattern in symbols.go.
//
// node_class is refreshed alongside the position, not held fixed at whatever
// the first scan decided. A node can legitimately change class: a Python
// import that resolved to nothing lands as an `external:py` anchor, and the
// day the module it names is added to the repo the same id becomes a real
// declaration. Freezing the class here would leave that declaration
// invisible to every "real code only" query until someone deleted the store.
const updateSymbolPositionSQL = `UPDATE symbols
SET kind = ?, file_path = ?, line = ?, end_line = ?, package = ?, domain = ?, node_class = ?
WHERE qualified_name = ?`

// =====================================================================
// Batching primitives (issue #109)
// =====================================================================
//
// A CPU profile of BenchmarkIngest_RescanChanged said 46.6% of the ingest
// was `sqlite3_prepare_v2` — SQLite re-parsing the same INSERT text
// thousands of times, once per row, because database/sql compiles a fresh
// statement per Exec. The writing was never the expensive part. Asking for
// it was. Everything below exists to ask once per batch instead.

// maxBoundParams caps how many `?` one statement may carry.
//
// 999 is SQLITE_MAX_VARIABLE_NUMBER's historical default and the ceiling
// every SQLite build in existence honours. The limit that ACTUALLY applies
// here is whatever modernc.org/sqlite's vendored amalgamation was compiled
// with — measured at 32,766 on the version in go.mod today — but that is a
// property of a dependency, set at its build time, and nothing in this
// repository pins or asserts it. A dependency bump that lowered it would
// turn a silent 999 assumption into "too many SQL variables" on a large
// repository and nowhere else, which is the worst possible place to find
// out. So: chunk against the number that cannot change.
//
// The cost of being conservative is arithmetic. 999 versus 32,766 is the
// difference between 43 statements and 2 for this repository's symbol
// table; both are a rounding error against the 5,288 the row-at-a-time
// loop compiled.
const maxBoundParams = 999

// rowsPerChunk is how many rows of a `columns`-wide table fit in one
// statement.
//
// It never returns zero. It is an integer division, its result is a loop
// stride, and a stride of zero does not terminate — so the floor is
// explicit here rather than left to every caller to remember. Today no
// table in this schema is wide enough to reach it; the guard is for the
// one that eventually is.
func rowsPerChunk(columns int) int {
	if columns < 1 {
		return maxBoundParams
	}
	if n := maxBoundParams / columns; n > 0 {
		return n
	}
	return 1
}

// inChunks hands rows to fn in contiguous slices of at most perChunk, in
// order. Order is the whole point: see writeSymbolsBatched.
func inChunks[T any](rows []T, perChunk int, fn func([]T) error) error {
	if perChunk < 1 {
		// Belt and braces with rowsPerChunk's floor. A zero stride would
		// spin forever on a non-empty slice, and a hang is a far worse
		// failure than a slow ingest.
		perChunk = 1
	}
	for start := 0; start < len(rows); start += perChunk {
		if err := fn(rows[start:min(start+perChunk, len(rows))]); err != nil {
			return err
		}
	}
	return nil
}

// valuesTuples renders `(?,?,?),(?,?,?)` for a multi-row VALUES clause.
func valuesTuples(rows, columns int) string {
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", columns), ",") + ")"
	return strings.TrimSuffix(strings.Repeat(one+",", rows), ",")
}

// =====================================================================
// Symbols
// =====================================================================

const symbolInsertColumns = 8

const insertSymbolsSQL = `INSERT OR IGNORE INTO symbols
  (qualified_name, kind, file_path, line, end_line, package, domain, node_class)
VALUES `

// symbolWrite is one symbol already normalised into the exact eight column
// values the table takes — kind collapsed, position defaulted, domain
// derived, node_class classified.
//
// It exists so the two writers below differ ONLY in how they talk to
// SQLite. If each re-derived its own parameters, a difference between them
// could be a batching bug or a normalisation bug, and the differential test
// could not tell you which.
type symbolWrite struct {
	qn        string
	kind      string
	path      string
	line      int64
	endLine   *int64
	pkg       *string
	domain    *string
	nodeClass string
}

// insertArgs returns the bound parameters in insertSymbolsSQL's column
// order. The order is load-bearing twice over: once per tuple, and once
// across tuples, because rowids are assigned in the order the tuples appear.
func (w symbolWrite) insertArgs() []any {
	return []any{w.qn, w.kind, w.path, w.line, w.endLine, w.pkg, w.domain, w.nodeClass}
}

func (w symbolWrite) insertParams() sqlc.InsertSymbolParams {
	return sqlc.InsertSymbolParams{
		QualifiedName: w.qn,
		Kind:          w.kind,
		FilePath:      w.path,
		Line:          w.line,
		EndLine:       w.endLine,
		Package:       w.pkg,
		Domain:        w.domain,
		NodeClass:     &w.nodeClass,
	}
}

// prepareSymbolWrite turns a scanner's shared.Symbol into the row the store
// will hold, or reports false for a symbol that must not become a row.
//
// The logger carries the parser-drift Warn record emitted by
// normalizeKindForWrite when an unknown kind has to be collapsed to
// KindFunc. Pass shared.NopLogger{} where the warning side channel does not
// matter.
func prepareSymbolWrite(ctx context.Context, logger shared.Logger, sym shared.Symbol) (symbolWrite, bool) {
	path := sym.Position.Path
	if path == "" {
		// Skip position-less anchors — the schema's file_path is NOT NULL.
		// These are the `route:` / `endpoint:` vertices the graph walk
		// invents and never gives a source location; an anchor that DOES
		// have one (a named sqlc query points at its .sql file, a pyscan
		// stub at the reserved external-import path) falls through and is
		// stored with node_class = 'anchor'.
		return symbolWrite{}, false
	}
	line := sym.Position.Line
	if line <= 0 {
		line = 1
	}
	w := symbolWrite{
		qn:   string(sym.ID),
		kind: string(normalizeKindForWrite(ctx, logger, "ingest.symbolWrite", sym.ID, sym.Kind)),
		path: path,
		line: int64(line),
		// Classified here rather than defaulted in the schema: the ingest
		// is the last place that still knows both the id and the path the
		// scanner produced, and shared.ClassifyNode is the single copy of
		// the rule migration 0019 backfilled existing rows with.
		nodeClass: string(shared.ClassifyNode(sym.ID, path)),
	}
	// end_line is the symbol's closing line. It is what the coverage
	// ingest uses to decide which executed statements belong to this
	// symbol; when it is NULL the span has to be guessed from the next
	// symbol's start line, which mis-attributes every statement in between
	// (issue #85). Scanners that cannot supply it leave it zero → NULL.
	if sym.EndLine >= line {
		v := int64(sym.EndLine)
		w.endLine = &v
	}
	if sym.Package != "" {
		v := sym.Package
		w.pkg = &v
	}
	if d := domainFor(path); d != "" {
		v := d
		w.domain = &v
	}
	return w, true
}

// writeSymbols writes every symbol this scan produced and records each
// row's surrogate id in ids, returning how many rows are new.
//
// rowAtATime selects the pre-#109 writer. It is not a fallback and not a
// tuning knob — nothing outside this package can reach it — it is the
// reference implementation the batched path is diffed against. See
// IngestOptions.rowAtATime.
func writeSymbols(
	ctx context.Context, tx *sql.Tx, qtx *sqlc.Queries,
	writes []symbolWrite, ids map[shared.SymbolID]int64, rowAtATime bool,
) (int, error) {
	if rowAtATime {
		return writeSymbolsRowAtATime(ctx, tx, qtx, writes, ids)
	}
	return writeSymbolsBatched(ctx, tx, writes, ids)
}

// writeSymbolsBatched is the shipping path: one chunked read, one batched
// INSERT, one chunked read back, and an UPDATE only for rows that moved.
//
// THE ORDER OF `fresh` IS THE CONTRACT. symbols.id is an AUTOINCREMENT
// rowid handed out in insertion order, edges and coverage rows reference
// it, and a batch that emitted its tuples in any other order would renumber
// the graph without violating a single constraint. So the split below walks
// `writes` once, in index order, and appends — it never sorts, never groups
// by file, and never dedupes into a map whose iteration order is random.
func writeSymbolsBatched(
	ctx context.Context, tx *sql.Tx, writes []symbolWrite, ids map[shared.SymbolID]int64,
) (int, error) {
	names := make([]string, len(writes))
	for i, w := range writes {
		names[i] = w.qn
	}
	stored, err := loadStoredSymbols(ctx, tx, names)
	if err != nil {
		return 0, err
	}

	fresh, known := splitSymbolWrites(writes, stored)

	inserted, err := insertSymbolChunks(ctx, tx, fresh)
	if err != nil {
		return 0, err
	}
	for qn, row := range stored {
		ids[shared.SymbolID(qn)] = row.id
	}
	freshNames := make([]string, len(fresh))
	for i, w := range fresh {
		freshNames[i] = w.qn
	}
	if err := loadSymbolIDs(ctx, tx, freshNames, ids); err != nil {
		return 0, err
	}
	// A batched INSERT reports one RowsAffected for the whole chunk, so
	// "did every tuple land" has to be asked separately. It is asked
	// because the alternative failure is silent: a missing id drops every
	// edge touching that symbol, and the ingest still commits.
	for _, w := range fresh {
		if _, ok := ids[shared.SymbolID(w.qn)]; !ok {
			return 0, fmt.Errorf("ingest symbol %q: row vanished after INSERT OR IGNORE", w.qn)
		}
	}

	if err := repositionMovedSymbols(ctx, tx, known, stored); err != nil {
		return 0, err
	}
	return inserted, nil
}

// splitSymbolWrites separates the symbols that need an INSERT from the ones
// that already have a row, PRESERVING INDEX ORDER in both.
//
// A name already in the table — or already claimed by an EARLIER entry of
// this same batch, which is how a scanner emitting one id twice behaves — is
// a reposition, never an insert. Letting INSERT OR IGNORE sort the duplicate
// out instead would produce the same table, but only because it silently
// discards a row; being explicit is what keeps the id arithmetic checkable
// against the row-at-a-time writer.
func splitSymbolWrites(writes []symbolWrite, stored map[string]storedSymbol) (fresh, known []symbolWrite) {
	fresh = make([]symbolWrite, 0, len(writes))
	known = make([]symbolWrite, 0, len(writes))
	claimed := make(map[string]bool, len(writes))
	for _, w := range writes {
		if _, seen := stored[w.qn]; seen || claimed[w.qn] {
			known = append(known, w)
			continue
		}
		claimed[w.qn] = true
		fresh = append(fresh, w)
	}
	return fresh, known
}

// repositionMovedSymbols refreshes the rows whose stored contents no longer
// match what this scan produced.
//
// INSERT OR IGNORE alone would leave the FIRST location a symbol was ever
// seen at. A declaration that merely moved (an import added above it, a
// helper hoisted to the top of the file, a function relocated to a sibling
// file) would keep its stale [line, end_line] span, and the coverage ingest
// would keep charging executed statements to a range the function no longer
// occupies. Updating in place — rather than delete+insert — preserves the
// surrogate id, so coverage history and feature_symbols links survive the
// edit.
//
// The UPDATE is skipped when the stored row already says exactly this, which
// the row-at-a-time writer could not do because it had never read the row.
// `symbols` has no updated_at and no update trigger that records anything,
// so a no-op UPDATE is invisible to every reader — the only thing skipping
// it changes is how long a rescan takes, and on a repository where nothing
// moved that is most of the rescan.
func repositionMovedSymbols(
	ctx context.Context, tx *sql.Tx, known []symbolWrite, stored map[string]storedSymbol,
) error {
	for _, w := range known {
		if prev, ok := stored[w.qn]; ok && prev.matches(w) {
			continue
		}
		if err := repositionSymbol(ctx, tx, w); err != nil {
			return err
		}
	}
	return nil
}

// insertSymbolChunks runs the multi-row INSERT and returns how many rows it
// actually created.
func insertSymbolChunks(ctx context.Context, tx *sql.Tx, writes []symbolWrite) (int, error) {
	inserted := 0
	err := inChunks(writes, rowsPerChunk(symbolInsertColumns), func(chunk []symbolWrite) error {
		args := make([]any, 0, len(chunk)*symbolInsertColumns)
		for _, w := range chunk {
			args = append(args, w.insertArgs()...)
		}
		res, err := tx.ExecContext(ctx,
			insertSymbolsSQL+valuesTuples(len(chunk), symbolInsertColumns), args...)
		if err != nil {
			// A batch cannot name the offending row the way a per-row
			// INSERT could — SQLite reports the constraint, not the tuple —
			// so the error names the span instead. That is enough to bisect
			// by hand, and a CHECK violation here means a scanner emitted a
			// kind or a class outside the closed set, which is a bug in that
			// scanner rather than in one row's data.
			return fmt.Errorf("ingest symbols %q..%q (%d rows): %w",
				chunk[0].qn, chunk[len(chunk)-1].qn, len(chunk), err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("ingest symbols %q..%q: rows affected: %w",
				chunk[0].qn, chunk[len(chunk)-1].qn, err)
		}
		inserted += int(n)
		return nil
	})
	return inserted, err
}

// writeSymbolsRowAtATime is the writer this package shipped before #109,
// kept as the reference the batched path is diffed against. It compiles one
// statement per row, which is exactly the cost the profile found.
func writeSymbolsRowAtATime(
	ctx context.Context, tx *sql.Tx, qtx *sqlc.Queries,
	writes []symbolWrite, ids map[shared.SymbolID]int64,
) (int, error) {
	inserted := 0
	for _, w := range writes {
		id, isNew, err := upsertSymbolTx(ctx, tx, qtx, w)
		if err != nil {
			return 0, err
		}
		if isNew {
			inserted++
		}
		ids[shared.SymbolID(w.qn)] = id
	}
	return inserted, nil
}

// upsertSymbolTx inserts one symbol and returns its surrogate id plus
// whether the insert created a new row.
func upsertSymbolTx(ctx context.Context, tx *sql.Tx, qtx *sqlc.Queries, w symbolWrite) (int64, bool, error) {
	res, err := qtx.InsertSymbol(ctx, w.insertParams())
	if err != nil {
		return 0, false, fmt.Errorf("ingest symbol %q: %w", w.qn, err)
	}
	// Whether the row is NEW must come from RowsAffected, not LastInsertId:
	// SQLite leaves last_insert_rowid untouched when INSERT OR IGNORE skips a
	// conflicting row, so LastInsertId returns the id of whatever was
	// inserted BEFORE — in this loop, a neighbouring symbol. Trusting it made
	// every already-known symbol resolve to another symbol's surrogate id on
	// re-scan, so edges, feature links and coverage results were written
	// against the wrong rows.
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("ingest symbol %q: rows affected: %w", w.qn, err)
	}
	if affected > 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return 0, false, fmt.Errorf("ingest symbol %q: last insert id: %w", w.qn, err)
		}
		return id, true, nil
	}
	// Already existed — refresh its position (see writeSymbolsBatched for
	// why an in-place UPDATE rather than delete+insert), then fetch the id.
	if err := repositionSymbol(ctx, tx, w); err != nil {
		return 0, false, err
	}
	id, ok, err := lookupSymbolIDTx(ctx, qtx, shared.SymbolID(w.qn))
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, fmt.Errorf("ingest symbol %q: row vanished after INSERT OR IGNORE", w.qn)
	}
	return id, false, nil
}

func repositionSymbol(ctx context.Context, tx *sql.Tx, w symbolWrite) error {
	if _, err := tx.ExecContext(ctx, updateSymbolPositionSQL,
		w.kind, w.path, w.line, w.endLine, w.pkg, w.domain, w.nodeClass, w.qn,
	); err != nil {
		return fmt.Errorf("ingest symbol %q: refresh position: %w", w.qn, err)
	}
	return nil
}

// storedSymbol is what the table already holds for one qualified name.
type storedSymbol struct {
	id        int64
	kind      string
	path      string
	line      int64
	endLine   sql.NullInt64
	pkg       sql.NullString
	domain    sql.NullString
	nodeClass sql.NullString
}

// matches reports whether the stored row already says exactly what w would
// write, in which case the UPDATE is a no-op and can be skipped.
//
// Every mutable column is compared, node_class included. Comparing only the
// position would be the tempting shortcut and it would reintroduce the bug
// updateSymbolPositionSQL's comment describes: an `external:py` anchor whose
// module later lands in the repository keeps its old class forever if the
// class is not part of "did this row change".
func (s storedSymbol) matches(w symbolWrite) bool {
	return s.kind == w.kind &&
		s.path == w.path &&
		s.line == w.line &&
		nullInt64Is(s.endLine, w.endLine) &&
		nullStringIs(s.pkg, w.pkg) &&
		nullStringIs(s.domain, w.domain) &&
		s.nodeClass.Valid && s.nodeClass.String == w.nodeClass
}

func nullInt64Is(got sql.NullInt64, want *int64) bool {
	if want == nil {
		return !got.Valid
	}
	return got.Valid && got.Int64 == *want
}

func nullStringIs(got sql.NullString, want *string) bool {
	if want == nil {
		return !got.Valid
	}
	return got.Valid && got.String == *want
}

const storedSymbolColumns = `qualified_name, id, kind, file_path, line, end_line, package, domain, node_class`

// loadStoredSymbols reads every named symbol the table already holds, in
// one chunked query rather than a SELECT per name.
func loadStoredSymbols(ctx context.Context, tx *sql.Tx, names []string) (map[string]storedSymbol, error) {
	out := make(map[string]storedSymbol, len(names))
	err := selectSymbolsByName(ctx, tx, storedSymbolColumns, names, func(rows *sql.Rows) error {
		var qn string
		var row storedSymbol
		if err := rows.Scan(&qn, &row.id, &row.kind, &row.path, &row.line,
			&row.endLine, &row.pkg, &row.domain, &row.nodeClass); err != nil {
			return fmt.Errorf("store ingest: scan stored symbol: %w", err)
		}
		out[qn] = row
		return nil
	})
	return out, err
}

// loadSymbolIDs records the surrogate id of every named symbol that exists,
// leaving absent names absent — the caller decides whether that is a
// problem. This is the read the unchanged-file path used to do one symbol
// at a time.
func loadSymbolIDs(ctx context.Context, tx *sql.Tx, names []string, into map[shared.SymbolID]int64) error {
	return selectSymbolsByName(ctx, tx, "qualified_name, id", names, func(rows *sql.Rows) error {
		var qn string
		var id int64
		if err := rows.Scan(&qn, &id); err != nil {
			return fmt.Errorf("store ingest: scan symbol id: %w", err)
		}
		into[shared.SymbolID(qn)] = id
		return nil
	})
}

// selectSymbolsByName runs `SELECT <projection> ... WHERE qualified_name IN
// (...)` in chunks and hands each row to scan.
//
// The IN list is built from placeholders, never from the names themselves —
// a qualified name is scanner-supplied text and has no business being
// concatenated into SQL.
func selectSymbolsByName(
	ctx context.Context, tx *sql.Tx, projection string, names []string, scan func(*sql.Rows) error,
) error {
	return inChunks(names, rowsPerChunk(1), func(chunk []string) error {
		args := make([]any, len(chunk))
		for i, n := range chunk {
			args[i] = n
		}
		query := "SELECT " + projection + " FROM symbols WHERE qualified_name IN (" +
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + ")"
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("store ingest: read %d symbols by name: %w", len(chunk), err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store ingest: read %d symbols by name: %w", len(chunk), err)
		}
		return nil
	})
}

// storedContentHashesMatch returns the subset of paths whose stored
// content_hash equals the one this scan computed — the files whose symbols
// and edges do not need rewriting.
//
// Reads on the bare *sql.DB, before the ingest transaction opens, which is
// where this check has always lived.
//
// A read failure now fails the ingest. The per-file version treated any
// error as "changed" and carried on, which sounds conservative and is not:
// a store that cannot be read is a store that is about to be written, and
// finding out at the SELECT is better than finding out at the COMMIT.
func storedContentHashesMatch(
	ctx context.Context, db *sql.DB, paths []string, fresh map[string]codeindex.FileHash,
) (map[string]bool, error) {
	unchanged := make(map[string]bool, len(paths))
	err := inChunks(paths, rowsPerChunk(1), func(chunk []string) error {
		args := make([]any, len(chunk))
		for i, p := range chunk {
			args[i] = p
		}
		query := "SELECT file_path, content_hash FROM file_hashes WHERE file_path IN (" +
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + ")"
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("store ingest: read %d file hashes: %w", len(chunk), err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var path, hash string
			if err := rows.Scan(&path, &hash); err != nil {
				return fmt.Errorf("store ingest: scan file hash: %w", err)
			}
			if fh, ok := fresh[path]; ok && fh.SHA256 == hash {
				unchanged[path] = true
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store ingest: read %d file hashes: %w", len(chunk), err)
		}
		return nil
	})
	return unchanged, err
}

// pruneStaleSymbolsTx deletes the symbol rows a rescan no longer produces.
//
// Scope is deliberately narrow: only files this scan actually re-read (walked
// AND changed since the last scan) are pruned, so a partial or filtered scan
// never deletes another file's symbols. Within those files, a stored symbol
// whose qualified name is absent from the fresh index is gone from the source
// — renamed, deleted, or moved to another file — and its row is stale. Left
// in place it keeps a [line, end_line] span that no longer holds any code,
// which the coverage ingest happily attributes executed statements to, and
// which `atlas codebase dead` reports as a live-but-uncalled symbol.
//
// Deleting cascades to that symbol's edges, feature links and coverage rows —
// all of which describe a declaration that no longer exists.
func pruneStaleSymbolsTx(ctx context.Context, qtx *sqlc.Queries, freshByFile map[string]map[string]bool, rescanned map[string]bool) (int, error) {
	pruned := 0
	for file := range rescanned {
		rows, err := qtx.ListSymbolNamesByFile(ctx, file)
		if err != nil {
			return pruned, fmt.Errorf("store ingest: list symbols for %q: %w", file, err)
		}
		fresh := freshByFile[file]
		for _, r := range rows {
			if fresh[r.QualifiedName] {
				continue
			}
			if err := qtx.DeleteSymbolByID(ctx, r.ID); err != nil {
				return pruned, fmt.Errorf("store ingest: prune symbol %q: %w", r.QualifiedName, err)
			}
			pruned++
		}
	}
	return pruned, nil
}

func lookupSymbolIDTx(ctx context.Context, qtx *sqlc.Queries, qn shared.SymbolID) (int64, bool, error) {
	id, err := qtx.GetSymbolIDByQualifiedName(ctx, string(qn))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lookup symbol %q: %w", qn, err)
	}
	return id, true, nil
}

// =====================================================================
// Edges
// =====================================================================

const edgeInsertColumns = 8

const insertEdgesSQL = `INSERT OR IGNORE INTO edges
  (from_symbol_id, to_symbol_id, kind, file_path, line, edge_meta, resolution_tier, ambiguous)
VALUES `

// edgeWrite is one edge normalised into the eight columns the table takes.
// The tier has already passed requireTier by the time an edgeWrite exists —
// an edge with no stated resolution mechanism must fail the ingest by name,
// not arrive as a plausible-looking row (issue #146).
type edgeWrite struct {
	fromID    int64
	toID      int64
	kind      string
	path      string
	line      int64
	meta      *string
	tier      string
	ambiguous int64
}

func (w edgeWrite) insertArgs() []any {
	return []any{w.fromID, w.toID, w.kind, w.path, w.line, w.meta, w.tier, w.ambiguous}
}

// writeEdges writes every edge and returns how many rows are new.
//
// Edges carry no surrogate id anyone reads — nothing references edges.id —
// so batching them is less delicate than batching symbols. They are still
// written in graph order, because an ingest whose row order depends on the
// batch size is one refactor away from being an ingest whose CONTENT does.
func writeEdges(
	ctx context.Context, tx *sql.Tx, qtx *sqlc.Queries, writes []edgeWrite, rowAtATime bool,
) (int, error) {
	if rowAtATime {
		return writeEdgesRowAtATime(ctx, qtx, writes)
	}
	inserted := 0
	err := inChunks(writes, rowsPerChunk(edgeInsertColumns), func(chunk []edgeWrite) error {
		args := make([]any, 0, len(chunk)*edgeInsertColumns)
		for _, w := range chunk {
			args = append(args, w.insertArgs()...)
		}
		res, err := tx.ExecContext(ctx,
			insertEdgesSQL+valuesTuples(len(chunk), edgeInsertColumns), args...)
		if err != nil {
			return fmt.Errorf("ingest edges %d->%d..%d->%d (%d rows): %w",
				chunk[0].fromID, chunk[0].toID,
				chunk[len(chunk)-1].fromID, chunk[len(chunk)-1].toID, len(chunk), err)
		}
		// Same #97 hazard as the symbol batch: INSERT OR IGNORE leaves
		// last_insert_rowid untouched when it skips a conflicting row, so
		// LastInsertId would report the id of whatever this transaction
		// inserted previously. RowsAffected is the only value the statement
		// actually sets, and across a multi-row VALUES it counts exactly the
		// tuples that were not ignored.
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("ingest edges (%d rows): rows affected: %w", len(chunk), err)
		}
		inserted += int(n)
		return nil
	})
	return inserted, err
}

// writeEdgesRowAtATime is the pre-#109 writer, kept as the reference the
// batched path is diffed against.
func writeEdgesRowAtATime(ctx context.Context, qtx *sqlc.Queries, writes []edgeWrite) (int, error) {
	inserted := 0
	for _, w := range writes {
		res, err := qtx.InsertEdge(ctx, sqlc.InsertEdgeParams{
			FromSymbolID:   w.fromID,
			ToSymbolID:     w.toID,
			Kind:           w.kind,
			FilePath:       w.path,
			Line:           w.line,
			EdgeMeta:       w.meta,
			ResolutionTier: w.tier,
			Ambiguous:      w.ambiguous,
		})
		if err != nil {
			return 0, fmt.Errorf("ingest edge %d->%d: %w", w.fromID, w.toID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("ingest edge %d->%d: rows affected: %w", w.fromID, w.toID, err)
		}
		if affected > 0 {
			inserted++
		}
	}
	return inserted, nil
}

// =====================================================================
// Annotations and file hashes
// =====================================================================
//
// Both are upserts rather than INSERT OR IGNORE, and both keep their
// conflict clause verbatim from the sqlc query they replace — the point of
// batching is to compile the statement once, not to change what it does.

const annotationInsertColumns = 5

const insertAnnotationsSQL = `INSERT INTO annotations (file_path, line, kind, value, source)
VALUES `

const annotationConflictSQL = `
ON CONFLICT(file_path, line, kind) DO UPDATE SET
  value     = excluded.value,
  source    = excluded.source,
  parsed_at = CURRENT_TIMESTAMP`

type annotationWrite struct {
	path   string
	line   int64
	kind   string
	value  string
	source string
}

func upsertAnnotations(ctx context.Context, tx *sql.Tx, writes []annotationWrite) error {
	return inChunks(writes, rowsPerChunk(annotationInsertColumns), func(chunk []annotationWrite) error {
		args := make([]any, 0, len(chunk)*annotationInsertColumns)
		for _, w := range chunk {
			args = append(args, w.path, w.line, w.kind, w.value, w.source)
		}
		_, err := tx.ExecContext(ctx,
			insertAnnotationsSQL+valuesTuples(len(chunk), annotationInsertColumns)+annotationConflictSQL,
			args...)
		if err != nil {
			return fmt.Errorf("store ingest annotations %s:%d..%s:%d (%d rows): %w",
				chunk[0].path, chunk[0].line,
				chunk[len(chunk)-1].path, chunk[len(chunk)-1].line, len(chunk), err)
		}
		return nil
	})
}

const fileHashInsertColumns = 4

const insertFileHashesSQL = `INSERT INTO file_hashes (file_path, content_hash, mtime, last_scanned)
VALUES `

const fileHashConflictSQL = `
ON CONFLICT(file_path) DO UPDATE SET
  content_hash = excluded.content_hash,
  mtime        = excluded.mtime,
  last_scanned = excluded.last_scanned`

type fileHashWrite struct {
	path        string
	hash        string
	mtime       time.Time
	lastScanned time.Time
}

func upsertFileHashes(ctx context.Context, tx *sql.Tx, writes []fileHashWrite) error {
	return inChunks(writes, rowsPerChunk(fileHashInsertColumns), func(chunk []fileHashWrite) error {
		args := make([]any, 0, len(chunk)*fileHashInsertColumns)
		for _, w := range chunk {
			args = append(args, w.path, w.hash, w.mtime, w.lastScanned)
		}
		_, err := tx.ExecContext(ctx,
			insertFileHashesSQL+valuesTuples(len(chunk), fileHashInsertColumns)+fileHashConflictSQL,
			args...)
		if err != nil {
			return fmt.Errorf("store ingest file_hashes %q..%q (%d rows): %w",
				chunk[0].path, chunk[len(chunk)-1].path, len(chunk), err)
		}
		return nil
	})
}

// extractFeatureIDsFromAnnotation returns the feature ids carried by a
// feature/contract annotation, with `#tag` suffixes (e.g. `#mocked`,
// `#real`, `#flaky`) stripped.
//
// Two code paths feed into this:
//
//   - In the common case, the parser has already split ann.IDs from
//     ann.Tags — we just filter IDs to the well-formed ones (paranoia
//     guard: drop empty strings + any token that slipped through with a
//     leading `#`).
//
//   - Defence in depth: if ann.IDs is empty but ann.Raw carries a
//     whitespace-separated payload (e.g. an integration test fixture
//     bypassing the parser), fall through and split Raw ourselves the
//     same way.
//
// The function is conservative: it never makes up an id, never lowercases,
// never re-validates against idValidationRe. The parser already enforces
// the canonical grammar; this helper just cleans up.
func extractFeatureIDsFromAnnotation(ann shared.Annotation) []string {
	tokens := ann.IDs
	if len(tokens) == 0 && ann.Raw != "" {
		tokens = strings.Fields(ann.Raw)
	}
	out := make([]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// Defensive: a literal `#tag` should never make it into ann.IDs —
		// the parser separates IDs from tags before populating the slice —
		// but the raw-fallback path may include them. Drop both forms.
		if strings.HasPrefix(t, "#") {
			continue
		}
		// `key=value` tags (e.g. `stream=meal_prep_events`, `step=1`) are
		// not feature ids; ann.IDs should never carry them, but the
		// raw-fallback path could. Drop conservatively.
		if strings.ContainsRune(t, '=') {
			continue
		}
		// Enforce the feature/contract id grammar (dotted namespace.feature).
		// This is the single materialization choke point, so filtering here
		// drops stray non-dotted tokens (e.g. `humatier`, `foundational`, a
		// doc-comment word, or a bare tier keyword surviving the raw-fallback
		// split) that would otherwise seed phantom features — regardless of
		// which scanner (Go / TS) produced the annotation. See issue #77.
		if !annotations.IsDottedFeatureID(t) {
			continue
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// domainFor lives in paths.go — kept as a pure string helper outside this
// transactional ingest file. See packages/store/paths.go.
