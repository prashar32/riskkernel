package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/approval"
	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/memory"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

// newSlackTestServer builds a Server whose Slack interactivity endpoint is wired
// with the given signing secret. The gate uses a nil notifier so creating an
// approval makes no outbound Slack call (outbound is covered in the approval
// package); the inbound handler still has the SlackNotifier to verify + parse.
func newSlackTestServer(t *testing.T, secret string) (*Server, *runs.Manager, *approval.Gate) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "slack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := runs.NewManager(governor.Budget{Tokens: 100000}).WithStore(store, log)
	gate := approval.NewGate(store, approval.Policy{DefaultSafe: true}, nil, log)
	slack := approval.NewSlackNotifier("xoxb-test", "C123", secret, log)
	srv := New(&config.Config{}, nil, mgr, gate, slack, nil, memory.NewReader(t.TempDir()), log)
	return srv, mgr, gate
}

func slackSignTest(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// slackInteractionBody builds a form body for an Approve/Deny click. Channel/ts
// are omitted so the source-message update no-ops (hermetic — no Slack call).
func slackInteractionBody(t *testing.T, actionID, approvalID string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"type":    "block_actions",
		"user":    map[string]any{"username": "alice"},
		"actions": []map[string]any{{"action_id": actionID, "value": approvalID}},
	})
	return "payload=" + url.QueryEscape(string(payload))
}

func TestSlackInteraction_ResolvesApproval(t *testing.T) {
	const secret = "shhh"
	srv, mgr, gate := newSlackTestServer(t, secret)
	h := srv.Handler()

	mgr.Create(runs.CreateOptions{ID: "run-s"}) // run row (FK)
	rec, required, err := gate.Create(context.Background(), approval.Request{
		RunID: "run-s", StepIndex: 1, Tool: "mcp://shell", SideEffect: "exec",
	})
	if err != nil || !required {
		t.Fatalf("create approval: required=%v err=%v", required, err)
	}

	body := slackInteractionBody(t, "riskkernel_approve", rec.ID)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/slack/interactions", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackSignTest(secret, ts, []byte(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	got, err := gate.Get(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != storage.ApprovalApproved {
		t.Fatalf("approval status = %q, want approved", got.Status)
	}
	if got.DecidedBy != "slack:alice" {
		t.Fatalf("decidedBy = %q, want slack:alice", got.DecidedBy)
	}
}

func TestSlackInteraction_RejectsBadSignature(t *testing.T) {
	srv, mgr, gate := newSlackTestServer(t, "shhh")
	h := srv.Handler()

	mgr.Create(runs.CreateOptions{ID: "run-s2"})
	rec, _, _ := gate.Create(context.Background(), approval.Request{
		RunID: "run-s2", StepIndex: 1, Tool: "mcp://shell", SideEffect: "exec",
	})

	body := slackInteractionBody(t, "riskkernel_approve", rec.ID)
	req := httptest.NewRequest(http.MethodPost, "/v1/integrations/slack/interactions", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Slack-Signature", "v0=deadbeef") // forged
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("forged-signature status = %d, want 401", w.Code)
	}
	got, _ := gate.Get(context.Background(), rec.ID)
	if got.Status != storage.ApprovalPending {
		t.Fatalf("approval must stay pending after a rejected signature, got %q", got.Status)
	}
}
