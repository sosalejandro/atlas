package store

import (
	"context"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

// AnnotationRow is one row of the `annotations` table (docs/schema-v1.md §5.11).
//
// This is the raw extract — pre-resolution. The resolver pass reads from
// here, looks up the nearest symbol below the annotation line, and emits
// the appropriate `feature_symbols` row.
type AnnotationRow struct {
	ID       int64                   `json:"id"`
	FilePath string                  `json:"file_path"`
	Line     int                     `json:"line"`
	Kind     shared.AnnotationKind   `json:"kind"`
	Value    string                  `json:"value"`
	Source   shared.AnnotationSource `json:"source"`
	ParsedAt time.Time               `json:"parsed_at"`
}

// schemaAnnotationKinds is the closed set the §5.11 CHECK constraint
// accepts (note: `api` is intentionally NOT persisted — it is consumed by
// the in-memory route resolver, not the annotations table).
var schemaAnnotationKinds = map[shared.AnnotationKind]bool{
	shared.AnnFeature:    true,
	shared.AnnContract:   true,
	shared.AnnOwner:      true,
	shared.AnnDeprecated: true,
	shared.AnnSince:      true,
}

// schemaAnnotationSources is the closed set the §5.11 CHECK constraint
// accepts. `api` source maps to `atlas` for storage parity (it's not part
// of the @atlas grammar but the parser also emits it).
var schemaAnnotationSources = map[shared.AnnotationSource]bool{
	shared.SourceAtlas:   true,
	shared.SourceTestreg: true,
}

// Annotations is the narrow port for the `annotations` raw-extract table.
type Annotations interface {
	// Upsert inserts a single annotation row. The unique
	// (file_path, line, kind) constraint dedupes; a re-parse refreshes
	// value/source/parsed_at.
	Upsert(ctx context.Context, row AnnotationRow) error

	// ListByFile returns every annotation row for filePath, ordered by line.
	ListByFile(ctx context.Context, filePath string) ([]AnnotationRow, error)

	// DeleteByFile removes every annotation row for filePath. Used by the
	// incremental re-scan to clear stale rows before inserting fresh ones.
	DeleteByFile(ctx context.Context, filePath string) error
}

var _ Annotations = (*annotationsStore)(nil)

// Annotations returns the Store's Annotations port.
func (s *Store) Annotations() Annotations { return &annotationsStore{db: s} }

type annotationsStore struct{ db *Store }

func (a *annotationsStore) Upsert(ctx context.Context, row AnnotationRow) error {
	if row.FilePath == "" {
		return fmt.Errorf("annotations upsert: file_path required")
	}
	if row.Line <= 0 {
		return fmt.Errorf("annotations upsert %q: line must be > 0", row.FilePath)
	}
	if !schemaAnnotationKinds[row.Kind] {
		// Silently skip kinds the schema doesn't know about (e.g. AnnAPI).
		// The caller can choose to filter ahead of time; here we just no-op
		// so the resolver doesn't have to special-case.
		return nil
	}
	src := row.Source
	if !schemaAnnotationSources[src] {
		src = shared.SourceAtlas
	}

	_, err := a.db.sqlDB().ExecContext(ctx, `
		INSERT INTO annotations (file_path, line, kind, value, source)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(file_path, line, kind) DO UPDATE SET
		  value     = excluded.value,
		  source    = excluded.source,
		  parsed_at = CURRENT_TIMESTAMP
	`, row.FilePath, row.Line, string(row.Kind), row.Value, string(src))
	if err != nil {
		return fmt.Errorf("annotations upsert %q L%d: %w", row.FilePath, row.Line, err)
	}
	return nil
}

func (a *annotationsStore) ListByFile(ctx context.Context, filePath string) ([]AnnotationRow, error) {
	rows, err := a.db.sqlDB().QueryContext(ctx, `
		SELECT id, file_path, line, kind, value, source, parsed_at
		FROM annotations WHERE file_path = ? ORDER BY line, kind
	`, filePath)
	if err != nil {
		return nil, fmt.Errorf("annotations by-file %q: %w", filePath, err)
	}
	defer rows.Close()

	var out []AnnotationRow
	for rows.Next() {
		var (
			r      AnnotationRow
			kind   string
			source string
		)
		if err := rows.Scan(&r.ID, &r.FilePath, &r.Line, &kind, &r.Value, &source, &r.ParsedAt); err != nil {
			return nil, fmt.Errorf("annotations scan: %w", err)
		}
		r.Kind = shared.AnnotationKind(kind)
		r.Source = shared.AnnotationSource(source)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (a *annotationsStore) DeleteByFile(ctx context.Context, filePath string) error {
	_, err := a.db.sqlDB().ExecContext(ctx, `DELETE FROM annotations WHERE file_path = ?`, filePath)
	if err != nil {
		return fmt.Errorf("annotations delete-by-file %q: %w", filePath, err)
	}
	return nil
}
