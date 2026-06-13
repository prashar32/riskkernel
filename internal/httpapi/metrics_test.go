package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

// seedMetricsRuns populates the store, via the manager, with a representative
// mix: a running run with spend, a run halted on the dollar budget, and a
// cancelled run. Returns the manager's store for any direct seeding.
func seedMetricsRuns(t *testing.T, mgr *runs.Manager) {
	t.Helper()

	// A running run with one recorded call → spend + tokens on the ledger.
	r := mgr.Create(runs.CreateOptions{ID: "run-running"})
	step, err := r.BeginStep()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RecordCall(runs.Call{
		StepIndex: step, Provider: "anthropic", Model: "claude-sonnet-4-5",
		PromptTokens: 100, CompletionTokens: 50, Dollars: 0.25, Priced: true, ResponseID: "a",
	}); err != nil {
		t.Fatal(err)
	}

	// A run that halts on the dollar budget: a tiny ceiling, then a call over it.
	budget := governor.Budget{Dollars: 0.01}
	h := mgr.Create(runs.CreateOptions{ID: "run-halted", Budget: &budget})
	hStep, _ := h.BeginStep()
	if err := h.RecordCall(runs.Call{
		StepIndex: hStep, Provider: "anthropic", Model: "claude-sonnet-4-5",
		PromptTokens: 10, CompletionTokens: 10, Dollars: 1.00, Priced: true, ResponseID: "b",
	}); err == nil {
		t.Fatal("expected the over-budget call to halt the run")
	}

	// A cancelled run (kill switch).
	c := mgr.Create(runs.CreateOptions{ID: "run-cancelled"})
	c.Cancel()
}

func TestMetricsEndpoint(t *testing.T) {
	srv, mgr, _ := newTestServer(t, "")
	seedMetricsRuns(t, mgr)

	// A pending human-in-the-loop approval, seeded directly into the store.
	store := mgr.Store()
	if err := store.CreateApproval(context.Background(), storage.ApprovalRecord{
		ID: "appr-1", RunID: "run-running", StepIndex: 1, Tool: "mcp://shell",
		SideEffect: "exec", Status: storage.ApprovalPending, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != metricsContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, metricsContentType)
	}

	body := w.Body.String()

	// Every metric carries its HELP/TYPE preamble.
	for _, want := range []string{
		"# HELP riskkernel_runs_total ",
		"# TYPE riskkernel_runs_total gauge",
		"# HELP riskkernel_runs_halted_total ",
		"# TYPE riskkernel_runs_halted_total gauge",
		"# TYPE riskkernel_spend_dollars_total counter",
		"# TYPE riskkernel_tokens_total counter",
		"# TYPE riskkernel_model_calls_total counter",
		"# TYPE riskkernel_approvals_pending gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing preamble %q\n---\n%s", want, body)
		}
	}

	// Run counts by status: one each of running / halted / cancelled.
	for _, want := range []string{
		`riskkernel_runs_total{status="running"} 1`,
		`riskkernel_runs_total{status="halted"} 1`,
		`riskkernel_runs_total{status="cancelled"} 1`,
		`riskkernel_runs_halted_total{reason="dollar_budget_exceeded"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing line %q\n---\n%s", want, body)
		}
	}

	// Spend / tokens / calls aggregate the ledger. The running run priced one call
	// at $0.25 / 150 tokens; the halted run's over-budget call is still recorded
	// ($1.00 / 20 tokens), so totals are $1.25, 170 tokens, 2 calls.
	for _, want := range []string{
		"riskkernel_spend_dollars_total 1.25",
		"riskkernel_tokens_total 170",
		"riskkernel_model_calls_total 2",
		"riskkernel_approvals_pending 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing line %q\n---\n%s", want, body)
		}
	}
}

// On a fresh daemon the runs_total series still exists (as a zero), so a scrape
// never reads an empty metric as a failed scrape.
func TestMetricsEndpoint_Empty(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`riskkernel_runs_total{status="running"} 0`,
		"riskkernel_spend_dollars_total 0",
		"riskkernel_tokens_total 0",
		"riskkernel_approvals_pending 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty metrics body missing line %q\n---\n%s", want, body)
		}
	}
}

func TestMetricsEndpoint_AuthRequired(t *testing.T) {
	srv, mgr, _ := newTestServer(t, "sekret")
	mgr.Create(runs.CreateOptions{ID: "run-1"})
	h := srv.Handler()

	// No token → 401.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", w.Code)
	}

	// Correct token → 200 with the exposition Content-Type.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authed status = %d, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != metricsContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, metricsContentType)
	}
}
