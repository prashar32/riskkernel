package storage

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for Postgres
	"github.com/pressly/goose/v3"
)

//go:embed migrations/postgres/*.sql
var pgMigrationsFS embed.FS

const pgMigrationsDir = "migrations/postgres"

// Postgres is the opt-in Store backend for multi-instance / HA deployments. It
// implements the same Store interface as the default SQLite backend against the
// same schema (timestamps as RFC3339 text, JSON marshaled in Go), so every
// package-level scan/marshal helper is shared — only the SQL dialect (placeholder
// style, the metadata JSON accessor) and the DDL differ. SQLite stays the default;
// Postgres is selected only when a connection string is configured.
type Postgres struct {
	db *sql.DB
}

// OpenPostgres connects to Postgres using a standard libpq/pgx connection string
// (e.g. postgres://user:pass@host:5432/db?sslmode=require), applies pending
// forward migrations in a transaction, and enforces downgrade protection.
func OpenPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open postgres: %w", err)
	}
	// Postgres handles concurrent writers — keep a modest, bounded pool.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: ping postgres: %w", err)
	}

	p := &Postgres{db: db}
	if err := p.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return p, nil
}

// migrate applies embedded forward migrations after enforcing that the on-disk
// schema is not newer than this binary understands (downgrade protection).
func (p *Postgres) migrate() error {
	goose.SetBaseFS(pgMigrationsFS)
	goose.SetLogger(quietGooseLogger{})
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("storage: set goose dialect: %w", err)
	}

	maxKnown, err := maxMigrationVersionFS(pgMigrationsFS, pgMigrationsDir)
	if err != nil {
		return err
	}
	current, err := goose.GetDBVersion(p.db)
	if err != nil {
		return fmt.Errorf("storage: read schema version: %w", err)
	}
	if current > maxKnown {
		return fmt.Errorf("%w (on-disk v%d > binary v%d)", ErrSchemaTooNew, current, maxKnown)
	}
	if err := goose.Up(p.db, pgMigrationsDir); err != nil {
		return fmt.Errorf("storage: apply migrations: %w", err)
	}
	return nil
}

// maxMigrationVersionFS returns the highest migration version in the given
// embedded directory (the numeric prefix of each filename).
func maxMigrationVersionFS(fsys fs.FS, dir string) (int64, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("storage: read embedded migrations: %w", err)
	}
	var max int64
	for _, e := range entries {
		name := path.Base(e.Name())
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(name, "_")
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			continue
		}
		if v > max {
			max = v
		}
	}
	return max, nil
}

// Close closes the connection pool.
func (p *Postgres) Close() error { return p.db.Close() }

// --- runs ---

func (p *Postgres) UpsertRun(ctx context.Context, r RunRecord) error {
	meta, err := marshalMeta(r.Metadata)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO runs (id, name, status, halt_reason,
			budget_tokens, budget_dollars, budget_loops, budget_seconds,
			usage_prompt_tokens, usage_completion_tokens, usage_dollars, usage_loops,
			metadata, created_at, updated_at, policy_ref)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, status=excluded.status, halt_reason=excluded.halt_reason,
			budget_tokens=excluded.budget_tokens, budget_dollars=excluded.budget_dollars,
			budget_loops=excluded.budget_loops, budget_seconds=excluded.budget_seconds,
			usage_prompt_tokens=excluded.usage_prompt_tokens,
			usage_completion_tokens=excluded.usage_completion_tokens,
			usage_dollars=excluded.usage_dollars, usage_loops=excluded.usage_loops,
			metadata=excluded.metadata, updated_at=excluded.updated_at`,
		r.ID, r.Name, r.Status, r.HaltReason,
		r.BudgetTokens, r.BudgetDollars, r.BudgetLoops, r.BudgetSeconds,
		r.UsagePromptTokens, r.UsageCompletionTokens, r.UsageDollars, r.UsageLoops,
		meta, fmtTime(r.CreatedAt), fmtTime(r.UpdatedAt), r.PolicyRef)
	if err != nil {
		return fmt.Errorf("storage: upsert run: %w", err)
	}
	return nil
}

func (p *Postgres) GetRun(ctx context.Context, id string) (RunRecord, error) {
	row := p.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = $1`, id)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return RunRecord{}, ErrNotFound
	}
	return r, err
}

