package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/prashar32/riskkernel/internal/approval"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

// These endpoints let the Python SDK (Surface 2) drive a governed run directly:
// create it, tick loop iterations, checkpoint state, request tool approval, and
// cancel. Model-call metering/cost/budget is handled by routing the SDK's LLM
// calls through the proxy with the run-id header, so the SDK stays thin.

type budgetBody struct {
	Tokens  int64   `json:"tokens"`
	Dollars float64 `json:"dollars"`
	Loops   int32   `json:"loops"`
	Seconds int32   `json:"seconds"`
}

type createRunBody struct {
	Name      string            `json:"name"`
	Budget    *budgetBody       `json:"budget"`
	PolicyRef string            `json:"policyRef"`
	Metadata  map[string]string `json:"metadata"`
}

// handleCreateRun implements POST /v1/runs.
func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var body createRunBody
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	opts := runs.CreateOptions{Name: body.Name, Metadata: body.Metadata}

	// A referenced policy bundle supplies the run's default budget; an inline
	// budget then overrides it field-by-field (a non-zero inline field wins).
	var budget *governor.Budget
	if body.PolicyRef != "" {
		store := s.runs.Store()
		if store == nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "policyRef requires a durable store")
			return
		}
		p, err := store.GetPolicy(r.Context(), body.PolicyRef)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				httpx.WriteError(w, http.StatusBadRequest, "unknown_policy", "no policy named "+body.PolicyRef)
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		budget = &governor.Budget{
			Tokens: p.BudgetTokens, Dollars: p.BudgetDollars,
			Loops: p.BudgetLoops, Seconds: p.BudgetSeconds,
		}
	}
	if body.Budget != nil {
		if budget == nil {
			budget = &governor.Budget{}
		}
		if body.Budget.Tokens != 0 {
			budget.Tokens = body.Budget.Tokens
		}
		if body.Budget.Dollars != 0 {
			budget.Dollars = body.Budget.Dollars
		}
		if body.Budget.Loops != 0 {
			budget.Loops = body.Budget.Loops
		}
		if body.Budget.Seconds != 0 {
			budget.Seconds = body.Budget.Seconds
		}
	}
	opts.Budget = budget

	run := s.runs.Create(opts)
	httpx.WriteJSON(w, http.StatusCreated, runViewFromManager(run))
}

// handleBeginStep implements POST /v1/runs/{id}/steps — registers a loop
// iteration and enforces the loop + time budgets. 402 when the budget is spent.
func (s *Server) handleBeginStep(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runs.Get(r.PathValue("id"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	step, err := run.BeginStep()
	if err != nil {
		writeHalt(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stepIndex": step})
}

type checkpointBody struct {
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
}

// handleSaveCheckpoint implements POST /v1/runs/{id}/checkpoints.
func (s *Server) handleSaveCheckpoint(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, ok := s.runs.Get(runID); !ok {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	var body checkpointBody
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.runs.Checkpoint(runID, body.Name, body.Payload); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// handleCancelRun implements POST /v1/runs/{id}/cancel (kill switch).
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runs.Get(r.PathValue("id"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	run.Cancel()
	httpx.WriteJSON(w, http.StatusOK, runViewFromManager(run))
}

// handleListToolCalls implements GET /v1/runs/{id}/tool-calls.
func (s *Server) handleListToolCalls(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	store := s.runs.Store()
	if store == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "no durable store configured")
		return
	}
	if _, err := store.GetRun(r.Context(), runID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	calls, err := store.ListToolCalls(r.Context(), runID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	views := make([]toolCallView, 0, len(calls))
	for _, call := range calls {
		views = append(views, toolCallViewFromStorage(call))
	}
	httpx.WriteJSON(w, http.StatusOK, views)
}

type toolApprovalBody struct {
	Tool       string         `json:"tool"`
	SideEffect string         `json:"sideEffect"`
	StepIndex  int32          `json:"stepIndex"`
	Arguments  map[string]any `json:"arguments"`
}

// handleRequestApproval implements POST /v1/runs/{id}/approvals — the SDK's
// @governed_tool path. If policy allows the call, returns status "approved"
// immediately; otherwise creates a pending approval the SDK polls via
// GET /v1/approvals/{id}.
func (s *Server) handleRequestApproval(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, ok := s.runs.Get(runID); !ok {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	var body toolApprovalBody
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if body.Tool == "" {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "tool is required")
		return
	}
	rec, required, err := s.approvals.Create(r.Context(), approval.Request{
		RunID: runID, StepIndex: body.StepIndex, Tool: body.Tool,
		SideEffect: body.SideEffect, Arguments: body.Arguments,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !required {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "approved", "required": false})
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, approvalView(rec))
}

// handleGetApproval implements GET /v1/approvals/{id} — the SDK polls this for a
// pending approval's resolution.
func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	a, err := s.approvals.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "approval not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, approvalView(a))
}

// --- helpers ---

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

// writeHalt translates a governor.HaltError into a 402 response.
func writeHalt(w http.ResponseWriter, err error) {
	var he *governor.HaltError
	if errors.As(err, &he) {
		httpx.WriteError(w, http.StatusPaymentRequired, string(he.Reason), "run halted: "+string(he.Reason))
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
}

type toolCallView struct {
	ID         string         `json:"id"`
	RunID      string         `json:"runId"`
	StepIndex  int32          `json:"stepIndex"`
	Tool       string         `json:"tool"`
	SideEffect string         `json:"sideEffect"`
	Arguments  map[string]any `json:"arguments"`
	Status     string         `json:"status"`
	CreatedAt  time.Time      `json:"createdAt"`
}

func toolCallViewFromStorage(call storage.ToolCallRecord) toolCallView {
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	return toolCallView{
		ID:         call.ID,
		RunID:      call.RunID,
		StepIndex:  call.StepIndex,
		Tool:       call.Tool,
		SideEffect: call.SideEffect,
		Arguments:  args,
		Status:     call.Status,
		CreatedAt:  call.CreatedAt,
	}
}

func runViewFromManager(run *runs.Run) map[string]any {
	v := run.View()
	return map[string]any{
		"id":         v.ID,
		"name":       v.Name,
		"status":     v.Status,
		"haltReason": string(v.HaltReason),
		"budget": map[string]any{
			"tokens": v.Budget.Tokens, "dollars": v.Budget.Dollars,
			"loops": v.Budget.Loops, "seconds": v.Budget.Seconds,
		},
		"usage": map[string]any{
			"tokens":           v.Usage.Tokens(),
			"promptTokens":     v.Usage.PromptTokens,
			"completionTokens": v.Usage.CompletionTokens,
			"dollars":          v.Usage.Dollars,
			"loops":            v.Usage.Loops,
		},
		"createdAt": v.CreatedAt,
		"updatedAt": v.UpdatedAt,
	}
}
