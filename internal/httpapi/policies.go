package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/storage"
)

// policyBody is the POST /v1/policies request (api/v1 Policy schema): a named,
// reusable bundle of a default budget, a tool allowlist, and approval rules.
type policyBody struct {
	Name           string              `json:"name"`
	Budget         *budgetBody         `json:"budget"`
	ToolAllowlist  []string            `json:"toolAllowlist"`
	ApprovalPolicy *approvalPolicyBody `json:"approvalPolicy"`
}

type approvalPolicyBody struct {
	RequireFor []approvalRuleBody `json:"requireFor"`
}

type approvalRuleBody struct {
	Tool       string `json:"tool"`
	SideEffect string `json:"sideEffect"`
}

// handleRegisterPolicy implements POST /v1/policies — register or update (by name)
// a reusable policy bundle. Deterministic config; no LLM is consulted.
func (s *Server) handleRegisterPolicy(w http.ResponseWriter, r *http.Request) {
	var body policyBody
	if err := decodeJSON(w, r, &body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "policy name is required")
		return
	}
	store := s.runs.Store()
	if store == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "no durable store configured")
		return
	}

	now := time.Now()
	rec := storage.PolicyRecord{
		Name:          body.Name,
		ToolAllowlist: body.ToolAllowlist,
		ApprovalRules: toApprovalRules(body.ApprovalPolicy),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if body.Budget != nil {
		rec.BudgetTokens = body.Budget.Tokens
		rec.BudgetDollars = body.Budget.Dollars
		rec.BudgetLoops = body.Budget.Loops
		rec.BudgetSeconds = body.Budget.Seconds
	}
	if err := store.UpsertPolicy(r.Context(), rec); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Read back so the response reflects the stored row (e.g. the original
	// created_at preserved across an update).
	saved, err := store.GetPolicy(r.Context(), body.Name)
	if err != nil {
		saved = rec
	}
	httpx.WriteJSON(w, http.StatusOK, policyView(saved))
}

// handleListPolicies implements GET /v1/policies.
func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	store := s.runs.Store()
	if store == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "no durable store configured")
		return
	}
	list, err := store.ListPolicies(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, policyView(p))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleGetPolicy implements GET /v1/policies/{name}.
func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	store := s.runs.Store()
	if store == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "no_store", "no durable store configured")
		return
	}
	p, err := store.GetPolicy(r.Context(), r.PathValue("name"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "policy not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, policyView(p))
}

func toApprovalRules(ap *approvalPolicyBody) []storage.ApprovalRule {
	if ap == nil {
		return nil
	}
	out := make([]storage.ApprovalRule, 0, len(ap.RequireFor))
	for _, r := range ap.RequireFor {
		out = append(out, storage.ApprovalRule{Tool: r.Tool, SideEffect: r.SideEffect})
	}
	return out
}

func policyView(p storage.PolicyRecord) map[string]any {
	rules := make([]map[string]any, 0, len(p.ApprovalRules))
	for _, r := range p.ApprovalRules {
		rules = append(rules, map[string]any{"tool": r.Tool, "sideEffect": r.SideEffect})
	}
	allow := p.ToolAllowlist
	if allow == nil {
		allow = []string{}
	}
	return map[string]any{
		"name": p.Name,
		"budget": map[string]any{
			"tokens": p.BudgetTokens, "dollars": p.BudgetDollars,
			"loops": p.BudgetLoops, "seconds": p.BudgetSeconds,
		},
		"toolAllowlist":  allow,
		"approvalPolicy": map[string]any{"requireFor": rules},
		"createdAt":      p.CreatedAt,
		"updatedAt":      p.UpdatedAt,
	}
}
