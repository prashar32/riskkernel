package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

func newTestServer(t *testing.T, token string) (*Server, *runs.Manager) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := runs.NewManager(governor.Budget{Tokens: 100000}).WithStore(store, log)
	srv := New(&config.Config{APIToken: token}, nil, mgr, log)
	return srv, mgr
}

func TestHealthAndVersion(t *testing.T) {
	srv, _ := newTestServer(t, "")
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
	srv, mgr := newTestServer(t, "")
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

func TestGetCheckpoint_AuthRequired(t *testing.T) {
	srv, _ := newTestServer(t, "sekret")
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
