package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// IngestStats records what Ingest wrote. Useful for the `atlas scan`
// command's terminal summary line ("symbols: 1342  edges: 4571 ...") and
// for tests that need to assert on side-effect shape.
type IngestStats struct {
	SymbolsInserted        int           `json:"symbols_inserted"`
	EdgesInserted          int           `json:"edges_inserted"`
	AnnotationsInserted    int           `json:"annotations_inserted"`
	FileHashesUpserted     int           `json:"file_hashes_upserted"`
	PatternMatchesSet      int           `json:"pattern_matches_set"`
	FeaturesMaterialized   int           `json:"features_materialized"`
	FeatureSymbolsLinked   int           `json:"feature_symbols_linked"`
	OrphanAnnotationsSkipped int         `json:"orphan_annotations_skipped"`
	FilesScanned           int           `json:"files_scanned"`
	FilesSkipped           int           `json:"files_skipped"`
	Duration               time.Duration `json:"duration"`
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
// All writes go through the sqlc-generated Queries (via WithTx for the
// transactional batch) — only the unchanged-file detection still reads via
// the FileHashes port, which is fine because that read happens before the
// tx opens.
func (s *Store) Ingest(ctx context.Context, idx *codeindex.Index) (*IngestStats, error) {
	if idx == nil {
		return nil, fmt.Errorf("store ingest: nil index")
	}
	start := time.Now()
	stats := &IngestStats{}

	// 1. Compute the set of files whose hash hasn't changed since the last
	// scan — we'll skip writing symbols/edges for those.
	unchanged := map[string]bool{}
	for path, fh := range idx.FileHashes {
		stats.FilesScanned++
		existing, err := s.FileHashes().Get(ctx, path)
		if err == nil && existing.ContentHash == fh.SHA256 {
			unchanged[path] = true
			stats.FilesSkipped++
		}
	}

	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store ingest: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	// 2. Upsert symbols (skip those declared in unchanged files).
	symbolIDByQualifiedName := make(map[shared.SymbolID]int64, len(idx.Symbols))
	for _, sym := range idx.Symbols {
		if sym.ID == "" {
			continue
		}
		path := sym.Position.Path
		if unchanged[path] {
			// Even when skipping inserts, we still need the surrogate id
			// for edge writes — look it up.
			id, ok, err := lookupSymbolIDTx(ctx, qtx, sym.ID)
			if err != nil {
				return nil, err
			}
			if ok {
				symbolIDByQualifiedName[sym.ID] = id
			}
			continue
		}
		id, inserted, err := upsertSymbolTx(ctx, qtx, s.logger, sym)
		if err != nil {
			return nil, err
		}
		if inserted {
			stats.SymbolsInserted++
		}
		// Skip position-less symbols (id == 0). upsertSymbolTx deliberately
		// returns (0, false, nil) for symbols without a file path — they
		// only exist in the in-memory graph, never in the SQLite store. If
		// we recorded their qn → 0 mapping, the edge pass would try to
		// insert an edge with to_symbol_id = 0 → FOREIGN KEY violation.
		if id == 0 {
			continue
		}
		symbolIDByQualifiedName[sym.ID] = id
	}

	// 3. Upsert edges. Skip edges where either endpoint lives in an unchanged
	// file — the existing rows are already authoritative. Also skip edges
	// whose endpoints have a zero surrogate id (position-less symbols that
	// got filtered above) — writing to_symbol_id = 0 is a FOREIGN KEY error.
	if idx.Graph != nil {
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
			inserted, err := upsertEdgeTx(ctx, qtx, fromID, toID, kind, path, line, meta)
			if err != nil {
				return nil, err
			}
			if inserted {
				stats.EdgesInserted++
			}
		}
	}

	// 4. Upsert raw annotations (skip those whose file is unchanged — same
	// content means same line numbers means same rows already exist).
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
		if err := qtx.UpsertAnnotation(ctx, sqlc.UpsertAnnotationParams{
			FilePath: path,
			Line:     int64(ann.Position.Line),
			Kind:     string(ann.Kind),
			Value:    value,
			Source:   string(src),
		}); err != nil {
			return nil, fmt.Errorf("store ingest annotation %q L%d: %w", path, ann.Position.Line, err)
		}
		stats.AnnotationsInserted++
	}

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
		val := string(b)
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
		symRow, err := qtx.LookupSymbolAtOrAfterLine(ctx, sqlc.LookupSymbolAtOrAfterLineParams{
			FilePath:     ann.Position.Path,
			Line:         int64(ann.Position.Line),
			MaxLookahead: defaultPositionLookahead,
		})
		if errors.Is(err, sql.ErrNoRows) {
			stats.OrphanAnnotationsSkipped++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store ingest feature-materialize lookup %q L%d: %w",
				ann.Position.Path, ann.Position.Line, err)
		}
		symID := symRow.ID

		featureKind := FeatureKindFeature
		if ann.Kind == shared.AnnContract {
			featureKind = FeatureKindContract
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

			role := RoleImpl
			if featureKind == FeatureKindContract {
				role = RoleContract
			}
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
	for path, fh := range idx.FileHashes {
		if err := qtx.UpsertFileHash(ctx, sqlc.UpsertFileHashParams{
			FilePath:    path,
			ContentHash: fh.SHA256,
			Mtime:       fh.ModTime,
			LastScanned: fh.LastScanned,
		}); err != nil {
			return nil, fmt.Errorf("store ingest file_hash %q: %w", path, err)
		}
		stats.FileHashesUpserted++
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store ingest: commit: %w", err)
	}

	stats.Duration = time.Since(start)
	return stats, nil
}

