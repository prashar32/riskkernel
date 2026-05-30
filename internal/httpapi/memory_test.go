package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/memory"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

// newMemoryServer builds a server with a populated memory dir and a store.
func newMemoryServer(t *testing.T) http.Handler {
	t.Helper()
	memDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(memDir, "developer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "developer", "style.md"),
		[]byte("---\ntitle: Style\n---\nuse tabs"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := runs.NewManager(governor.Budget{}).WithStore(store, log)
	srv := New(&config.Config{}, nil, mgr, nil, nil, memory.NewReader(memDir), log)
	return srv.Handler()
}

func TestMemoryEndpoints(t *testing.T) {
	h := newMemoryServer(t)

	// List a namespace.
	w := do(t, h, http.MethodGet, "/v1/memory?namespace=developer", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	var entries []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &entries)
	if len(entries) != 1 || entries[0]["title"] != "Style" {
		t.Fatalf("entries = %v", entries)
	}

	// Read an entry.
	w = do(t, h, http.MethodGet, "/v1/memory/entry?namespace=developer&name=style.md", "")
	if w.Code != http.StatusOK {
		t.Fatalf("read status = %d", w.Code)
	}
	var entry map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &entry)
	if entry["content"] != "---\ntitle: Style\n---\nuse tabs" {
		t.Fatalf("content = %v", entry["content"])
	}

	// Path traversal rejected.
	w = do(t, h, http.MethodGet, "/v1/memory/entry?namespace=developer&name=../../etc/passwd", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d, want 400", w.Code)
	}

	// Missing entry → 404.
	w = do(t, h, http.MethodGet, "/v1/memory/entry?namespace=developer&name=nope.md", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", w.Code)
	}
}

func TestFactsEndpoints(t *testing.T) {
	h := newMemoryServer(t)

	// Write a fact.
	w := do(t, h, http.MethodPut, "/v1/memory/facts",
		`{"namespace":"developer","key":"db","value":"postgres"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("put fact status = %d, body=%s", w.Code, w.Body.String())
	}

	// Read it back.
	w = do(t, h, http.MethodGet, "/v1/memory/facts?namespace=developer", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list facts status = %d", w.Code)
	}
	var facts []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &facts)
	if len(facts) != 1 || facts[0]["key"] != "db" || facts[0]["value"] != "postgres" {
		t.Fatalf("facts = %v", facts)
	}
}
