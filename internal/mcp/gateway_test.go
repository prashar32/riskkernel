package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/approval"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func reqCtx() context.Context { return context.Background() }

func newTestGateway(t *testing.T, allowlist, readonly []string) (*Gateway, *int32) {
	t.Helper()
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	t.Cleanup(upstream.Close)

	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := slog.New(slog.NewTextHandler(discard{}, nil))
	gate := approval.NewGate(store, approval.Policy{DefaultSafe: true}, nil, log)
	mgr := runs.NewManager(governor.Budget{}).WithStore(store, log)

	g := New(upstream.URL, allowlist, readonly, gate, mgr, store, 5*time.Second, log)
	return g, &hits
}

func mcpReq(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set(HeaderRunID, "test-run")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func rpcError(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	e, _ := resp["error"].(map[string]any)
	return e
}

func TestForwardsNonToolCall(t *testing.T) {
	g, hits := newTestGateway(t, nil, nil)
	w := httptest.NewRecorder()
	g.handle(w, mcpReq(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if w.Code != http.StatusOK || *hits != 1 {
		t.Fatalf("tools/list should forward: code=%d hits=%d", w.Code, *hits)
	}
}

func TestAllowlistBlocks(t *testing.T) {
	g, hits := newTestGateway(t, []string{"safe_*"}, nil)
	w := httptest.NewRecorder()
	g.handle(w, mcpReq(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"danger_rm"}}`))
	if e := rpcError(t, w); e == nil || e["code"].(float64) != -32001 {
		t.Fatalf("expected allowlist error, got %v", e)
	}
	if *hits != 0 {
		t.Fatal("blocked tool must NOT reach upstream")
	}
}

// spyStore records every tool-call audit row written through it (there is no
// tool_calls read API yet — that's #38 — so the test observes the writes directly).
type spyStore struct {
	storage.Store
	mu      sync.Mutex
	records []storage.ToolCallRecord
}

func (s *spyStore) AppendToolCall(ctx context.Context, t storage.ToolCallRecord) error {
	s.mu.Lock()
	s.records = append(s.records, t)
	s.mu.Unlock()
	return s.Store.AppendToolCall(ctx, t)
}

func TestAllowlistBlockIsAudited(t *testing.T) {
	base, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	spy := &spyStore{Store: base}
	log := slog.New(slog.NewTextHandler(discard{}, nil))
	gate := approval.NewGate(spy, approval.Policy{DefaultSafe: true}, nil, log)
	mgr := runs.NewManager(governor.Budget{}).WithStore(spy, log)
	// Upstream is intentionally unreachable: a blocked tool must never be forwarded.
	g := New("http://127.0.0.1:1", []string{"safe_*"}, nil, gate, mgr, spy, time.Second, log)

	w := httptest.NewRecorder()
	g.handle(w, mcpReq(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"danger_rm","arguments":{"path":"/etc"}}}`))

	if e := rpcError(t, w); e == nil || e["code"].(float64) != -32001 {
		t.Fatalf("expected allowlist error, got %v", e)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.records) != 1 || spy.records[0].Tool != "danger_rm" || spy.records[0].Status != "blocked" {
		t.Fatalf("blocked tool not audited: %+v", spy.records)
	}
}

func TestReadOnlyToolForwards(t *testing.T) {
	g, hits := newTestGateway(t, nil, []string{"search"})
	w := httptest.NewRecorder()
	g.handle(w, mcpReq(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"q":"x"}}}`))
	if w.Code != http.StatusOK || *hits != 1 {
		t.Fatalf("read-only tool should forward without approval: code=%d hits=%d", w.Code, *hits)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("response not forwarded: %s", w.Body.String())
	}
}

func TestSideEffectingToolApproved(t *testing.T) {
	g, hits := newTestGateway(t, nil, []string{"search"}) // "write" is NOT read-only

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		g.handle(w, mcpReq(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write","arguments":{"path":"/x"}}}`))
		done <- w
	}()

	id := waitPending(t, g)
	if err := g.gate.Resolve(reqCtx(), id, true, "ok", "tester"); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != http.StatusOK || *hits != 1 || !strings.Contains(w.Body.String(), "ok") {
			t.Fatalf("approved tool should forward: code=%d hits=%d body=%s", w.Code, *hits, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approved tools/call did not complete")
	}
}

func TestSideEffectingToolDenied(t *testing.T) {
	g, hits := newTestGateway(t, nil, nil)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		g.handle(w, mcpReq(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deploy","arguments":{}}}`))
		done <- w
	}()

	id := waitPending(t, g)
	if err := g.gate.Resolve(reqCtx(), id, false, "no", "tester"); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if e := rpcError(t, w); e == nil || e["code"].(float64) != -32003 {
			t.Fatalf("expected approval-denied error, got %v", e)
		}
		if *hits != 0 {
			t.Fatal("denied tool must NOT reach upstream")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("denied tools/call did not complete")
	}
}

func waitPending(t *testing.T, g *Gateway) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		p, err := g.gate.Pending(reqCtx())
		if err != nil {
			t.Fatal(err)
		}
		if len(p) == 1 {
			return p[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("pending approval never appeared")
	return ""
}
