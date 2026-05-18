package store

import (
	"context"
	"database/sql"
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
	SymbolsInserted     int           `json:"symbols_inserted"`
	EdgesInserted       int           `json:"edges_inserted"`
	AnnotationsInserted int           `json:"annotations_inserted"`
	FileHashesUpserted  int           `json:"file_hashes_upserted"`
	FilesScanned        int           `json:"files_scanned"`
	FilesSkipped        int           `json:"files_skipped"`
	Duration            time.Duration `json:"duration"`
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
		id, inserted, err := upsertSymbolTx(ctx, qtx, sym)
		if err != nil {
			return nil, err
		}
		if inserted {
			stats.SymbolsInserted++
		}
		symbolIDByQualifiedName[sym.ID] = id
	}

	// 3. Upsert edges. Skip edges where either endpoint lives in an unchanged
	// file — the existing rows are already authoritative.
	if idx.Graph != nil {
		for _, e := range idx.Graph.Edges {
			fromID, ok := symbolIDByQualifiedName[e.From]
			if !ok {
				continue
			}
			toID, ok := symbolIDByQualifiedName[e.To]
			if !ok {
				continue
			}
			fromNode, hasFrom := idx.Graph.Nodes[e.From]
			if !hasFrom {
				continue
			}
			path := fromNode.Position.Path
			line := fromNode.Position.Line
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
			inserted, err := upsertEdgeTx(ctx, qtx, fromID, toID, EdgeKindCall, path, line)
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
func upsertSymbolTx(ctx context.Context, qtx *sqlc.Queries, sym shared.Symbol) (int64, bool, error) {
	kind := normalizeKind(sym.Kind)
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

func upsertEdgeTx(ctx context.Context, qtx *sqlc.Queries, fromID, toID int64, kind EdgeKind, filePath string, line int) (bool, error) {
	res, err := qtx.InsertEdge(ctx, sqlc.InsertEdgeParams{
		FromSymbolID: fromID,
		ToSymbolID:   toID,
		Kind:         string(kind),
		FilePath:     filePath,
		Line:         int64(line),
	})
	if err != nil {
		return false, fmt.Errorf("ingest edge %d->%d: %w", fromID, toID, err)
	}
	id, _ := res.LastInsertId()
	return id != 0, nil
}

// bcPathFor returns the bounded-context path prefix for a repo-relative
// file path, or "" if the file does not live under src/contexts/<bc>/.
//
// The convention is fixed by docs/architecture.md §3.7 + schema-v1.md §5.4.
// Atlas treats anything matching `src/contexts/<bc>/` as living in that BC.
func bcPathFor(relPath string) string {
	const prefix = "src/contexts/"
	if !strings.HasPrefix(relPath, prefix) {
		return ""
	}
	rest := relPath[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return ""
	}
	return prefix + rest[:slash]
}
