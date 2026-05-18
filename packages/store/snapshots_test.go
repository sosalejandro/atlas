package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestSnapshots_CaptureAndGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	indexJSON := `{"root":"/tmp/p","symbols":[]}`
	auditJSON := `[{"feature_id":"x","score":80}]`
	notes := "pre-cutover"

	id, err := s.Snapshots().Capture(ctx, CaptureInput{
		GitRef:    "abc123",
		IndexJSON: indexJSON,
		AuditJSON: &auditJSON,
		Notes:     &notes,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if id == 0 {
		t.Fatal("Capture returned id=0")
	}

	row, err := s.Snapshots().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.GitRef != "abc123" {
		t.Errorf("GitRef = %q, want abc123", row.GitRef)
	}
	if row.IndexJSON != indexJSON {
		t.Errorf("IndexJSON mismatch")
	}
	if row.AuditJSON == nil || *row.AuditJSON != auditJSON {
		t.Errorf("AuditJSON mismatch: %v", row.AuditJSON)
	}
	if row.Notes == nil || *row.Notes != notes {
		t.Errorf("Notes mismatch: %v", row.Notes)
	}
	if row.CapturedAt.IsZero() {
		t.Error("CapturedAt should default to now()")
	}
}

func TestSnapshots_Capture_NoAuditNoNotes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.Snapshots().Capture(ctx, CaptureInput{
		GitRef:    "deadbeef",
		IndexJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	row, err := s.Snapshots().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.AuditJSON != nil {
		t.Errorf("AuditJSON expected nil, got %v", row.AuditJSON)
	}
	if row.Notes != nil {
		t.Errorf("Notes expected nil, got %v", row.Notes)
	}
}

func TestSnapshots_Capture_RequiresGitRef(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Snapshots().Capture(context.Background(), CaptureInput{
		IndexJSON: `{}`,
	}); err == nil {
		t.Fatal("expected error for empty git_ref, got nil")
	}
}

func TestSnapshots_Capture_RequiresIndexJSON(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Snapshots().Capture(context.Background(), CaptureInput{
		GitRef: "abc",
	}); err == nil {
		t.Fatal("expected error for empty index_json, got nil")
	}
}

func TestSnapshots_Capture_RejectsInvalidJSON(t *testing.T) {
	s := openTestStore(t)
	bad := "this is not json"
	if _, err := s.Snapshots().Capture(context.Background(), CaptureInput{
		GitRef:    "abc",
		IndexJSON: bad,
	}); err == nil {
		t.Fatal("expected error for invalid index_json, got nil")
	}
	auditBad := "not json"
	if _, err := s.Snapshots().Capture(context.Background(), CaptureInput{
		GitRef:    "abc",
		IndexJSON: `{}`,
		AuditJSON: &auditBad,
	}); err == nil {
		t.Fatal("expected error for invalid audit_json, got nil")
	}
}

func TestSnapshots_Get_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.Snapshots().Get(context.Background(), 9999)
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Get missing id: want shared.ErrNotFound, got %v", err)
	}
}

func TestSnapshots_List_OrdersByCapturedAtDesc(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i, when := range []time.Time{
		now.Add(-2 * time.Hour),
		now.Add(-1 * time.Hour),
		now,
	} {
		_, err := s.Snapshots().Capture(ctx, CaptureInput{
			GitRef:     "ref",
			IndexJSON:  `{"i":` + itoa(i) + `}`,
			CapturedAt: when,
		})
		if err != nil {
			t.Fatalf("Capture %d: %v", i, err)
		}
	}

	rows, err := s.Snapshots().List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	for i := 0; i < len(rows)-1; i++ {
		if !rows[i].CapturedAt.After(rows[i+1].CapturedAt) {
			t.Errorf("rows[%d].CapturedAt (%v) not after rows[%d].CapturedAt (%v)",
				i, rows[i].CapturedAt, i+1, rows[i+1].CapturedAt)
		}
	}
}

func TestSnapshots_List_FilterByGitRef(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, ref := range []string{"a", "a", "b"} {
		if _, err := s.Snapshots().Capture(ctx, CaptureInput{
			GitRef:    ref,
			IndexJSON: `{}`,
		}); err != nil {
			t.Fatalf("Capture: %v", err)
		}
	}
	aRows, err := s.Snapshots().List(ctx, "a")
	if err != nil {
		t.Fatalf("List a: %v", err)
	}
	if len(aRows) != 2 {
		t.Errorf("len(aRows) = %d, want 2", len(aRows))
	}
	bRows, err := s.Snapshots().List(ctx, "b")
	if err != nil {
		t.Fatalf("List b: %v", err)
	}
	if len(bRows) != 1 {
		t.Errorf("len(bRows) = %d, want 1", len(bRows))
	}
}

func TestSnapshots_Delete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, err := s.Snapshots().Capture(ctx, CaptureInput{
		GitRef:    "abc",
		IndexJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if err := s.Snapshots().Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Snapshots().Delete(ctx, id); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Delete missing: want shared.ErrNotFound, got %v", err)
	}
}

// itoa is a tiny helper to inline-stringify ints in JSON bodies without
// pulling in strconv on the test path. Keeping it local because the file
// only needs single-digit values for the order check.
func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return strings.Repeat("9", i/9)
}
