package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

// ConfigEntry is a single row from the `config` table.
type ConfigEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Config is the narrow port for the `config` key/value table
// (docs/schema-v1.md §5.2). Reserved keys are documented in the schema doc.
type Config interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	All(ctx context.Context) ([]ConfigEntry, error)
	Delete(ctx context.Context, key string) error
}

// Compile-time interface satisfaction assertion.
var _ Config = (*configStore)(nil)

// Config returns the Store's Config port.
func (s *Store) Config() Config { return &configStore{db: s} }

type configStore struct{ db *Store }

func (c *configStore) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := c.db.sqlDB().QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", shared.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("config get %q: %w", key, err)
	}
	return v, nil
}

func (c *configStore) Set(ctx context.Context, key, value string) error {
	_, err := c.db.sqlDB().ExecContext(ctx, `
		INSERT INTO config (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
		  value      = excluded.value,
		  updated_at = CURRENT_TIMESTAMP
	`, key, value)
	if err != nil {
		return fmt.Errorf("config set %q: %w", key, err)
	}
	return nil
}

func (c *configStore) All(ctx context.Context) ([]ConfigEntry, error) {
	rows, err := c.db.sqlDB().QueryContext(ctx, `SELECT key, value, updated_at FROM config ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("config all: %w", err)
	}
	defer rows.Close()

	var out []ConfigEntry
	for rows.Next() {
		var e ConfigEntry
		if err := rows.Scan(&e.Key, &e.Value, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("config scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (c *configStore) Delete(ctx context.Context, key string) error {
	_, err := c.db.sqlDB().ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("config delete %q: %w", key, err)
	}
	return nil
}
