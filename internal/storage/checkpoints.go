package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const runColumns = `id, name, status, halt_reason,
	budget_tokens, budget_dollars, budget_loops, budget_seconds,
	usage_prompt_tokens, usage_completion_tokens, usage_dollars, usage_loops,
	metadata, created_at, updated_at, policy_ref`

// ListRunsByStatus returns runs in the given lifecycle status, newest first.
func (s *SQLite) ListRunsByStatus(ctx context.Context, status string) ([]RunRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE status = ? ORDER BY created_at DESC`, status)
	if err != nil {
		return nil, fmt.Errorf("storage: list runs by status: %w", err)
	}
	defer rows.Close()
	var out []RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveCheckpoint appends a crash-resumable checkpoint.
func (s *SQLite) SaveCheckpoint(ctx context.Context, c CheckpointRecord) error {
	payload, err := json.Marshal(orEmptyMapAny(c.Payload))
	if err != nil {
		return fmt.Errorf("storage: marshal checkpoint payload: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO checkpoints (run_id, step_idx, name,
			usage_prompt_tokens, usage_completion_tokens, usage_dollars, usage_loops,
			payload, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		c.RunID, c.StepIndex, c.Name,
		c.UsagePromptTokens, c.UsageCompletionTokens, c.UsageDollars, c.UsageLoops,
		string(payload), fmtTime(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: save checkpoint: %w", err)
	}
	return nil
}

// LatestCheckpoint returns a run's most recent checkpoint, or ErrNotFound.
func (s *SQLite) LatestCheckpoint(ctx context.Context, runID string) (CheckpointRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, step_idx, name, usage_prompt_tokens, usage_completion_tokens,
			usage_dollars, usage_loops, payload, created_at
		FROM checkpoints WHERE run_id = ? ORDER BY id DESC LIMIT 1`, runID)
	c, err := scanCheckpoint(row)
	if err == sql.ErrNoRows {
		return CheckpointRecord{}, ErrNotFound
	}
	return c, err
}

// ListCheckpoints returns a run's checkpoints in time order.
func (s *SQLite) ListCheckpoints(ctx context.Context, runID string) ([]CheckpointRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, step_idx, name, usage_prompt_tokens, usage_completion_tokens,
			usage_dollars, usage_loops, payload, created_at
		FROM checkpoints WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("storage: list checkpoints: %w", err)
	}
	defer rows.Close()
	var out []CheckpointRecord
	for rows.Next() {
		c, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCheckpoint(row rowScanner) (CheckpointRecord, error) {
	var c CheckpointRecord
	var payload, created string
	if err := row.Scan(&c.RunID, &c.StepIndex, &c.Name,
		&c.UsagePromptTokens, &c.UsageCompletionTokens, &c.UsageDollars, &c.UsageLoops,
		&payload, &created); err != nil {
		return CheckpointRecord{}, err
	}
	c.Payload = unmarshalMapAny(payload)
	c.CreatedAt = parseTime(created)
	return c, nil
}

func orEmptyMapAny(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func unmarshalMapAny(s string) map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}
