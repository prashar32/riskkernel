package approval

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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

func testSlack(t *testing.T) *SlackNotifier {
	t.Helper()
	n := NewSlackNotifier("xoxb-test", "C123", "shhh", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if n == nil {
		t.Fatal("NewSlackNotifier returned nil")
	}
	return n
}

func slackSign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSlackNotifier_VerifySignature(t *testing.T) {
	n := testSlack(t)
	fixed := time.Unix(1_700_000_000, 0)
	n.now = func() time.Time { return fixed }
	ts := strconv.FormatInt(fixed.Unix(), 10)
	body := []byte("payload=%7B%22hi%22%3A1%7D")

	if !n.VerifySignature(ts, slackSign("shhh", ts, body), body) {
		t.Fatal("a valid signature should verify")
	}
	if n.VerifySignature(ts, slackSign("shhh", ts, body), []byte("payload=tampered")) {
		t.Fatal("a tampered body must fail")
	}
	if n.VerifySignature(ts, slackSign("wrong-secret", ts, body), body) {
		t.Fatal("a wrong secret must fail")
	}
	stale := strconv.FormatInt(fixed.Add(-6*time.Minute).Unix(), 10)
	if n.VerifySignature(stale, slackSign("shhh", stale, body), body) {
		t.Fatal("a stale timestamp must fail (replay protection)")
	}
	// No signing secret → fail closed.
	n2 := NewSlackNotifier("xoxb", "C1", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if n2.VerifySignature(ts, slackSign("", ts, body), body) {
		t.Fatal("a missing signing secret must fail closed")
	}
}

func TestSlackNotifier_ParseInteraction(t *testing.T) {
	n := testSlack(t)
	mk := func(actionID string) []byte {
		b, _ := json.Marshal(map[string]any{
			"type":    "block_actions",
			"user":    map[string]any{"username": "alice", "id": "U1"},
			"channel": map[string]any{"id": "C9"},
			"message": map[string]any{"ts": "123.45"},
			"actions": []map[string]any{{"action_id": actionID, "value": "appr-1"}},
		})
		return b
	}

	act, ok := n.ParseInteraction(mk(slackApproveAction))
	if !ok || !act.Approve || act.ApprovalID != "appr-1" || act.By != "slack:alice" ||
		act.Channel != "C9" || act.MessageTS != "123.45" {
		t.Fatalf("approve parse = %+v ok=%v", act, ok)
	}
	if act, ok := n.ParseInteraction(mk(slackDenyAction)); !ok || act.Approve {
		t.Fatalf("deny parse = %+v ok=%v", act, ok)
	}
	if _, ok := n.ParseInteraction(mk("some_other_action")); ok {
		t.Fatal("an unrecognized action must not parse")
	}
	if _, ok := n.ParseInteraction([]byte(`{"type":"view_submission"}`)); ok {
		t.Fatal("a non-block_actions payload must not parse")
	}
}

func TestSlackNotifier_Notify(t *testing.T) {
	var gotPath string
	gotBody := map[string]any{}
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if got := r.Header.Get("Authorization"); got != "Bearer xoxb-test" {
			t.Errorf("auth header = %q", got)
		}
		_, _ = io.WriteString(w, `{"ok":true,"ts":"1.2","channel":"C123"}`)
		close(done)
	}))
	defer srv.Close()

	n := testSlack(t)
	n.apiBase = srv.URL
	n.Notify(context.Background(), storage.ApprovalRecord{
		ID: "appr-1", RunID: "run-1", Tool: "mcp://shell", SideEffect: "write",
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify did not post to Slack")
	}
	if !strings.HasSuffix(gotPath, "/chat.postMessage") {
		t.Fatalf("posted to %s, want /chat.postMessage", gotPath)
	}
	if gotBody["channel"] != "C123" {
		t.Fatalf("channel = %v", gotBody["channel"])
	}
	// The approval id and the approve action must ride on the buttons so a click
	// resolves the right approval.
	raw, _ := json.Marshal(gotBody["blocks"])
	if !strings.Contains(string(raw), "appr-1") || !strings.Contains(string(raw), slackApproveAction) {
		t.Fatalf("blocks missing approval id / approve action: %s", raw)
	}
}
