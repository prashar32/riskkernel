// Package httpapi hosts RiskKernel's HTTP surface: the public /v1 API, the
// OpenAI-compatible proxy, and the local admin/health endpoints. v0.1 starts with
// health and version; the proxy and /v1 run endpoints are layered on in later
// build steps against this same server.
package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prashar32/riskkernel/internal/approval"
	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/gateway"
	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/mcp"
	"github.com/prashar32/riskkernel/internal/memory"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
	"github.com/prashar32/riskkernel/internal/version"
)

// Server wires dependencies into an http.Handler. It holds no per-request state.
type Server struct {
	cfg       *config.Config
	gateway   *gateway.Gateway
	runs      *runs.Manager
	approvals *approval.Gate
	mcp       *mcp.Gateway
	memory    *memory.Reader
	log       *slog.Logger
}

// New constructs a Server.
func New(cfg *config.Config, gw *gateway.Gateway, mgr *runs.Manager, gate *approval.Gate,
	mcpGW *mcp.Gateway, mem *memory.Reader, log *slog.Logger) *Server {
	return &Server{cfg: cfg, gateway: gw, runs: mgr, approvals: gate, mcp: mcpGW, memory: mem, log: log}
}

// Handler returns the root HTTP handler with all routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Liveness/readiness: unauthenticated, no secrets, safe to expose to a load
	// balancer or orchestrator health probe.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)

	// Surface 1 — the OpenAI/Anthropic-compatible proxy, guarded by the bearer
	// token (which doubles as the virtual key: the client sets it as its provider
	// key and RiskKernel swaps in the real key at egress).
	if s.gateway != nil {
		s.gateway.Register(mux, s.requireAuth)
	}
	// MCP gateway (Surface 1, tool governance) — only when an upstream is set.
	if s.mcp != nil {
		s.mcp.Register(mux, s.requireAuth)
	}

	// Public /v1 contract routes (authenticated).
	if s.runs != nil {
		mux.HandleFunc("GET /v1/checkpoints/{run_id}", s.requireAuth(s.handleGetCheckpoint))
		mux.HandleFunc("GET /v1/runs/{id}", s.requireAuth(s.handleGetRun))
		mux.HandleFunc("GET /v1/runs/{id}/tool-calls", s.requireAuth(s.handleListToolCalls))
		// Run lifecycle control (Surface 2 — the Python SDK drives these).
		mux.HandleFunc("POST /v1/runs", s.requireAuth(s.handleCreateRun))
		mux.HandleFunc("POST /v1/runs/{id}/steps", s.requireAuth(s.handleBeginStep))
		mux.HandleFunc("POST /v1/runs/{id}/checkpoints", s.requireAuth(s.handleSaveCheckpoint))
		mux.HandleFunc("POST /v1/runs/{id}/cancel", s.requireAuth(s.handleCancelRun))
		// Reusable, named policy bundles — referenced by a run's policyRef.
		mux.HandleFunc("POST /v1/policies", s.requireAuth(s.handleRegisterPolicy))
		mux.HandleFunc("GET /v1/policies", s.requireAuth(s.handleListPolicies))
		mux.HandleFunc("GET /v1/policies/{name}", s.requireAuth(s.handleGetPolicy))
	}
	if s.approvals != nil {
		mux.HandleFunc("POST /v1/runs/{id}/approve", s.requireAuth(s.handleApprove))
		mux.HandleFunc("POST /v1/runs/{id}/approvals", s.requireAuth(s.handleRequestApproval))
		mux.HandleFunc("GET /v1/approvals", s.requireAuth(s.handleListApprovals))
		mux.HandleFunc("GET /v1/approvals/{id}", s.requireAuth(s.handleGetApproval))
		// Local admin web page (Surface: human-in-the-loop, pull channel).
		mux.HandleFunc("GET /admin/approvals", s.requireAuth(s.handleAdminApprovalsPage))
	}
	// Git-native memory layer.
	if s.memory != nil {
		mux.HandleFunc("GET /v1/memory", s.requireAuth(s.handleListMemory))
		mux.HandleFunc("GET /v1/memory/entry", s.requireAuth(s.handleReadMemory))
	}
	if s.runs != nil {
		mux.HandleFunc("GET /v1/memory/facts", s.requireAuth(s.handleListFacts))
		mux.HandleFunc("PUT /v1/memory/facts", s.requireAuth(s.handlePutFact))
	}

	return s.recoverer(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
}

// handleGetCheckpoint implements GET /v1/checkpoints/{run_id} — the latest
// crash-resumable checkpoint for a run, per the api/v1 contract.
func (s *Server) handleGetCheckpoint(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	store := s.runs.Store()
	if store == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "no durable store configured")
		return
	}
	cp, err := store.LatestCheckpoint(r.Context(), runID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "no checkpoint for run "+runID)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"runId":     cp.RunID,
		"stepIndex": cp.StepIndex,
		"usage": map[string]any{
			"promptTokens":     cp.UsagePromptTokens,
			"completionTokens": cp.UsageCompletionTokens,
			"tokens":           cp.UsagePromptTokens + cp.UsageCompletionTokens,
			"dollars":          cp.UsageDollars,
			"loops":            cp.UsageLoops,
		},
		"payload":   orEmptyPayload(cp.Payload),
		"createdAt": cp.CreatedAt,
	})
}

func orEmptyPayload(p map[string]any) map[string]any {
	if p == nil {
		return map[string]any{}
	}
	return p
}

// Serve runs the HTTP server on the given address until ctx is cancelled, then
// drains in-flight requests within a short grace period. This is how the kill
// switch and Ctrl-C reach a clean shutdown.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.Handler(),
		ReadTimeout:  defaultReadTimeout,
		WriteTimeout: defaultWriteTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("riskkernel listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}

// requireAuth wraps a handler with single-tenant bearer-token auth. If no API
// token is configured, auth is disabled (local-only convenience) and a warning is
// logged once at startup by the caller. Comparison is constant-time.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" {
			next(w, r)
			return
		}
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if len(h) <= len(prefix) || h[:len(prefix)] != prefix ||
			subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(s.cfg.APIToken)) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next(w, r)
	}
}

// recoverer turns a panic into a 500 instead of crashing the daemon, and logs it.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler", "panic", rec, "path", r.URL.Path)
				httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ReadTimeout / WriteTimeout defaults for the daemon's http.Server.
const (
	defaultReadTimeout  = 15 * time.Second
	defaultWriteTimeout = 130 * time.Second // > provider timeout so proxied calls finish
)
