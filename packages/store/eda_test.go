package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// seedEDA writes a small fixture into the annotations table covering every
// EDA kind. Each helper test seeds whatever subset it needs.
func seedEDA(t *testing.T, a Annotations, rows []AnnotationRow) {
	t.Helper()
	ctx := context.Background()
	for _, r := range rows {
		if err := a.Upsert(ctx, r); err != nil {
			t.Fatalf("seedEDA upsert %+v: %v", r, err)
		}
	}
}

func TestEDA_ListByBC(t *testing.T) {
	s := openTestStore(t)
	a := s.Annotations()

	// Two files in BC "identity", one file in BC "meal_prep", one file
	// with no bc annotation.
	seedEDA(t, a, []AnnotationRow{
		// identity BC
		{FilePath: "src/identity/login.go", Line: 1, Kind: shared.AnnBC, Value: "identity", Source: shared.SourceAtlas},
		{FilePath: "src/identity/login.go", Line: 10, Kind: shared.AnnFeature, Value: "auth.login", Source: shared.SourceAtlas},
		{FilePath: "src/identity/session.go", Line: 1, Kind: shared.AnnBC, Value: "identity", Source: shared.SourceAtlas},
		{FilePath: "src/identity/session.go", Line: 20, Kind: shared.AnnEventEmit, Value: "session_created", Source: shared.SourceAtlas},
		// meal_prep BC
		{FilePath: "src/meal_prep/batch.go", Line: 1, Kind: shared.AnnBC, Value: "meal_prep", Source: shared.SourceAtlas},
		{FilePath: "src/meal_prep/batch.go", Line: 5, Kind: shared.AnnAggregate, Value: "meal_prep.batch_session", Source: shared.SourceAtlas},
		// uncategorised file
		{FilePath: "src/util/clock.go", Line: 1, Kind: shared.AnnFeature, Value: "util.clock", Source: shared.SourceAtlas},
	})

	got, err := s.EDA().ListByBC(context.Background(), "identity")
	if err != nil {
		t.Fatalf("ListByBC: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListByBC(identity) got %d rows, want 4: %+v", len(got), got)
	}

	mp, err := s.EDA().ListByBC(context.Background(), "meal_prep")
	if err != nil {
		t.Fatalf("ListByBC meal_prep: %v", err)
	}
	if len(mp) != 2 {
		t.Fatalf("ListByBC(meal_prep) got %d rows, want 2", len(mp))
	}

	// Unknown BC → empty, not error.
	empty, err := s.EDA().ListByBC(context.Background(), "no_such_bc")
	if err != nil {
		t.Fatalf("ListByBC no_such_bc: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty for unknown bc; got %+v", empty)
	}
}

func TestEDA_ListByBC_EmptyNameRejected(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.EDA().ListByBC(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty bcName")
	}
}

func TestEDA_FindAggregate_WithCanonicalService(t *testing.T) {
	s := openTestStore(t)
	seedEDA(t, s.Annotations(), []AnnotationRow{
		{FilePath: "src/meal_prep/batch.go", Line: 5, Kind: shared.AnnAggregate, Value: "meal_prep.batch_session", Source: shared.SourceAtlas},
		{FilePath: "src/meal_prep/service.go", Line: 12, Kind: shared.AnnAggregateService, Value: "meal_prep.batch_session", Source: shared.SourceAtlas},
	})

	view, err := s.EDA().FindAggregate(context.Background(), "meal_prep.batch_session")
	if err != nil {
		t.Fatalf("FindAggregate: %v", err)
	}
	if view.Declaration.FilePath != "src/meal_prep/batch.go" {
		t.Fatalf("declaration file = %q", view.Declaration.FilePath)
	}
	if view.CanonicalService == nil {
		t.Fatal("expected linked canonical service; got nil")
	}
	if view.CanonicalService.FilePath != "src/meal_prep/service.go" {
		t.Fatalf("service file = %q", view.CanonicalService.FilePath)
	}
}

