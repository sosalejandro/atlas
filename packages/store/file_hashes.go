package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

// FileHashRow is one row of the `file_hashes` table (docs/schema-v1.md §5.7).
// Mirrors codeindex.FileHash with the persistence-only LastScanned timestamp.
type FileHashRow struct {
	FilePath     string    `json:"file_path"`
	ContentHash  string    `json:"content_hash"`
	ModTime      time.Time `json:"mtime"`
	LastScanned  time.Time `json:"last_scanned"`
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
func (s *Store) FileHashes() FileHashes { return &fileHashesStore{db: s} }

type fileHashesStore struct{ db *Store }

const fileHashesSelectCols = `file_path, content_hash, mtime, last_scanned`

func scanFileHashRow(row interface{ Scan(...any) error }) (FileHashRow, error) {
	var r FileHashRow
	if err := row.Scan(&r.FilePath, &r.ContentHash, &r.ModTime, &r.LastScanned); err != nil {
		return FileHashRow{}, err
	}
	return r, nil
}

func (h *fileHashesStore) Get(ctx context.Context, filePath string) (FileHashRow, error) {
	row := h.db.sqlDB().QueryRowContext(ctx,
		`SELECT `+fileHashesSelectCols+` FROM file_hashes WHERE file_path = ?`, filePath)
	r, err := scanFileHashRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FileHashRow{}, shared.ErrNotFound
	}
	if err != nil {
		return FileHashRow{}, fmt.Errorf("file_hashes get %q: %w", filePath, err)
	}
	return r, nil
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

	_, err := h.db.sqlDB().ExecContext(ctx, `
		INSERT INTO file_hashes (file_path, content_hash, mtime, last_scanned)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
		  content_hash = excluded.content_hash,
		  mtime        = excluded.mtime,
		  last_scanned = excluded.last_scanned
	`, row.FilePath, row.ContentHash, mtime, lastScanned)
	if err != nil {
		return fmt.Errorf("file_hashes upsert %q: %w", row.FilePath, err)
	}
	return nil
}

func (h *fileHashesStore) List(ctx context.Context) ([]FileHashRow, error) {
	rows, err := h.db.sqlDB().QueryContext(ctx,
		`SELECT `+fileHashesSelectCols+` FROM file_hashes ORDER BY file_path`)
	if err != nil {
		return nil, fmt.Errorf("file_hashes list: %w", err)
	}
	defer rows.Close()

	var out []FileHashRow
	for rows.Next() {
		r, err := scanFileHashRow(rows)
		if err != nil {
			return nil, fmt.Errorf("file_hashes scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (h *fileHashesStore) Delete(ctx context.Context, filePath string) error {
	_, err := h.db.sqlDB().ExecContext(ctx, `DELETE FROM file_hashes WHERE file_path = ?`, filePath)
	if err != nil {
		return fmt.Errorf("file_hashes delete %q: %w", filePath, err)
	}
	return nil
}
