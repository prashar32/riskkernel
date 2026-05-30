package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// CreateApproval persists a new (pending) approval request.
func (s *SQLite) CreateApproval(ctx context.Context, a ApprovalRecord) error {
	args, err := json.Marshal(orEmptyMapAny(a.Arguments))
	if err != nil {
		return fmt.Errorf("storage: marshal approval args: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO approvals (id, run_id, step_idx, tool, side_effect, arguments, status, reason, decided_by, created_at, decided_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.RunID, a.StepIndex, a.Tool, a.SideEffect, string(args),
		a.Status, a.Reason, a.DecidedBy, fmtTime(a.CreatedAt), fmtTimePtr(a.DecidedAt))
	if err != nil {
		return fmt.Errorf("storage: create approval: %w", err)
	}
	return nil
}

// GetApproval returns an approval by id, or ErrNotFound.
func (s *SQLite) GetApproval(ctx context.Context, id string) (ApprovalRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, step_idx, tool, side_effect, arguments, status, reason, decided_by, created_at, decided_at
		FROM approvals WHERE id = ?`, id)
	a, err := scanApproval(row)
	if err == sql.ErrNoRows {
		return ApprovalRecord{}, ErrNotFound
	}
	return a, err
}

// ResolveApproval records a decision on a pending approval. It is a no-op if the
// approval is not currently pending (returns ErrNotFound so callers can detect a
// double-resolve or unknown id).
func (s *SQLite) ResolveApproval(ctx context.Context, id, status, reason, decidedBy string, decidedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE approvals SET status = ?, reason = ?, decided_by = ?, decided_at = ?
		WHERE id = ? AND status = ?`,
		status, reason, decidedBy, fmtTime(decidedAt), id, ApprovalPending)
	if err != nil {
		return fmt.Errorf("storage: resolve approval: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListApprovals returns approvals filtered by status ("" = all), newest first.
func (s *SQLite) ListApprovals(ctx context.Context, status string) ([]ApprovalRecord, error) {
	q := `SELECT id, run_id, step_idx, tool, side_effect, arguments, status, reason, decided_by, created_at, decided_at
		FROM approvals`
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.QueryContext(ctx, q+` ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` WHERE status = ? ORDER BY created_at DESC`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("storage: list approvals: %w", err)
	}
	defer rows.Close()
	var out []ApprovalRecord
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanApproval(row rowScanner) (ApprovalRecord, error) {
	var a ApprovalRecord
	var args, created string
	var decided sql.NullString
	if err := row.Scan(&a.ID, &a.RunID, &a.StepIndex, &a.Tool, &a.SideEffect, &args,
		&a.Status, &a.Reason, &a.DecidedBy, &created, &decided); err != nil {
		return ApprovalRecord{}, err
	}
	a.Arguments = unmarshalMapAny(args)
	a.CreatedAt = parseTime(created)
	if decided.Valid && decided.String != "" {
		t := parseTime(decided.String)
		a.DecidedAt = &t
	}
	return a, nil
}
