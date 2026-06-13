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

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo → single static binary)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

// timeLayout is the canonical on-disk timestamp format (UTC RFC3339, ns).
const timeLayout = time.RFC3339Nano

// SQLite is the default Store backend: a single WAL-mode SQLite file the user
// owns. Pure-Go driver, so the binary stays static and cross-compilable.
type SQLite struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) the SQLite database at path, applies
// pending forward migrations in a transaction, and enforces downgrade protection.
func OpenSQLite(path string) (*SQLite, error) {
	// WAL for concurrent readers + a bounded writer; busy_timeout to ride out
	// brief lock contention; foreign_keys on for referential integrity.
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open sqlite: %w", err)
	}
	// SQLite tolerates one writer; keep the pool small and predictable.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: ping sqlite: %w", err)
	}

	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate applies embedded forward migrations after enforcing that the on-disk
// schema is not newer than this binary understands.
func (s *SQLite) migrate() error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(quietGooseLogger{}) // we log migration status via slog, not goose
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("storage: set goose dialect: %w", err)
	}

	maxKnown, err := maxMigrationVersion()
	if err != nil {
		return err
	}

	// GetDBVersion ensures the version table exists and returns 0 on a fresh DB.
	current, err := goose.GetDBVersion(s.db)
	if err != nil {
		return fmt.Errorf("storage: read schema version: %w", err)
	}
	if current > maxKnown {
		return fmt.Errorf("%w (on-disk v%d > binary v%d)", ErrSchemaTooNew, current, maxKnown)
	}

	if err := goose.Up(s.db, migrationsDir); err != nil {
		return fmt.Errorf("storage: apply migrations: %w", err)
	}
	return nil
}