func TestEDA_FindAggregate_WithoutCanonicalService(t *testing.T) {
	// Edge case (pressure dim: state-shape): aggregate without a linked
	// service. FindAggregate returns CanonicalService=nil, NOT an error.
	s := openTestStore(t)
	seedEDA(t, s.Annotations(), []AnnotationRow{
		{FilePath: "src/meal_prep/batch.go", Line: 5, Kind: shared.AnnAggregate, Value: "meal_prep.batch_session", Source: shared.SourceAtlas},
	})

	view, err := s.EDA().FindAggregate(context.Background(), "meal_prep.batch_session")
	if err != nil {
		t.Fatalf("FindAggregate (no service): %v", err)
	}
	if view.CanonicalService != nil {
		t.Fatalf("expected CanonicalService=nil; got %+v", view.CanonicalService)
	}
}

func TestEDA_FindAggregate_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.EDA().FindAggregate(context.Background(), "no_such.aggregate")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for missing aggregate; got %v", err)
	}
}

func TestEDA_WalkSaga_Ordering(t *testing.T) {
	s := openTestStore(t)
	seedEDA(t, s.Annotations(), []AnnotationRow{
		// Saga steps deliberately seeded out of order so the query has
		// to sort them.
		{FilePath: "src/meal_prep/saga.go", Line: 30, Kind: shared.AnnSaga, Value: "meal_prep_flow step=3", Source: shared.SourceAtlas},
		{FilePath: "src/meal_prep/saga.go", Line: 10, Kind: shared.AnnSaga, Value: "meal_prep_flow step=1", Source: shared.SourceAtlas},
		{FilePath: "src/meal_prep/saga.go", Line: 20, Kind: shared.AnnSaga, Value: "meal_prep_flow step=2", Source: shared.SourceAtlas},
		// Unrelated saga in the same store — must be filtered out.
		{FilePath: "src/identity/saga.go", Line: 5, Kind: shared.AnnSaga, Value: "session_handoff step=1", Source: shared.SourceAtlas},
	})

	steps, err := s.EDA().WalkSaga(context.Background(), "meal_prep_flow")
	if err != nil {
		t.Fatalf("WalkSaga: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("WalkSaga got %d steps; want 3", len(steps))
	}
	for i, st := range steps {
		wantOrder := i + 1
		if st.Order != wantOrder {
			t.Fatalf("step[%d].Order = %d; want %d", i, st.Order, wantOrder)
		}
	}
}

func TestEDA_WalkSaga_SkipsStepless(t *testing.T) {
	// A saga annotation with no step= tag won't be persisted in practice
	// (parser doesn't require step=, but FindSaga only returns step-tagged
	// entries — verify the store layer is defensive against parser-skipped
	// rows that somehow snuck in).
	s := openTestStore(t)
	seedEDA(t, s.Annotations(), []AnnotationRow{
		{FilePath: "src/x.go", Line: 1, Kind: shared.AnnSaga, Value: "saga_a", Source: shared.SourceAtlas},
		{FilePath: "src/x.go", Line: 2, Kind: shared.AnnSaga, Value: "saga_a step=1", Source: shared.SourceAtlas},
	})

	steps, err := s.EDA().WalkSaga(context.Background(), "saga_a")
	if err != nil {
		t.Fatalf("WalkSaga: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step-tagged annotation; got %d (%+v)", len(steps), steps)
	}
}

func TestEDA_ListConsumers_All(t *testing.T) {
	s := openTestStore(t)
	seedEDA(t, s.Annotations(), []AnnotationRow{
		{FilePath: "src/a.go", Line: 1, Kind: shared.AnnConsumer, Value: "stream=meal_prep_events", Source: shared.SourceAtlas},
		{FilePath: "src/b.go", Line: 1, Kind: shared.AnnConsumer, Value: "stream=identity_events", Source: shared.SourceAtlas},
		{FilePath: "src/c.go", Line: 1, Kind: shared.AnnConsumer, Value: "stream=meal_prep_events", Source: shared.SourceAtlas},
	})

	all, err := s.EDA().ListConsumers(context.Background(), "")
	if err != nil {
		t.Fatalf("ListConsumers all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListConsumers all got %d; want 3", len(all))
	}

	mp, err := s.EDA().ListConsumers(context.Background(), "meal_prep_events")
	if err != nil {
		t.Fatalf("ListConsumers meal_prep_events: %v", err)
	}
	if len(mp) != 2 {
		t.Fatalf("ListConsumers(meal_prep_events) got %d; want 2", len(mp))
	}
	for _, cv := range mp {
		if cv.Stream != "meal_prep_events" {
			t.Fatalf("stream = %q; want meal_prep_events", cv.Stream)
		}
	}

	none, err := s.EDA().ListConsumers(context.Background(), "no_such_stream")
	if err != nil {
		t.Fatalf("ListConsumers no_such: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected empty; got %+v", none)
	}
}

func TestEDA_FindEventEmitters(t *testing.T) {
	s := openTestStore(t)
	seedEDA(t, s.Annotations(), []AnnotationRow{
		{FilePath: "src/agg.go", Line: 50, Kind: shared.AnnEventEmit, Value: "batch_session_started", Source: shared.SourceAtlas},
		{FilePath: "src/outbox.go", Line: 80, Kind: shared.AnnOutboxPublish, Value: "batch_session_started", Source: shared.SourceAtlas},
		{FilePath: "src/agg.go", Line: 60, Kind: shared.AnnEventEmit, Value: "batch_session_completed", Source: shared.SourceAtlas},
		// Decoy: an event-emit for a different event must be filtered.
		{FilePath: "src/other.go", Line: 1, Kind: shared.AnnEventEmit, Value: "session_created", Source: shared.SourceAtlas},
	})

	view, err := s.EDA().FindEventEmitters(context.Background(), "batch_session_started")
	if err != nil {
		t.Fatalf("FindEventEmitters: %v", err)
	}
	if view.EventName != "batch_session_started" {
		t.Fatalf("EventName = %q", view.EventName)
	}
	if len(view.Emitters) != 1 {
		t.Fatalf("Emitters got %d; want 1", len(view.Emitters))
	}
	if len(view.Publishers) != 1 {
		t.Fatalf("Publishers got %d; want 1", len(view.Publishers))
	}
	if view.Emitters[0].FilePath != "src/agg.go" || view.Publishers[0].FilePath != "src/outbox.go" {
		t.Fatalf("unexpected file paths: emit=%s publish=%s",
			view.Emitters[0].FilePath, view.Publishers[0].FilePath)
	}

	// Event with no sites — returns empty slices, NOT an error.
	none, err := s.EDA().FindEventEmitters(context.Background(), "no_such_event")
	if err != nil {
		t.Fatalf("FindEventEmitters no_such: %v", err)
	}
	if len(none.Emitters) != 0 || len(none.Publishers) != 0 {
		t.Fatalf("expected empty view; got %+v", none)
	}
}

func TestEDA_FindEventEmitters_EmptyNameRejected(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.EDA().FindEventEmitters(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty eventName")
	}
}

// TestEDA_NewKindsPersist asserts the schema CHECK constraint accepts every
// new EDA kind (regression guard against forgetting to migrate-up).
func TestEDA_NewKindsPersist(t *testing.T) {
	s := openTestStore(t)
	a := s.Annotations()
	ctx := context.Background()

	newKinds := []shared.AnnotationKind{
		shared.AnnBC, shared.AnnAggregate, shared.AnnAggregateService,
		shared.AnnSaga, shared.AnnConsumer, shared.AnnEventEmit,
		shared.AnnOutboxPublish,
	}
	for i, k := range newKinds {
		row := AnnotationRow{
			FilePath: "src/x.go",
			Line:     i + 1,
			Kind:     k,
			Value:    "test_value",
			Source:   shared.SourceAtlas,
		}
		if err := a.Upsert(ctx, row); err != nil {
			t.Fatalf("Upsert kind=%s: %v", k, err)
		}
	}
	got, err := a.ListByFile(ctx, "src/x.go")
	if err != nil {
		t.Fatalf("ListByFile: %v", err)
	}
	if len(got) != len(newKinds) {
		t.Fatalf("got %d rows; want %d", len(got), len(newKinds))
	}
}