func (p *Postgres) ListRuns(ctx context.Context) ([]RunRecord, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list runs: %w", err)
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

func (p *Postgres) ListRunsByStatus(ctx context.Context, status string) ([]RunRecord, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE status = $1 ORDER BY created_at DESC`, status)
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

// --- steps ---

func (p *Postgres) UpsertStep(ctx context.Context, st StepRecord) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO steps (run_id, idx, status, prompt_tokens, completion_tokens, dollars, started_at, ended_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(run_id, idx) DO UPDATE SET
			status=excluded.status, prompt_tokens=excluded.prompt_tokens,
			completion_tokens=excluded.completion_tokens, dollars=excluded.dollars,
			ended_at=excluded.ended_at`,
		st.RunID, st.Index, st.Status, st.PromptTokens, st.CompletionTokens, st.Dollars,
		fmtTime(st.StartedAt), fmtTimePtr(st.EndedAt))
	if err != nil {
		return fmt.Errorf("storage: upsert step: %w", err)
	}
	return nil
}

func (p *Postgres) ListSteps(ctx context.Context, runID string) ([]StepRecord, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT run_id, idx, status, prompt_tokens, completion_tokens, dollars, started_at, ended_at
		FROM steps WHERE run_id = $1 ORDER BY idx`, runID)
	if err != nil {
		return nil, fmt.Errorf("storage: list steps: %w", err)
	}
	defer rows.Close()
	var out []StepRecord
	for rows.Next() {
		var st StepRecord
		var started string
		var ended sql.NullString
		if err := rows.Scan(&st.RunID, &st.Index, &st.Status, &st.PromptTokens,
			&st.CompletionTokens, &st.Dollars, &started, &ended); err != nil {
			return nil, err
		}
		st.StartedAt = parseTime(started)
		if ended.Valid && ended.String != "" {
			t := parseTime(ended.String)
			st.EndedAt = &t
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// --- ledger ---

func (p *Postgres) AppendLedger(ctx context.Context, e LedgerEntry) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO cost_ledger (run_id, step_idx, provider, model,
			prompt_tokens, completion_tokens, dollars, priced, response_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		e.RunID, e.StepIndex, e.Provider, e.Model,
		e.PromptTokens, e.CompletionTokens, e.Dollars, boolToInt(e.Priced), e.ResponseID,
		fmtTime(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: append ledger: %w", err)
	}
	return nil
}

func (p *Postgres) LedgerForRun(ctx context.Context, runID string) ([]LedgerEntry, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT run_id, step_idx, provider, model, prompt_tokens, completion_tokens,
			dollars, priced, response_id, created_at
		FROM cost_ledger WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("storage: ledger for run: %w", err)
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		var priced int
		var created string
		if err := rows.Scan(&e.RunID, &e.StepIndex, &e.Provider, &e.Model,
			&e.PromptTokens, &e.CompletionTokens, &e.Dollars, &priced, &e.ResponseID, &created); err != nil {
			return nil, err
		}
		e.Priced = priced != 0
		e.CreatedAt = parseTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *Postgres) Totals(ctx context.Context, runID string) (LedgerTotals, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
			COALESCE(SUM(dollars),0)
		FROM cost_ledger WHERE run_id = $1`, runID)
	t := LedgerTotals{RunID: runID}
	if err := row.Scan(&t.Calls, &t.PromptTokens, &t.CompletionTokens, &t.Dollars); err != nil {
		return LedgerTotals{}, fmt.Errorf("storage: totals: %w", err)
	}
	return t, nil
}

