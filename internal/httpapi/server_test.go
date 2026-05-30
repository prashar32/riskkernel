package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/approval"
	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

func newTestServer(t *testing.T, token string) (*Server, *runs.Manager, *approval.Gate) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := runs.NewManager(governor.Budget{Tokens: 100000}).WithStore(store, log)
	gate := approval.NewGate(store, approval.Policy{DefaultSafe: true}, nil, log)
	srv := New(&config.Config{APIToken: token}, nil, mgr, gate, log)
	return srv, mgr, gate
}

func TestHealthAndVersion(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()

	for _, path := range []string{"/healthz", "/version"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s = %d", path, w.Code)
		}
	}
}

func TestGetCheckpoint(t *testing.T) {
	srv, mgr, _ := newTestServer(t, "")
	h := srv.Handler()

	// Seed a run with one recorded call → a checkpoint is written per step.
	r := mgr.Create(runs.CreateOptions{ID: "cp-run"})
	step, _ := r.BeginStep()
	if err := r.RecordCall(runs.Call{StepIndex: step, Provider: "anthropic", Model: "claude-sonnet-4-5",
		PromptTokens: 120, CompletionTokens: 30, Dollars: 0.01, Priced: true, ResponseID: "z"}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/checkpoints/cp-run", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		RunID     string `json:"runId"`
		StepIndex int    `json:"stepIndex"`
		Usage     struct {
			Tokens int64 `json:"tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RunID != "cp-run" || body.StepIndex != 1 || body.Usage.Tokens != 150 {
		t.Fatalf("checkpoint body = %+v", body)
	}

	// Unknown run → 404.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/checkpoints/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown run status = %d, want 404", w.Code)
	}
}

func TestApproveFlow(t *testing.T) {
	srv, mgr, gate := newTestServer(t, "")
	h := srv.Handler()

	mgr.Create(runs.CreateOptions{ID: "run-x"}) // persist the run row (FK)

	// A side-effecting tool call blocks awaiting approval.
	type res struct {
		d   approval.Decision
		err error
	}
	done := make(chan res, 1)
	go func() {
		d, _, err := gate.Request(context.Background(), approval.Request{
			RunID: "run-x", StepIndex: 1, Tool: "mcp://shell", SideEffect: "exec",
			Arguments: map[string]any{"cmd": "rm -rf /tmp/x"},
		})
		done <- res{d, err}
	}()

	// Wait until it's pending, then confirm the API surfaces it.
	id := waitPendingAPI(t, h)

	// GET /v1/runs/run-x shows status waiting_approval + the pending approval.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/runs/run-x", nil))
	var run map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	if run["status"] != "waiting_approval" || run["pendingApproval"] == nil {
		t.Fatalf("run should be waiting_approval with a pending approval: %v", run)
	}

	// Approve via the API (no approvalId → resolves the single pending one).
	body := `{"decision":"approve","decidedBy":"tester"}`
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/runs/run-x/approve", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body=%s", w.Code, w.Body.String())
	}

	select {
	case r := <-done:
		if r.err != nil || !r.d.Approved {
			t.Fatalf("blocked call should be approved: %+v err=%v", r.d, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approval via API did not unblock the call")
	}
	_ = id

	// Pending list is now empty.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/approvals?status=pending", nil))
	var list []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("expected no pending approvals after approve, got %d", len(list))
	}
}

func TestApprove_NoPending(t *testing.T) {
	srv, mgr, _ := newTestServer(t, "")
	h := srv.Handler()
	mgr.Create(runs.CreateOptions{ID: "run-y"})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/runs/run-y/approve", strings.NewReader(`{"decision":"approve"}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when nothing pending, got %d", w.Code)
	}
}

func waitPendingAPI(t *testing.T, h http.Handler) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/approvals?status=pending", nil))
		var list []map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &list)
		if len(list) == 1 {
			return list[0]["id"].(string)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("pending approval never appeared via API")
	return ""
}

func TestGetCheckpoint_AuthRequired(t *testing.T) {
	srv, _, _ := newTestServer(t, "sekret")
	h := srv.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/checkpoints/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", w.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/checkpoints/x", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	// Authenticated but the run doesn't exist → 404 (not 401).
	if w.Code != http.StatusNotFound {
		t.Fatalf("authed status = %d, want 404", w.Code)
	}
}
