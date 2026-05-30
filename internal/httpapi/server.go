// Package httpapi hosts RiskKernel's HTTP surface: the public /v1 API, the
// OpenAI-compatible proxy, and the local admin/health endpoints. v0.1 starts with
// health and version; the proxy and /v1 run endpoints are layered on in later
// build steps against this same server.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/provider"
	"github.com/prashar32/riskkernel/internal/version"
)

// Server wires dependencies into an http.Handler. It holds no per-request state.
type Server struct {
	cfg       *config.Config
	providers *provider.Registry
	log       *slog.Logger
}

// New constructs a Server.
func New(cfg *config.Config, providers *provider.Registry, log *slog.Logger) *Server {
	return &Server{cfg: cfg, providers: providers, log: log}
}

// Handler returns the root HTTP handler with all routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Liveness/readiness: unauthenticated, no secrets, safe to expose to a load
	// balancer or orchestrator health probe.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)

	// Authenticated routes are registered here as later build steps land
	// (e.g. /v1/runs, the OpenAI-compatible proxy). They go through requireAuth.

	return s.recoverer(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
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
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
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
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an Error per the api/v1 contract shape.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "message": message})
}

// ReadTimeout / WriteTimeout defaults for the daemon's http.Server.
const (
	defaultReadTimeout  = 15 * time.Second
	defaultWriteTimeout = 130 * time.Second // > provider timeout so proxied calls finish
)