// SummarizeLedger mirrors the SQLite aggregation. The grouping expression is chosen
// from a fixed whitelist (structural, not a bound parameter); the metadata key is
// validated and bound as a value, so nothing is built from raw user input unsafely.
// The only dialect difference from SQLite is the metadata accessor: Postgres reads
// the JSON-in-text column via `metadata::jsonb ->> key` instead of json_extract.
func (p *Postgres) SummarizeLedger(ctx context.Context, opts SummarizeOptions) (UsageSummary, error) {
	var groupExpr, metaArg string
	needRuns := false
	n := 0 // next positional placeholder index
	next := func() string { n++; return "$" + strconv.Itoa(n) }

	switch {
	case opts.By == "provider":
		groupExpr = "l.provider"
	case opts.By == "model":
		groupExpr = "l.model"
	case opts.By == "day":
		groupExpr = "substr(l.created_at, 1, 10)" // RFC3339 date prefix (UTC)
	case opts.By == "name":
		groupExpr, needRuns = "r.name", true
	case strings.HasPrefix(opts.By, "metadata."):
		key := strings.TrimPrefix(opts.By, "metadata.")
		if !validMetaKey(key) {
			return UsageSummary{}, fmt.Errorf("storage: invalid metadata key %q", key)
		}
		groupExpr, metaArg, needRuns = "(r.metadata::jsonb ->> "+next()+")", key, true
	default:
		return UsageSummary{}, fmt.Errorf("storage: unsupported group dimension %q", opts.By)
	}

	from := "cost_ledger l"
	if needRuns {
		from += " JOIN runs r ON r.id = l.run_id"
	}

	var args []any
	if metaArg != "" { // the metadata key is the first placeholder, in SELECT
		args = append(args, metaArg)
	}
	var conds []string
	if opts.Since != nil {
		conds = append(conds, "l.created_at >= "+next())
		args = append(args, fmtTime(*opts.Since))
	}
	if opts.Until != nil {
		conds = append(conds, "l.created_at < "+next())
		args = append(args, fmtTime(*opts.Until))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT COALESCE(%s, '(none)') AS k,
			COUNT(*), COALESCE(SUM(l.prompt_tokens), 0), COALESCE(SUM(l.completion_tokens), 0),
			COALESCE(SUM(l.dollars), 0)
		FROM %s%s
		GROUP BY k
		ORDER BY SUM(l.dollars) DESC, k ASC`, groupExpr, from, where)

	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return UsageSummary{}, fmt.Errorf("storage: summarize ledger: %w", err)
	}
	defer rows.Close()

	out := UsageSummary{By: opts.By, Groups: []UsageGroup{}, Total: UsageGroup{Key: "total"}}
	for rows.Next() {
		var g UsageGroup
		if err := rows.Scan(&g.Key, &g.Calls, &g.PromptTokens, &g.CompletionTokens, &g.Dollars); err != nil {
			return UsageSummary{}, err
		}
		out.Groups = append(out.Groups, g)
		out.Total.Calls += g.Calls
		out.Total.PromptTokens += g.PromptTokens
		out.Total.CompletionTokens += g.CompletionTokens
		out.Total.Dollars += g.Dollars
	}
	return out, rows.Err()
}

// --- tool calls ---

func (p *Postgres) AppendToolCall(ctx context.Context, t ToolCallRecord) error {
	args, err := json.Marshal(orEmptyMap(t.Arguments))
	if err != nil {
		return fmt.Errorf("storage: marshal tool args: %w", err)
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO tool_calls (id, run_id, step_idx, tool, side_effect, arguments, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.RunID, t.StepIndex, t.Tool, t.SideEffect, string(args), t.Status, fmtTime(t.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: append tool call: %w", err)
	}
	return nil
}

func (p *Postgres) ListToolCalls(ctx context.Context, runID string) ([]ToolCallRecord, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, run_id, step_idx, tool, side_effect, arguments, status, created_at
		FROM tool_calls WHERE run_id = $1 ORDER BY step_idx, created_at, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("storage: list tool calls: %w", err)
	}
	defer rows.Close()
	var out []ToolCallRecord
	for rows.Next() {
		var t ToolCallRecord
		var args, created string
		if err := rows.Scan(&t.ID, &t.RunID, &t.StepIndex, &t.Tool,
			&t.SideEffect, &args, &t.Status, &created); err != nil {
			return nil, err
		}
		t.Arguments = unmarshalArgs(args)
		t.CreatedAt = parseTime(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- memory facts ---

func (p *Postgres) PutFact(ctx context.Context, f Fact) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO memory_facts (namespace, key, value, run_id, updated_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT(namespace, key) DO UPDATE SET
			value=excluded.value, run_id=excluded.run_id, updated_at=excluded.updated_at`,
		f.Namespace, f.Key, f.Value, nullStr(f.RunID), fmtTime(f.UpdatedAt))
	if err != nil {
		return fmt.Errorf("storage: put fact: %w", err)
	}
	return nil
}

