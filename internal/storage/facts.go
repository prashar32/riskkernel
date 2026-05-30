package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Fact is one episodic memory key/value.
type Fact struct {
	Namespace string
	Key       string
	Value     string
	RunID     string
	UpdatedAt time.Time
}

// PutFact inserts or updates an episodic fact by (namespace, key).
func (s *SQLite) PutFact(ctx context.Context, f Fact) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memory_facts (namespace, key, value, run_id, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(namespace, key) DO UPDATE SET
			value=excluded.value, run_id=excluded.run_id, updated_at=excluded.updated_at`,
		f.Namespace, f.Key, f.Value, nullStr(f.RunID), fmtTime(f.UpdatedAt))
	if err != nil {
		return fmt.Errorf("storage: put fact: %w", err)
	}
	return nil
}

// GetFact returns a fact by (namespace, key), or ErrNotFound.
func (s *SQLite) GetFact(ctx context.Context, namespace, key string) (Fact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT namespace, key, value, run_id, updated_at FROM memory_facts WHERE namespace = ? AND key = ?`,
		namespace, key)
	f, err := scanFact(row)
	if err == sql.ErrNoRows {
		return Fact{}, ErrNotFound
	}
	return f, err
}

// ListFacts returns all facts in a namespace, key order.
func (s *SQLite) ListFacts(ctx context.Context, namespace string) ([]Fact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT namespace, key, value, run_id, updated_at FROM memory_facts WHERE namespace = ? ORDER BY key`,
		namespace)
	if err != nil {
		return nil, fmt.Errorf("storage: list facts: %w", err)
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanFact(row rowScanner) (Fact, error) {
	var f Fact
	var runID sql.NullString
	var updated string
	if err := row.Scan(&f.Namespace, &f.Key, &f.Value, &runID, &updated); err != nil {
		return Fact{}, err
	}
	f.RunID = runID.String
	f.UpdatedAt = parseTime(updated)
	return f, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
