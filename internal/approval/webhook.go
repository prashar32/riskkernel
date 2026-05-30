package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

// WebhookNotifier POSTs a JSON notification to a user-configured URL when an
// approval becomes pending. This is user-configured outbound network (like the
// OTLP endpoint) — RiskKernel calls it only because the user set the URL. It does
// NOT include any provider keys or secrets. See SECURITY.md.
type WebhookNotifier struct {
	url    string
	client *http.Client
	log    *slog.Logger
}

// NewWebhookNotifier returns a notifier, or nil if url is empty (no push channel).
func NewWebhookNotifier(url string, log *slog.Logger) *WebhookNotifier {
	if url == "" {
		return nil
	}
	return &WebhookNotifier{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

type webhookPayload struct {
	Event      string         `json:"event"`
	ID         string         `json:"id"`
	RunID      string         `json:"run_id"`
	StepIndex  int32          `json:"step_index"`
	Tool       string         `json:"tool"`
	SideEffect string         `json:"side_effect"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Notify fires the webhook asynchronously (best-effort; failures are logged, never
// fatal — a flaky webhook must not wedge a governed run).
func (w *WebhookNotifier) Notify(_ context.Context, a storage.ApprovalRecord) {
	payload := webhookPayload{
		Event:      "approval.pending",
		ID:         a.ID,
		RunID:      a.RunID,
		StepIndex:  a.StepIndex,
		Tool:       a.Tool,
		SideEffect: a.SideEffect,
		Arguments:  a.Arguments,
		CreatedAt:  a.CreatedAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		w.log.Error("approval webhook marshal failed", "err", err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
		if err != nil {
			w.log.Error("approval webhook request failed", "err", err)
			return
		}
		req.Header.Set("content-type", "application/json")
		resp, err := w.client.Do(req)
		if err != nil {
			w.log.Error("approval webhook delivery failed", "url", w.url, "err", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			w.log.Warn("approval webhook non-2xx", "url", w.url, "status", resp.StatusCode)
		}
	}()
}
