package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// FileHashRow is one row of the `file_hashes` table (docs/schema-v1.md §5.7).
// Mirrors codeindex.FileHash with the persistence-only LastScanned timestamp.
type FileHashRow struct {
	FilePath    string    `json:"file_path"`
	ContentHash string    `json:"content_hash"`
	ModTime     time.Time `json:"mtime"`
	LastScanned time.Time `json:"last_scanned"`
}

// FileHashes is the narrow port for the `file_hashes` table.
type FileHashes interface {
	// Get returns the current hash row for filePath, or shared.ErrNotFound.
	Get(ctx context.Context, filePath string) (FileHashRow, error)

	// Upsert inserts or refreshes the hash row. last_scanned is bumped to
	// CURRENT_TIMESTAMP if the caller does not provide one.
	Upsert(ctx context.Context, row FileHashRow) error

	// List returns every row, ordered by file_path. Used by the incremental
	// scanner to compute the diff between disk + cache.
	List(ctx context.Context) ([]FileHashRow, error)

	// Delete removes the row for filePath. No-op if absent.
	Delete(ctx context.Context, filePath string) error
}

var _ FileHashes = (*fileHashesStore)(nil)

// FileHashes returns the Store's FileHashes port.
func (s *Store) FileHashes() FileHashes { return &fileHashesStore{q: s.queries()} }

type fileHashesStore struct{ q *sqlc.Queries }

func fromSQLCFileHash(r sqlc.FileHash) FileHashRow {
	return FileHashRow{
		FilePath:    r.FilePath,
		ContentHash: r.ContentHash,
		ModTime:     r.Mtime,
		LastScanned: r.LastScanned,
	}
}

func (h *fileHashesStore) Get(ctx context.Context, filePath string) (FileHashRow, error) {
	row, err := h.q.GetFileHash(ctx, filePath)
	if errors.Is(err, sql.ErrNoRows) {
		return FileHashRow{}, shared.ErrNotFound
	}
	if err != nil {
		return FileHashRow{}, fmt.Errorf("file_hashes get %q: %w", filePath, err)
	}
	return fromSQLCFileHash(row), nil
}

func (h *fileHashesStore) Upsert(ctx context.Context, row FileHashRow) error {
	if row.FilePath == "" {
		return fmt.Errorf("file_hashes upsert: file_path required")
	}
	if row.ContentHash == "" {
		return fmt.Errorf("file_hashes upsert %q: content_hash required", row.FilePath)
	}
	mtime := row.ModTime
	if mtime.IsZero() {
		mtime = time.Now().UTC()
	}
	lastScanned := row.LastScanned
	if lastScanned.IsZero() {
		lastScanned = time.Now().UTC()
	}

	err := h.q.UpsertFileHash(ctx, sqlc.UpsertFileHashParams{
		FilePath:    row.FilePath,
		ContentHash: row.ContentHash,
		Mtime:       mtime,
		LastScanned: lastScanned,
	})
	if err != nil {
		return fmt.Errorf("file_hashes upsert %q: %w", row.FilePath, err)
	}
	return nil
}

func (h *fileHashesStore) List(ctx context.Context) ([]FileHashRow, error) {
	rows, err := h.q.ListFileHashes(ctx)
	if err != nil {
		return nil, fmt.Errorf("file_hashes list: %w", err)
	}
	out := make([]FileHashRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCFileHash(r))
	}
	return out, nil
}

func (h *fileHashesStore) Delete(ctx context.Context, filePath string) error {
	if err := h.q.DeleteFileHash(ctx, filePath); err != nil {
		return fmt.Errorf("file_hashes delete %q: %w", filePath, err)
	}
	return nil
}
