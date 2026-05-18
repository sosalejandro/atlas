package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// ---------------------------------------------------------------------------
// EDA query layer (Phase 6e).
//
// These are read-only queries over the `annotations` table that surface the
// EDA-pattern annotation kinds (bc, aggregate, aggregate-service, saga,
// consumer, event-emit, outbox-publish) as typed Views the rest of the system
// can index on.
//
// All queries are derived from the raw `annotations` table — there is no
// dedicated EDA table because the per-kind dispatch is cheap enough at read
// time (the `kind` column has the annotations_dedupe_idx covering it
// per-file, and the row volume is bounded by the number of source files in
// a project, never more than ~tens of thousands).
//
// The interface is intentionally narrow per ISP — each method owns one
// query shape. Phase 7's CLI verbs map 1:1 onto these methods.
// ---------------------------------------------------------------------------

// AggregateView is the materialised form of an `@atlas:aggregate` annotation
// joined with its (optional) `@atlas:aggregate-service` partner.
//
// CanonicalService is nil when no service annotation exists for the
// aggregate id — that is NOT an error; many aggregates have no canonical
// service helper yet (see FindAggregate doc).
type AggregateView struct {
	Declaration      AnnotationRow  `json:"declaration"`
	CanonicalService *AnnotationRow `json:"canonical_service,omitempty"`
}

// SagaStep is one step of a saga, parsed out of the `step=N` tag in the
// raw annotation Value. Order is the integer N (which the parser has
// already validated as a non-negative integer).
type SagaStep struct {
	Order      int           `json:"order"`
	Annotation AnnotationRow `json:"annotation"`
}

// ConsumerView is one `@atlas:consumer` annotation. Stream is the value of
// the stream= tag (which the parser also promotes to Annotation.IDs, so it
// is the same value as AnnotationRow.Value parsed for stream=).
type ConsumerView struct {
	Stream     string        `json:"stream"`
	Annotation AnnotationRow `json:"annotation"`
}

// EventEmitView groups every emit + publish site for one named event so
// callers can see "where this event is recorded" and "where it is shipped
// to the bus" side-by-side. Empty slices when no sites exist.
type EventEmitView struct {
	EventName  string          `json:"event_name"`
	Emitters   []AnnotationRow `json:"emitters"`
	Publishers []AnnotationRow `json:"publishers"`
}

// EDAQueries is the narrow port for the Phase 6e EDA query methods. Phase 7
// CLI verbs (`atlas codebase agg`, `atlas codebase saga`, etc.) drive every
// call through this interface.
type EDAQueries interface {
	// ListByBC returns every annotation row inside files that declare
	// themselves to be in the given BC (via `@atlas:bc <bcName>`).
	//
	// Returns rows in (file_path, line) order. An empty result is NOT
	// an error — the BC may simply have no annotations yet.
	ListByBC(ctx context.Context, bcName string) ([]AnnotationRow, error)

	// FindAggregate returns the aggregate declaration site for id, plus
	// its linked canonical-service site (if any).
	//
	// Returns (AggregateView{}, sql.ErrNoRows) when no aggregate
	// declaration exists for id. AggregateView.CanonicalService is nil
	// when the aggregate exists but no service annotation links to it
	// — that is NOT an error.
	FindAggregate(ctx context.Context, id string) (AggregateView, error)

	// WalkSaga returns ordered saga steps for the named saga.
	//
	// Steps are ordered by the integer parsed from the `step=N` tag in
	// each annotation's Value. Steps without a parseable step= tag are
	// omitted (the parser already enforces this — they would not have
	// been persisted). Order ties resolve by file_path, line.
	WalkSaga(ctx context.Context, id string) ([]SagaStep, error)

	// ListConsumers returns every consumer subscription. When
	// streamName == "", returns all consumer annotations in the store.
	// Otherwise filters to consumers of that specific stream.
	ListConsumers(ctx context.Context, streamName string) ([]ConsumerView, error)

	// FindEventEmitters returns every `event-emit` + `outbox-publish`
	// annotation for eventName. Empty slices when no sites exist; this
	// is NOT an error.
	FindEventEmitters(ctx context.Context, eventName string) (EventEmitView, error)
}

// EDA returns the Store's EDAQueries port.
func (s *Store) EDA() EDAQueries { return &edaStore{db: s.sqlDB()} }