func (p *Postgres) GetFact(ctx context.Context, namespace, key string) (Fact, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT namespace, key, value, run_id, updated_at FROM memory_facts WHERE namespace = $1 AND key = $2`,
		namespace, key)
	f, err := scanFact(row)
	if err == sql.ErrNoRows {
		return Fact{}, ErrNotFound
	}
	return f, err
}

func (p *Postgres) ListFacts(ctx context.Context, namespace string) ([]Fact, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT namespace, key, value, run_id, updated_at FROM memory_facts WHERE namespace = $1 ORDER BY key`,
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

// --- approvals ---

func (p *Postgres) CreateApproval(ctx context.Context, a ApprovalRecord) error {
	args, err := json.Marshal(orEmptyMapAny(a.Arguments))
	if err != nil {
		return fmt.Errorf("storage: marshal approval args: %w", err)
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO approvals (id, run_id, step_idx, tool, side_effect, arguments, status, reason, decided_by, created_at, decided_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		a.ID, a.RunID, a.StepIndex, a.Tool, a.SideEffect, string(args),
		a.Status, a.Reason, a.DecidedBy, fmtTime(a.CreatedAt), fmtTimePtr(a.DecidedAt))
	if err != nil {
		return fmt.Errorf("storage: create approval: %w", err)
	}
	return nil
}

func (p *Postgres) GetApproval(ctx context.Context, id string) (ApprovalRecord, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT id, run_id, step_idx, tool, side_effect, arguments, status, reason, decided_by, created_at, decided_at
		FROM approvals WHERE id = $1`, id)
	a, err := scanApproval(row)
	if err == sql.ErrNoRows {
		return ApprovalRecord{}, ErrNotFound
	}
	return a, err
}

func (p *Postgres) ResolveApproval(ctx context.Context, id, status, reason, decidedBy string, decidedAt time.Time) error {
	res, err := p.db.ExecContext(ctx, `
		UPDATE approvals SET status = $1, reason = $2, decided_by = $3, decided_at = $4
		WHERE id = $5 AND status = $6`,
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

func (p *Postgres) ListApprovals(ctx context.Context, status string) ([]ApprovalRecord, error) {
	q := `SELECT id, run_id, step_idx, tool, side_effect, arguments, status, reason, decided_by, created_at, decided_at
		FROM approvals`
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = p.db.QueryContext(ctx, q+` ORDER BY created_at DESC`)
	} else {
		rows, err = p.db.QueryContext(ctx, q+` WHERE status = $1 ORDER BY created_at DESC`, status)
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

// --- policies ---

func (p *Postgres) UpsertPolicy(ctx context.Context, pol PolicyRecord) error {
	allow, err := json.Marshal(orEmptyStrings(pol.ToolAllowlist))
	if err != nil {
		return fmt.Errorf("storage: marshal policy allowlist: %w", err)
	}
	rules, err := json.Marshal(orEmptyRules(pol.ApprovalRules))
	if err != nil {
		return fmt.Errorf("storage: marshal policy rules: %w", err)
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO policies (name, budget_tokens, budget_dollars, budget_loops, budget_seconds, tool_allowlist, approval_rules, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(name) DO UPDATE SET
			budget_tokens=excluded.budget_tokens, budget_dollars=excluded.budget_dollars,
			budget_loops=excluded.budget_loops, budget_seconds=excluded.budget_seconds,
			tool_allowlist=excluded.tool_allowlist, approval_rules=excluded.approval_rules,
			updated_at=excluded.updated_at`,
		pol.Name, pol.BudgetTokens, pol.BudgetDollars, pol.BudgetLoops, pol.BudgetSeconds,
		string(allow), string(rules), fmtTime(pol.CreatedAt), fmtTime(pol.UpdatedAt))
	if err != nil {
		return fmt.Errorf("storage: upsert policy: %w", err)
	}
	return nil
}

func (p *Postgres) GetPolicy(ctx context.Context, name string) (PolicyRecord, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT name, budget_tokens, budget_dollars, budget_loops, budget_seconds, tool_allowlist, approval_rules, created_at, updated_at
		FROM policies WHERE name = $1`, name)
	pol, err := scanPolicy(row)
	if err == sql.ErrNoRows {
		return PolicyRecord{}, ErrNotFound
	}
	return pol, err
}

func (p *Postgres) ListPolicies(ctx context.Context) ([]PolicyRecord, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT name, budget_tokens, budget_dollars, budget_loops, budget_seconds, tool_allowlist, approval_rules, created_at, updated_at
		FROM policies ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list policies: %w", err)
	}
	defer rows.Close()
	var out []PolicyRecord
	for rows.Next() {
		pol, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pol)
	}
	return out, rows.Err()
}

// --- checkpoints ---

func (p *Postgres) SaveCheckpoint(ctx context.Context, c CheckpointRecord) error {
	payload, err := json.Marshal(orEmptyMapAny(c.Payload))
	if err != nil {
		return fmt.Errorf("storage: marshal checkpoint payload: %w", err)
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO checkpoints (run_id, step_idx, name,
			usage_prompt_tokens, usage_completion_tokens, usage_dollars, usage_loops,
			payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.RunID, c.StepIndex, c.Name,
		c.UsagePromptTokens, c.UsageCompletionTokens, c.UsageDollars, c.UsageLoops,
		string(payload), fmtTime(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: save checkpoint: %w", err)
	}
	return nil
}

func (p *Postgres) LatestCheckpoint(ctx context.Context, runID string) (CheckpointRecord, error) {
	row := p.db.QueryRowContext(ctx, `
		SELECT run_id, step_idx, name, usage_prompt_tokens, usage_completion_tokens,
			usage_dollars, usage_loops, payload, created_at
		FROM checkpoints WHERE run_id = $1 ORDER BY id DESC LIMIT 1`, runID)
	c, err := scanCheckpoint(row)
	if err == sql.ErrNoRows {
		return CheckpointRecord{}, ErrNotFound
	}
	return c, err
}

func (p *Postgres) ListCheckpoints(ctx context.Context, runID string) ([]CheckpointRecord, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT run_id, step_idx, name, usage_prompt_tokens, usage_completion_tokens,
			usage_dollars, usage_loops, payload, created_at
		FROM checkpoints WHERE run_id = $1 ORDER BY id`, runID)
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
