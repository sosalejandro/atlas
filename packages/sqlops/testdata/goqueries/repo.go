// Package repo is a fixture for the Go SQL extractor. It lives under
// testdata/ so the Go tool never builds it; every function here exists to pin
// one extraction behaviour.
package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const listUsersSQL = `SELECT id, email FROM users WHERE tenant_id = $1`

type UserRepo struct{ db *sql.DB }

// ListUsers scans into a slice with no LIMIT -- the unbounded read.
func (r *UserRepo) ListUsers(ctx context.Context, tenant int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, listUsersSQL, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetUser is a single-row read; nothing to advise about.
func (r *UserRepo) GetUser(ctx context.Context, id int64) (string, error) {
	var email string
	err := r.db.QueryRowContext(ctx, "SELECT email FROM users WHERE id = $1", id).Scan(&email)
	return email, err
}

// Page paginates by OFFSET with a caller-supplied value and no ORDER BY.
func (r *UserRepo) Page(ctx context.Context, limit, offset int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM events LIMIT $1 OFFSET $2", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out, nil
}

// SearchUsers splices a caller-supplied column name into the query text.
func (r *UserRepo) SearchUsers(ctx context.Context, column string) ([]string, error) {
	q := "SELECT id FROM users WHERE " + column + " IS NOT NULL"
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out, nil
}

// CountByTable formats the table name into the query with Sprintf.
func (r *UserRepo) CountByTable(ctx context.Context, table string) (int, error) {
	var n int
	q := fmt.Sprintf("SELECT count(*) FROM %s", table)
	err := r.db.QueryRowContext(ctx, q).Scan(&n)
	return n, err
}

// ExpandIn uses Sprintf only to widen a placeholder list -- no caller value
// reaches the SQL text, so the injection check must stay quiet.
func (r *UserRepo) ExpandIn(ctx context.Context, ids []int64) error {
	q := fmt.Sprintf("DELETE FROM sessions WHERE id IN (%s)",
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","))
	_, err := r.db.ExecContext(ctx, q)
	return err
}

// Dynamic hands over a query built somewhere Atlas cannot see.
func (r *UserRepo) Dynamic(ctx context.Context, b *builder) error {
	_, err := r.db.ExecContext(ctx, b.SQL())
	return err
}

// Touch is a write.
func (r *UserRepo) Touch(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE users SET seen_at = now() WHERE id = $1", id)
	return err
}

// AllTenants is knowingly unbounded; the directive says so.
// atlas:sql-ignore sql.unbounded-list
func (r *UserRepo) AllTenants(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM tenants")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out, nil
}

type builder struct{}

func (b *builder) SQL() string { return "" }

type cache struct{}

func (c *cache) Query(k string) string { return k }

// notADatabase is the false-positive guard: a Query method on something that
// is not a database, passed something that is not SQL.
func notADatabase(c *cache) string { return c.Query("something") }
