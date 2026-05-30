package httpapi

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/storage"
)

//go:embed admin/approvals.html
var approvalsPage []byte

// approvalDecision is the POST /v1/runs/{id}/approve body (api/v1 ApprovalDecision).
type approvalDecision struct {
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"` // approve | deny
	Reason     string `json:"reason"`
	DecidedBy  string `json:"decidedBy"`
}

// handleApprove resolves a human-in-the-loop gate (POST /v1/runs/{id}/approve).
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var dec approvalDecision
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&dec); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	approve, ok := decisionToBool(dec.Decision)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", `decision must be "approve" or "deny"`)
		return
	}

	// Resolve the named approval, or the run's single pending one.
	approvalID := dec.ApprovalID
	if approvalID == "" {
		pending, err := s.pendingForRun(r, runID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		switch len(pending) {
		case 0:
			httpx.WriteError(w, http.StatusConflict, "no_pending_approval", "no approval is pending for this run")
			return
		case 1:
			approvalID = pending[0].ID
		default:
			httpx.WriteError(w, http.StatusConflict, "ambiguous_approval", "run has multiple pending approvals; specify approvalId")
			return
		}
	} else {
		// Validate the approval belongs to this run.
		a, err := s.approvals.Get(r.Context(), approvalID)
		if err != nil {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "approval not found")
			return
		}
		if a.RunID != runID {
			httpx.WriteError(w, http.StatusBadRequest, "bad_request", "approval does not belong to this run")
			return
		}
	}

	if err := s.approvals.Resolve(r.Context(), approvalID, approve, dec.Reason, dec.DecidedBy); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httpx.WriteError(w, http.StatusConflict, "already_resolved", "approval is unknown or already resolved")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Per the api/v1 contract, /approve returns the run's current state.
	body, err := s.runViewBody(r, runID)
	if err != nil {
		// Resolution succeeded even if we can't read the run back; report success.
		a, _ := s.approvals.Get(r.Context(), approvalID)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"resolved": approvalView(a)})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// handleListApprovals lists approvals (GET /v1/approvals?status=pending).
func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	store := s.runs.Store()
	if store == nil {
		httpx.WriteJSON(w, http.StatusOK, []any{})
		return
	}
	list, err := store.ListApprovals(r.Context(), status)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, approvalView(a))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleGetRun returns a run's state including any pending approval (GET /v1/runs/{id}).
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	body, err := s.runViewBody(r, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		if errors.Is(err, errNoStore) {
			httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "no durable store configured")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

var errNoStore = errors.New("no durable store configured")

// runViewBody builds the api/v1 Run view from the store, overlaying a pending
// approval (and the waiting_approval status) when one exists.
func (s *Server) runViewBody(r *http.Request, runID string) (map[string]any, error) {
	store := s.runs.Store()
	if store == nil {
		return nil, errNoStore
	}
	rec, err := store.GetRun(r.Context(), runID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"id":         rec.ID,
		"name":       rec.Name,
		"status":     rec.Status,
		"haltReason": rec.HaltReason,
		"usage": map[string]any{
			"tokens":           rec.UsagePromptTokens + rec.UsageCompletionTokens,
			"promptTokens":     rec.UsagePromptTokens,
			"completionTokens": rec.UsageCompletionTokens,
			"dollars":          rec.UsageDollars,
			"loops":            rec.UsageLoops,
		},
		"createdAt": rec.CreatedAt,
		"updatedAt": rec.UpdatedAt,
	}
	if pending, err := s.pendingForRun(r, runID); err == nil && len(pending) > 0 {
		body["pendingApproval"] = approvalView(pending[0])
		body["status"] = "waiting_approval"
	}
	return body, nil
}

// handleAdminApprovalsPage serves the embedded local approvals page.
func (s *Server) handleAdminApprovalsPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(approvalsPage)
}

// pendingForRun returns the run's pending approvals.
func (s *Server) pendingForRun(r *http.Request, runID string) ([]storage.ApprovalRecord, error) {
	store := s.runs.Store()
	if store == nil {
		return nil, nil
	}
	all, err := store.ListApprovals(r.Context(), storage.ApprovalPending)
	if err != nil {
		return nil, err
	}
	var out []storage.ApprovalRecord
	for _, a := range all {
		if a.RunID == runID {
			out = append(out, a)
		}
	}
	return out, nil
}

func decisionToBool(d string) (approve, ok bool) {
	switch d {
	case "approve":
		return true, true
	case "deny":
		return false, true
	default:
		return false, false
	}
}

func approvalView(a storage.ApprovalRecord) map[string]any {
	v := map[string]any{
		"id":         a.ID,
		"runId":      a.RunID,
		"stepIndex":  a.StepIndex,
		"tool":       a.Tool,
		"sideEffect": a.SideEffect,
		"arguments":  a.Arguments,
		"status":     a.Status,
		"createdAt":  a.CreatedAt,
	}
	if a.Reason != "" {
		v["reason"] = a.Reason
	}
	if a.DecidedBy != "" {
		v["decidedBy"] = a.DecidedBy
	}
	if a.DecidedAt != nil {
		v["decidedAt"] = a.DecidedAt.Format(time.RFC3339)
	}
	return v
}