type edaStore struct{ db *sql.DB }

var _ EDAQueries = (*edaStore)(nil)

// ---------------------------------------------------------------------------
// Implementation
// ---------------------------------------------------------------------------

// scanAnnotationRow scans a single annotations row into AnnotationRow.
// Shared by every query below (we go raw-sql here because sqlc's sqlite
// engine cannot express the dynamic-IN-list joins we need).
//
// Caller wraps the returned error with operation context.
func scanAnnotationRow(scanner interface {
	Scan(dest ...any) error
}) (AnnotationRow, error) {
	var r AnnotationRow
	var kindStr, sourceStr string
	if err := scanner.Scan(
		&r.ID, &r.FilePath, &r.Line, &kindStr, &r.Value, &sourceStr, &r.ParsedAt,
	); err != nil {
		return AnnotationRow{}, fmt.Errorf("annotation scan: %w", err)
	}
	r.Kind = shared.AnnotationKind(kindStr)
	r.Source = shared.AnnotationSource(sourceStr)
	return r, nil
}

func (e *edaStore) ListByBC(ctx context.Context, bcName string) ([]AnnotationRow, error) {
	if bcName == "" {
		return nil, errors.New("ListByBC: bcName required")
	}
	// Two-step query: (1) find every file_path declaring `@atlas:bc <name>`,
	// (2) return every annotation row in those files. Done as a single SQL
	// statement with an IN subquery.
	const q = `
SELECT id, file_path, line, kind, value, source, parsed_at
FROM annotations
WHERE file_path IN (
  SELECT DISTINCT file_path
  FROM annotations
  WHERE kind = 'bc' AND value = ?
)
ORDER BY file_path, line
`
	rows, err := e.db.QueryContext(ctx, q, bcName)
	if err != nil {
		return nil, fmt.Errorf("ListByBC %q: %w", bcName, err)
	}
	defer func() { _ = rows.Close() }()
	out := []AnnotationRow{}
	for rows.Next() {
		r, err := scanAnnotationRow(rows)
		if err != nil {
			return nil, fmt.Errorf("ListByBC %q: scan: %w", bcName, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListByBC %q: rows: %w", bcName, err)
	}
	return out, nil
}

func (e *edaStore) FindAggregate(ctx context.Context, id string) (AggregateView, error) {
	if id == "" {
		return AggregateView{}, errors.New("FindAggregate: id required")
	}
	const declQ = `
SELECT id, file_path, line, kind, value, source, parsed_at
FROM annotations
WHERE kind = 'aggregate' AND value = ?
ORDER BY file_path, line
LIMIT 1
`
	declRow, err := scanAnnotationRow(e.db.QueryRowContext(ctx, declQ, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AggregateView{}, sql.ErrNoRows
		}
		return AggregateView{}, fmt.Errorf("FindAggregate %q: declaration: %w", id, err)
	}
	view := AggregateView{Declaration: declRow}

	const svcQ = `
SELECT id, file_path, line, kind, value, source, parsed_at
FROM annotations
WHERE kind = 'aggregate-service' AND value = ?
ORDER BY file_path, line
LIMIT 1
`
	svcRow, err := scanAnnotationRow(e.db.QueryRowContext(ctx, svcQ, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Linked canonical-service is optional.
			return view, nil
		}
		return AggregateView{}, fmt.Errorf("FindAggregate %q: service: %w", id, err)
	}
	view.CanonicalService = &svcRow
	return view, nil
}

func (e *edaStore) WalkSaga(ctx context.Context, id string) ([]SagaStep, error) {
	if id == "" {
		return nil, errors.New("WalkSaga: id required")
	}
	const q = `
SELECT id, file_path, line, kind, value, source, parsed_at
FROM annotations
WHERE kind = 'saga' AND value LIKE ? || '%'
ORDER BY file_path, line
`
	// We pre-filter with LIKE then re-validate the id below — the LIKE is
	// a row-reduction hint (the saga id is the first token of Value), the
	// final check is the source of truth.
	rows, err := e.db.QueryContext(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("WalkSaga %q: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	out := []SagaStep{}
	for rows.Next() {
		r, err := scanAnnotationRow(rows)
		if err != nil {
			return nil, fmt.Errorf("WalkSaga %q: scan: %w", id, err)
		}
		sagaID, step, ok := parseSagaValue(r.Value)
		if !ok || sagaID != id {
			continue
		}
		out = append(out, SagaStep{Order: step, Annotation: r})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("WalkSaga %q: rows: %w", id, err)
	}
	// Sort by Order, ties broken by file_path,line (which the rows already
	// arrive in, so a stable sort preserves it).
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

func (e *edaStore) ListConsumers(ctx context.Context, streamName string) ([]ConsumerView, error) {
	const baseQ = `
SELECT id, file_path, line, kind, value, source, parsed_at
FROM annotations
WHERE kind = 'consumer'
`
	var (
		rows *sql.Rows
		err  error
	)
	if streamName == "" {
		rows, err = e.db.QueryContext(ctx, baseQ+" ORDER BY file_path, line")
	} else {
		// Value carries `stream=<name>` per parser contract. Filter via LIKE
		// pre-reduction; final source of truth is parseConsumerStream.
		rows, err = e.db.QueryContext(ctx,
			baseQ+" AND value LIKE ? ORDER BY file_path, line",
			"%stream="+streamName+"%")
	}
	if err != nil {
		return nil, fmt.Errorf("ListConsumers %q: %w", streamName, err)
	}
	defer func() { _ = rows.Close() }()
	out := []ConsumerView{}
	for rows.Next() {
		r, err := scanAnnotationRow(rows)
		if err != nil {
			return nil, fmt.Errorf("ListConsumers %q: scan: %w", streamName, err)
		}
		got, ok := parseConsumerStream(r.Value)
		if !ok {
			// Shouldn't happen — parser rejects stream-less consumers —
			// but be defensive.
			continue
		}
		if streamName != "" && got != streamName {
			continue
		}
		out = append(out, ConsumerView{Stream: got, Annotation: r})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListConsumers %q: rows: %w", streamName, err)
	}
	return out, nil
}

func (e *edaStore) FindEventEmitters(ctx context.Context, eventName string) (EventEmitView, error) {
	if eventName == "" {
		return EventEmitView{}, errors.New("FindEventEmitters: eventName required")
	}
	view := EventEmitView{
		EventName:  eventName,
		Emitters:   []AnnotationRow{},
		Publishers: []AnnotationRow{},
	}

	const q = `
SELECT id, file_path, line, kind, value, source, parsed_at
FROM annotations
WHERE kind IN ('event-emit', 'outbox-publish') AND value = ?
ORDER BY file_path, line
`
	rows, err := e.db.QueryContext(ctx, q, eventName)
	if err != nil {
		return EventEmitView{}, fmt.Errorf("FindEventEmitters %q: %w", eventName, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r, err := scanAnnotationRow(rows)
		if err != nil {
			return EventEmitView{}, fmt.Errorf("FindEventEmitters %q: scan: %w", eventName, err)
		}
		switch r.Kind {
		case shared.AnnEventEmit:
			view.Emitters = append(view.Emitters, r)
		case shared.AnnOutboxPublish:
			view.Publishers = append(view.Publishers, r)
		}
	}
	if err := rows.Err(); err != nil {
		return EventEmitView{}, fmt.Errorf("FindEventEmitters %q: rows: %w", eventName, err)
	}
	return view, nil
}

// ---------------------------------------------------------------------------
// Value parsers — extract per-kind structured fields from the raw Value
// string. These mirror the parser's grammar and stay in lockstep with it.
// ---------------------------------------------------------------------------

// parseSagaValue extracts the saga id (first whitespace-separated token)
// and the integer step from `step=N` if present. Returns ok=false when no
// step tag exists or it does not parse as an integer.
func parseSagaValue(value string) (sagaID string, step int, ok bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return "", 0, false
	}
	sagaID = fields[0]
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "step=") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(f, "step="))
		if err != nil {
			return sagaID, 0, false
		}
		return sagaID, n, true
	}
	return sagaID, 0, false
}

// parseConsumerStream extracts the stream= value from a consumer
// annotation's Value. Returns ok=false when no stream= tag exists.
func parseConsumerStream(value string) (stream string, ok bool) {
	for _, f := range strings.Fields(value) {
		if strings.HasPrefix(f, "stream=") {
			s := strings.TrimPrefix(f, "stream=")
			if s == "" {
				return "", false
			}
			return s, true
		}
	}
	return "", false
}