// upsertSymbolTx inserts a shared.Symbol via the sqlc tx and returns the
// row's surrogate id plus whether the insert created a new row.
//
// The logger argument is the Store's logger — it carries the parser-drift
// Warn record emitted by normalizeKindForWrite when an unknown kind has to
// be collapsed to KindFunc. Pass shared.NopLogger{} in tests that don't
// care about the warning side channel.
func upsertSymbolTx(ctx context.Context, qtx *sqlc.Queries, logger shared.Logger, sym shared.Symbol) (int64, bool, error) {
	kind := normalizeKindForWrite(ctx, logger, "ingest.upsertSymbolTx", sym.ID, sym.Kind)
	var pkg *string
	if sym.Package != "" {
		v := sym.Package
		pkg = &v
	}
	var bc *string
	if bcPath := bcPathFor(sym.Position.Path); bcPath != "" {
		v := bcPath
		bc = &v
	}

	path := sym.Position.Path
	if path == "" {
		// Skip synthetic / position-less symbols — the schema's file_path is
		// NOT NULL. Their qualified names typically encode `route:` or
		// `endpoint:` prefixes that are graph-walk-only.
		return 0, false, nil
	}
	line := sym.Position.Line
	if line <= 0 {
		line = 1
	}

	res, err := qtx.InsertSymbol(ctx, sqlc.InsertSymbolParams{
		QualifiedName: string(sym.ID),
		Kind:          string(kind),
		FilePath:      path,
		Line:          int64(line),
		EndLine:       nil,
		Package:       pkg,
		BcPath:        bc,
	})
	if err != nil {
		return 0, false, fmt.Errorf("ingest symbol %q: %w", sym.ID, err)
	}
	id, _ := res.LastInsertId()
	if id != 0 {
		return id, true, nil
	}
	// Already existed — fetch the surrogate id.
	id, ok, err := lookupSymbolIDTx(ctx, qtx, sym.ID)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, fmt.Errorf("ingest symbol %q: row vanished after INSERT OR IGNORE", sym.ID)
	}
	return id, false, nil
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

func upsertEdgeTx(ctx context.Context, qtx *sqlc.Queries, fromID, toID int64, kind EdgeKind, filePath string, line int, meta string) (bool, error) {
	res, err := qtx.InsertEdge(ctx, sqlc.InsertEdgeParams{
		FromSymbolID: fromID,
		ToSymbolID:   toID,
		Kind:         string(kind),
		FilePath:     filePath,
		Line:         int64(line),
		EdgeMeta:     metaParam(meta),
	})
	if err != nil {
		return false, fmt.Errorf("ingest edge %d->%d: %w", fromID, toID, err)
	}
	id, _ := res.LastInsertId()
	return id != 0, nil
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
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// bcPathFor lives in paths.go — kept as a pure string helper outside this
// transactional ingest file. See packages/store/paths.go.
