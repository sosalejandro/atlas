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
func (s *Store) Config() Config { return &configStore{q: s.queries()} }

type configStore struct{ q *sqlc.Queries }

func (c *configStore) Get(ctx context.Context, key string) (string, error) {
	v, err := c.q.GetConfig(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", shared.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("config get %q: %w", key, err)
	}
	return v, nil
}

func (c *configStore) Set(ctx context.Context, key, value string) error {
	if err := c.q.SetConfig(ctx, sqlc.SetConfigParams{Key: key, Value: value}); err != nil {
		return fmt.Errorf("config set %q: %w", key, err)
	}
	return nil
}

func (c *configStore) All(ctx context.Context) ([]ConfigEntry, error) {
	rows, err := c.q.ListConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("config all: %w", err)
	}
	out := make([]ConfigEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, ConfigEntry{Key: r.Key, Value: r.Value, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

func (c *configStore) Delete(ctx context.Context, key string) error {
	if err := c.q.DeleteConfig(ctx, key); err != nil {
		return fmt.Errorf("config delete %q: %w", key, err)
	}
	return nil
}
