package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

type policyResp struct {
	Name   string `json:"name"`
	Budget struct {
		Tokens  int64   `json:"tokens"`
		Dollars float64 `json:"dollars"`
		Loops   int32   `json:"loops"`
		Seconds int32   `json:"seconds"`
	} `json:"budget"`
	ToolAllowlist  []string `json:"toolAllowlist"`
	ApprovalPolicy struct {
		RequireFor []struct {
			Tool       string `json:"tool"`
			SideEffect string `json:"sideEffect"`
		} `json:"requireFor"`
	} `json:"approvalPolicy"`
}

type runBudgetResp struct {
	ID     string `json:"id"`
	Budget struct {
		Tokens  int64   `json:"tokens"`
		Dollars float64 `json:"dollars"`
		Loops   int32   `json:"loops"`
		Seconds int32   `json:"seconds"`
	} `json:"budget"`
}

func TestPolicies_RegisterAndGet(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()

	reg := `{"name":"developer","budget":{"dollars":5,"loops":50},"toolAllowlist":["mcp://github"],"approvalPolicy":{"requireFor":[{"tool":"mcp://shell"},{"sideEffect":"*write*"}]}}`
	if w := do(t, h, http.MethodPost, "/v1/policies", reg); w.Code != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", w.Code, w.Body.String())
	}

	w := do(t, h, http.MethodGet, "/v1/policies/developer", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d", w.Code)
	}
	var p policyResp
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Budget.Dollars != 5 || p.Budget.Loops != 50 {
		t.Fatalf("budget = %+v", p.Budget)
	}
	if len(p.ToolAllowlist) != 1 || p.ToolAllowlist[0] != "mcp://github" {
		t.Fatalf("allowlist = %v", p.ToolAllowlist)
	}
	if len(p.ApprovalPolicy.RequireFor) != 2 || p.ApprovalPolicy.RequireFor[1].SideEffect != "*write*" {
		t.Fatalf("approval rules = %+v", p.ApprovalPolicy.RequireFor)
	}

	// Unknown policy → 404; missing name → 400.
	if w := do(t, h, http.MethodGet, "/v1/policies/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown policy status = %d, want 404", w.Code)
	}
	if w := do(t, h, http.MethodPost, "/v1/policies", `{"budget":{"loops":1}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no-name status = %d, want 400", w.Code)
	}
}

func TestPolicies_RunPolicyRefAppliesBudget(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()

	if w := do(t, h, http.MethodPost, "/v1/policies", `{"name":"capped","budget":{"dollars":2,"loops":20,"seconds":600}}`); w.Code != http.StatusOK {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}

	// A run referencing the bundle inherits its budget.
	w := do(t, h, http.MethodPost, "/v1/runs", `{"name":"r1","policyRef":"capped"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create run: %d %s", w.Code, w.Body.String())
	}
	var run runBudgetResp
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	if run.Budget.Dollars != 2 || run.Budget.Loops != 20 || run.Budget.Seconds != 600 {
		t.Fatalf("policyRef budget not applied: %+v", run.Budget)
	}

	// An inline budget overrides the bundle field-by-field (loops here), keeping the
	// rest from the bundle.
	w = do(t, h, http.MethodPost, "/v1/runs", `{"name":"r2","policyRef":"capped","budget":{"loops":3}}`)
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	if run.Budget.Loops != 3 {
		t.Fatalf("inline loops override = %d, want 3", run.Budget.Loops)
	}
	if run.Budget.Dollars != 2 || run.Budget.Seconds != 600 {
		t.Fatalf("non-overridden bundle fields changed: %+v", run.Budget)
	}

	// Unknown policyRef → 400.
	if w := do(t, h, http.MethodPost, "/v1/runs", `{"policyRef":"ghost"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown policyRef status = %d, want 400", w.Code)
	}
}
