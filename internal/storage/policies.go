package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// UpsertPolicy inserts or replaces a named policy bundle. On an update (same name)
// the original created_at is preserved.
func (s *SQLite) UpsertPolicy(ctx context.Context, p PolicyRecord) error {
	allow, err := json.Marshal(orEmptyStrings(p.ToolAllowlist))
	if err != nil {
		return fmt.Errorf("storage: marshal policy allowlist: %w", err)
	}
	rules, err := json.Marshal(orEmptyRules(p.ApprovalRules))
	if err != nil {
		return fmt.Errorf("storage: marshal policy rules: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO policies (name, budget_tokens, budget_dollars, budget_loops, budget_seconds, tool_allowlist, approval_rules, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			budget_tokens=excluded.budget_tokens, budget_dollars=excluded.budget_dollars,
			budget_loops=excluded.budget_loops, budget_seconds=excluded.budget_seconds,
			tool_allowlist=excluded.tool_allowlist, approval_rules=excluded.approval_rules,
			updated_at=excluded.updated_at`,
		p.Name, p.BudgetTokens, p.BudgetDollars, p.BudgetLoops, p.BudgetSeconds,
		string(allow), string(rules), fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("storage: upsert policy: %w", err)
	}
	return nil
}

// GetPolicy returns a policy bundle by name, or ErrNotFound.
func (s *SQLite) GetPolicy(ctx context.Context, name string) (PolicyRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, budget_tokens, budget_dollars, budget_loops, budget_seconds, tool_allowlist, approval_rules, created_at, updated_at
		FROM policies WHERE name = ?`, name)
	p, err := scanPolicy(row)
	if err == sql.ErrNoRows {
		return PolicyRecord{}, ErrNotFound
	}
	return p, err
}

// ListPolicies returns all policy bundles, newest first.
func (s *SQLite) ListPolicies(ctx context.Context) ([]PolicyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, budget_tokens, budget_dollars, budget_loops, budget_seconds, tool_allowlist, approval_rules, created_at, updated_at
		FROM policies ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list policies: %w", err)
	}
	defer rows.Close()
	var out []PolicyRecord
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPolicy(row rowScanner) (PolicyRecord, error) {
	var p PolicyRecord
	var allow, rules, created, updated string
	if err := row.Scan(&p.Name, &p.BudgetTokens, &p.BudgetDollars, &p.BudgetLoops, &p.BudgetSeconds,
		&allow, &rules, &created, &updated); err != nil {
		return PolicyRecord{}, err
	}
	_ = json.Unmarshal([]byte(allow), &p.ToolAllowlist)
	_ = json.Unmarshal([]byte(rules), &p.ApprovalRules)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyRules(r []ApprovalRule) []ApprovalRule {
	if r == nil {
		return []ApprovalRule{}
	}
	return r
}
