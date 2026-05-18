package store

import (
	"context"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
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
//
// Extended in Phase 6e (migration 0002) with the EDA-pattern kinds:
// bc, aggregate, aggregate-service, saga, consumer, event-emit,
// outbox-publish.
var schemaAnnotationKinds = map[shared.AnnotationKind]bool{
	shared.AnnFeature:    true,
	shared.AnnContract:   true,
	shared.AnnOwner:      true,
	shared.AnnDeprecated: true,
	shared.AnnSince:      true,

	shared.AnnBC:               true,
	shared.AnnAggregate:        true,
	shared.AnnAggregateService: true,
	shared.AnnSaga:             true,
	shared.AnnConsumer:         true,
	shared.AnnEventEmit:        true,
	shared.AnnOutboxPublish:    true,
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
func (s *Store) Annotations() Annotations { return &annotationsStore{q: s.queries()} }

type annotationsStore struct{ q *sqlc.Queries }

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

	err := a.q.UpsertAnnotation(ctx, sqlc.UpsertAnnotationParams{
		FilePath: row.FilePath,
		Line:     int64(row.Line),
		Kind:     string(row.Kind),
		Value:    row.Value,
		Source:   string(src),
	})
	if err != nil {
		return fmt.Errorf("annotations upsert %q L%d: %w", row.FilePath, row.Line, err)
	}
	return nil
}

func (a *annotationsStore) ListByFile(ctx context.Context, filePath string) ([]AnnotationRow, error) {
	rows, err := a.q.ListAnnotationsByFile(ctx, filePath)
	if err != nil {
		return nil, fmt.Errorf("annotations by-file %q: %w", filePath, err)
	}
	out := make([]AnnotationRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AnnotationRow{
			ID:       r.ID,
			FilePath: r.FilePath,
			Line:     int(r.Line),
			Kind:     shared.AnnotationKind(r.Kind),
			Value:    r.Value,
			Source:   shared.AnnotationSource(r.Source),
			ParsedAt: r.ParsedAt,
		})
	}
	return out, nil
}

func (a *annotationsStore) DeleteByFile(ctx context.Context, filePath string) error {
	if err := a.q.DeleteAnnotationsByFile(ctx, filePath); err != nil {
		return fmt.Errorf("annotations delete-by-file %q: %w", filePath, err)
	}
	return nil
}
