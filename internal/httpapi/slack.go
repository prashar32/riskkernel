package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

// handleSlackInteraction receives Slack's interactive callback when a human clicks
// Approve/Deny on a pending-approval message. It is authenticated by the Slack
// request signature (not the daemon's API token, which Slack can't send), resolves
// the approval, and updates the source message. (#92)
func (s *Server) handleSlackInteraction(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Verify the Slack signature over the RAW body before trusting anything in it.
	if !s.slack.VerifySignature(
		r.Header.Get("X-Slack-Request-Timestamp"),
		r.Header.Get("X-Slack-Signature"),
		body,
	) {
		s.log.Warn("slack interaction signature rejected", "remote", r.RemoteAddr)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	// Slack posts application/x-www-form-urlencoded with a single `payload` field.
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	act, ok := s.slack.ParseInteraction([]byte(form.Get("payload")))
	if !ok {
		// Not a recognized Approve/Deny click — ack so Slack doesn't retry.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Read the approval (for the run/tool labels on the message update) before
	// resolving; an unknown/expired id just gets an ack.
	a, err := s.approvals.Get(r.Context(), act.ApprovalID)
	if err != nil {
		s.log.Warn("slack interaction for unknown approval", "id", act.ApprovalID, "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := s.approvals.Resolve(r.Context(), act.ApprovalID, act.Approve, "via Slack", act.By); err != nil {
		if !errors.Is(err, storage.ErrNotFound) { // ErrNotFound = already resolved; still ack + refresh
			s.log.Error("slack interaction resolve failed", "id", act.ApprovalID, "err", err)
		}
	} else {
		s.log.Info("approval resolved via slack", "id", act.ApprovalID, "approved", act.Approve, "by", act.By)
	}

	// Rewrite the source message to show the decision and drop the buttons — off the
	// request path so a slow Slack call can't delay the ack Slack waits for.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.slack.UpdateResolved(ctx, act, a)
	}()

	w.WriteHeader(http.StatusOK)
}
