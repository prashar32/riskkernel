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
