package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

func TestAuditToolsPrintsToolCalls(t *testing.T) {
	dir := seedAuditStore(t)
	t.Setenv("RISKKERNEL_DATA_DIR", dir)

	out := captureStdout(t, func() error {
		return runAudit([]string{"tools", "run-tools"})
	})

	var body struct {
		RunID     string `json:"run_id"`
		ToolCalls []struct {
			ID         string         `json:"id"`
			RunID      string         `json:"runId"`
			StepIndex  int32          `json:"stepIndex"`
			Tool       string         `json:"tool"`
			SideEffect string         `json:"sideEffect"`
			Arguments  map[string]any `json:"arguments"`
			Status     string         `json:"status"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body.RunID != "run-tools" || len(body.ToolCalls) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if got := body.ToolCalls[0]; got.ID != "tc-1" || got.RunID != "run-tools" ||
		got.Tool != "mcp://shell" || got.SideEffect != "exec" ||
		got.Arguments["cmd"] != "ls" || got.Status != "approved" {
		t.Fatalf("tool call = %+v", got)
	}
}

func TestAuditExportIncludesToolCalls(t *testing.T) {
	dir := seedAuditStore(t)
	t.Setenv("RISKKERNEL_DATA_DIR", dir)

	out := captureStdout(t, func() error {
		return runAudit([]string{"export", "run-tools"})
	})

	var body struct {
		RunID     string          `json:"run_id"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body.RunID != "run-tools" {
		t.Fatalf("run_id = %q", body.RunID)
	}
	if !bytes.Contains(body.ToolCalls, []byte(`"mcp://shell"`)) {
		t.Fatalf("tool_calls missing from export: %s", string(out))
	}
}

func TestAuditSummaryByMetadata(t *testing.T) {
	dir := seedSummaryStore(t)
	t.Setenv("RISKKERNEL_DATA_DIR", dir)

	out := captureStdout(t, func() error {
		return runAudit([]string{"summary", "--by", "metadata.team", "--json"})
	})

	var sum struct {
		By     string `json:"by"`
		Groups []struct {
			Key              string  `json:"key"`
			Calls            int64   `json:"calls"`
			PromptTokens     int64   `json:"promptTokens"`
			CompletionTokens int64   `json:"completionTokens"`
			Dollars          float64 `json:"dollars"`
		} `json:"groups"`
		Total struct {
			Calls   int64   `json:"calls"`
			Dollars float64 `json:"dollars"`
		} `json:"total"`
	}
	if err := json.Unmarshal(out, &sum); err != nil {
		t.Fatalf("decode: %v (out=%s)", err, out)
	}
	if sum.By != "metadata.team" || len(sum.Groups) != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	byTeam := map[string]float64{}
	for _, g := range sum.Groups {
		byTeam[g.Key] = g.Dollars
	}
	if byTeam["alpha"] != 0.01 || byTeam["beta"] != 0.03 {
		t.Fatalf("per-team dollars = %v, want alpha=0.01 beta=0.03", byTeam)
	}
	if sum.Total.Calls != 2 || sum.Total.Dollars != 0.04 {
		t.Fatalf("total = %+v, want calls=2 dollars=0.04", sum.Total)
	}
}

func TestAuditSummaryRequiresBy(t *testing.T) {
	if err := runAudit([]string{"summary"}); err == nil {
		t.Fatal("audit summary with no --by should error")
	}
}

func TestParseTimeFlag(t *testing.T) {
	if _, err := parseTimeFlag("2026-06-15"); err != nil {
		t.Errorf("date: %v", err)
	}
	if _, err := parseTimeFlag("2026-06-15T10:00:00Z"); err != nil {
		t.Errorf("rfc3339: %v", err)
	}
	if _, err := parseTimeFlag("nope"); err == nil {
		t.Error("invalid time should error")
	}
}

func TestSummaryHeader(t *testing.T) {
	if got := summaryHeader("metadata.team"); got != "TEAM" {
		t.Errorf("metadata.team header = %q, want TEAM", got)
	}
	if got := summaryHeader("provider"); got != "PROVIDER" {
		t.Errorf("provider header = %q, want PROVIDER", got)
	}
}

func seedSummaryStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.OpenSQLite(filepath.Join(dir, "riskkernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	for _, r := range []struct {
		id, team string
		dollars  float64
	}{{"run-a", "alpha", 0.01}, {"run-b", "beta", 0.03}} {
		if err := s.UpsertRun(ctx, storage.RunRecord{
			ID: r.id, Status: "running", Metadata: map[string]string{"team": r.team},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendLedger(ctx, storage.LedgerEntry{
			RunID: r.id, StepIndex: 1, Provider: "anthropic", Model: "claude",
			PromptTokens: 100, CompletionTokens: 50, Dollars: r.dollars, Priced: true, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func seedAuditStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.OpenSQLite(filepath.Join(dir, "riskkernel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	if err := s.UpsertRun(context.Background(), storage.RunRecord{
		ID: "run-tools", Status: "running", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendToolCall(context.Background(), storage.ToolCallRecord{
		ID: "tc-1", RunID: "run-tools", StepIndex: 1, Tool: "mcp://shell",
		SideEffect: "exec", Arguments: map[string]any{"cmd": "ls"}, Status: "approved", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func captureStdout(t *testing.T, fn func() error) []byte {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = fn()
	_ = w.Close()
	os.Stdout = orig
	if err != nil {
		t.Fatal(err)
	}
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return out
}