// maxMigrationVersion returns the highest migration version embedded in the
// binary (the numeric prefix of each migration filename).
func maxMigrationVersion() (int64, error) {
	entries, err := fs.ReadDir(migrationsFS, migrationsDir)
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

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

// --- runs ---

func (s *SQLite) UpsertRun(ctx context.Context, r RunRecord) error {
	meta, err := marshalMeta(r.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO runs (id, name, status, halt_reason,
			budget_tokens, budget_dollars, budget_loops, budget_seconds,
			usage_prompt_tokens, usage_completion_tokens, usage_dollars, usage_loops,
			metadata, created_at, updated_at, policy_ref)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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

func (s *SQLite) GetRun(ctx context.Context, id string) (RunRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = ?`, id)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return RunRecord{}, ErrNotFound
	}
	return r, err
}

func (s *SQLite) ListRuns(ctx context.Context) ([]RunRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runColumns+` FROM runs ORDER BY created_at DESC`)
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

// --- steps ---

func (s *SQLite) UpsertStep(ctx context.Context, st StepRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO steps (run_id, idx, status, prompt_tokens, completion_tokens, dollars, started_at, ended_at)
		VALUES (?,?,?,?,?,?,?,?)
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

func (s *SQLite) ListSteps(ctx context.Context, runID string) ([]StepRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, idx, status, prompt_tokens, completion_tokens, dollars, started_at, ended_at
		FROM steps WHERE run_id = ? ORDER BY idx`, runID)
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

func (s *SQLite) AppendLedger(ctx context.Context, e LedgerEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cost_ledger (run_id, step_idx, provider, model,
			prompt_tokens, completion_tokens, dollars, priced, response_id, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.RunID, e.StepIndex, e.Provider, e.Model,
		e.PromptTokens, e.CompletionTokens, e.Dollars, boolToInt(e.Priced), e.ResponseID,
		fmtTime(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: append ledger: %w", err)
	}
	return nil
}

func (s *SQLite) LedgerForRun(ctx context.Context, runID string) ([]LedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, step_idx, provider, model, prompt_tokens, completion_tokens,
			dollars, priced, response_id, created_at
		FROM cost_ledger WHERE run_id = ? ORDER BY id`, runID)
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

func (s *SQLite) Totals(ctx context.Context, runID string) (LedgerTotals, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
			COALESCE(SUM(dollars),0)
		FROM cost_ledger WHERE run_id = ?`, runID)
	t := LedgerTotals{RunID: runID}
	if err := row.Scan(&t.Calls, &t.PromptTokens, &t.CompletionTokens, &t.Dollars); err != nil {
		return LedgerTotals{}, fmt.Errorf("storage: totals: %w", err)
	}
	return t, nil
}

// SummarizeLedger aggregates the cost ledger across runs, grouped by opts.By.
// The grouping expression is chosen from a fixed whitelist (it's structural and
// can't be a bound parameter); the metadata key is validated and its JSON path is
// bound as a value, so nothing here is built from raw user input unsafely.
func (s *SQLite) SummarizeLedger(ctx context.Context, opts SummarizeOptions) (UsageSummary, error) {
	var groupExpr, metaArg string
	needRuns := false
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
		groupExpr, metaArg, needRuns = "json_extract(r.metadata, ?)", "$."+key, true
	default:
		return UsageSummary{}, fmt.Errorf("storage: unsupported group dimension %q", opts.By)
	}

	from := "cost_ledger l"
	if needRuns {
		from += " JOIN runs r ON r.id = l.run_id"
	}

	var args []any
	if metaArg != "" { // the json_extract path is the first placeholder, in SELECT
		args = append(args, metaArg)
	}
	var conds []string
	if opts.Since != nil {
		conds = append(conds, "l.created_at >= ?")
		args = append(args, fmtTime(*opts.Since))
	}
	if opts.Until != nil {
		conds = append(conds, "l.created_at < ?")
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

	rows, err := s.db.QueryContext(ctx, q, args...)
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

// validMetaKey allows only flat, identifier-like metadata keys (no JSON-path
// metacharacters), so the bound "$.<key>" path can't be abused.
func validMetaKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// --- tool calls ---

func (s *SQLite) AppendToolCall(ctx context.Context, t ToolCallRecord) error {
	args, err := json.Marshal(orEmptyMap(t.Arguments))
	if err != nil {
		return fmt.Errorf("storage: marshal tool args: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tool_calls (id, run_id, step_idx, tool, side_effect, arguments, status, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		t.ID, t.RunID, t.StepIndex, t.Tool, t.SideEffect, string(args), t.Status, fmtTime(t.CreatedAt))
	if err != nil {
		return fmt.Errorf("storage: append tool call: %w", err)
	}
	return nil
}

func (s *SQLite) ListToolCalls(ctx context.Context, runID string) ([]ToolCallRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, step_idx, tool, side_effect, arguments, status, created_at
		FROM tool_calls WHERE run_id = ? ORDER BY step_idx, created_at, id`, runID)
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

// --- helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (RunRecord, error) {
	var r RunRecord
	var meta, created, updated string
	if err := row.Scan(&r.ID, &r.Name, &r.Status, &r.HaltReason,
		&r.BudgetTokens, &r.BudgetDollars, &r.BudgetLoops, &r.BudgetSeconds,
		&r.UsagePromptTokens, &r.UsageCompletionTokens, &r.UsageDollars, &r.UsageLoops,
		&meta, &created, &updated, &r.PolicyRef); err != nil {
		return RunRecord{}, err
	}
	r.Metadata = unmarshalMeta(meta)
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	return r, nil
}

func marshalMeta(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("storage: marshal metadata: %w", err)
	}
	return string(b), nil
}

func unmarshalMeta(s string) map[string]string {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func unmarshalArgs(s string) map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// quietGooseLogger discards goose's chatty migration output (e.g. "no migrations
// to run"); RiskKernel surfaces store status through its own slog logger.
type quietGooseLogger struct{}

func (quietGooseLogger) Printf(string, ...interface{}) {}
func (quietGooseLogger) Fatalf(format string, v ...interface{}) {
	panic(fmt.Sprintf(format, v...))
}
