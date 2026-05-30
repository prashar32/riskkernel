package approval

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

func newTestGate(t *testing.T, policy Policy) (*Gate, storage.Store) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "appr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Seed a run so approval FK constraints hold.
	now := time.Now().UTC()
	if err := store.UpsertRun(context.Background(), storage.RunRecord{
		ID: "run-1", Status: "running", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return NewGate(store, policy, nil, slog.New(slog.NewTextHandler(discard{}, nil))), store
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestGate_AutoApproveWhenNotRequired(t *testing.T) {
	g, _ := newTestGate(t, Policy{DefaultSafe: true})
	// Read-only call (no side effect) → not required → immediate approve.
	d, id, err := g.Request(context.Background(), Request{RunID: "run-1", Tool: "mcp://fs", SideEffect: ""})
	if err != nil || !d.Approved {
		t.Fatalf("expected auto-approve, got d=%+v id=%q err=%v", d, id, err)
	}
	// Nothing should have been persisted.
	pend, _ := g.Pending(context.Background())
	if len(pend) != 0 {
		t.Errorf("auto-approve should not create a pending row: %+v", pend)
	}
}

func TestGate_RequestBlocksUntilApproved(t *testing.T) {
	g, _ := newTestGate(t, Policy{DefaultSafe: true})

	type result struct {
		d   Decision
		err error
	}
	done := make(chan result, 1)
	go func() {
		d, _, err := g.Request(context.Background(), Request{
			RunID: "run-1", StepIndex: 2, Tool: "mcp://shell", SideEffect: "exec",
			Arguments: map[string]any{"cmd": "ls"},
		})
		done <- result{d, err}
	}()

	// Wait for the pending approval to appear, then approve it.
	id := waitPending(t, g)
	if err := g.Resolve(context.Background(), id, true, "looks fine", "tester"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil || !r.d.Approved || r.d.By != "tester" {
			t.Fatalf("expected approved decision, got %+v err=%v", r.d, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock after approval")
	}
}

func TestGate_RequestBlocksUntilDenied(t *testing.T) {
	g, _ := newTestGate(t, Policy{DefaultSafe: true})
	done := make(chan Decision, 1)
	go func() {
		d, _, _ := g.Request(context.Background(), Request{RunID: "run-1", Tool: "mcp://shell", SideEffect: "exec"})
		done <- d
	}()
	id := waitPending(t, g)
	if err := g.Resolve(context.Background(), id, false, "nope", "tester"); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-done:
		if d.Approved {
			t.Fatalf("expected denied, got %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock after denial")
	}
}

func TestGate_RequestContextCancel(t *testing.T) {
	g, _ := newTestGate(t, Policy{DefaultSafe: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := g.Request(ctx, Request{RunID: "run-1", Tool: "mcp://shell", SideEffect: "exec"})
		done <- err
	}()
	waitPending(t, g)
	cancel() // e.g. run killed / time budget hit while awaiting approval
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock on context cancel")
	}
}

func TestGate_ResolveUnknown(t *testing.T) {
	g, _ := newTestGate(t, Policy{DefaultSafe: true})
	err := g.Resolve(context.Background(), "does-not-exist", true, "", "")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func waitPending(t *testing.T, g *Gate) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		pend, err := g.Pending(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(pend) == 1 {
			return pend[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("pending approval never appeared")
	return ""
}
