package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCreateRun(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()
	w := do(t, h, http.MethodPost, "/v1/runs", `{"name":"sdk-run","budget":{"tokens":1000,"loops":3}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var run map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	if run["id"] == "" || run["status"] != "running" {
		t.Fatalf("run = %v", run)
	}
	budget := run["budget"].(map[string]any)
	if budget["tokens"].(float64) != 1000 {
		t.Errorf("budget = %v", budget)
	}
}

func TestBeginStep_LoopBudget(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()
	w := do(t, h, http.MethodPost, "/v1/runs", `{"budget":{"loops":2}}`)
	var run map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	id := run["id"].(string)

	for i := 1; i <= 2; i++ {
		w := do(t, h, http.MethodPost, "/v1/runs/"+id+"/steps", "")
		if w.Code != http.StatusOK {
			t.Fatalf("step %d status = %d", i, w.Code)
		}
	}
	// 3rd step exceeds the loop budget.
	w = do(t, h, http.MethodPost, "/v1/runs/"+id+"/steps", "")
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("3rd step status = %d, want 402; body=%s", w.Code, w.Body.String())
	}
	var errBody struct{ Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &errBody)
	if errBody.Code != "loop_budget_exceeded" {
		t.Errorf("error code = %q", errBody.Code)
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()
	w := do(t, h, http.MethodPost, "/v1/runs", `{"name":"cp"}`)
	var run map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	id := run["id"].(string)

	w = do(t, h, http.MethodPost, "/v1/runs/"+id+"/checkpoints",
		`{"name":"after-plan","payload":{"messages":["hi"],"cursor":3}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("checkpoint status = %d, body=%s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodGet, "/v1/checkpoints/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get checkpoint status = %d", w.Code)
	}
	var cp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &cp)
	payload, _ := cp["payload"].(map[string]any)
	if payload["cursor"].(float64) != 3 {
		t.Fatalf("checkpoint payload not persisted: %v", cp)
	}
}

func TestCancelRun(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()
	w := do(t, h, http.MethodPost, "/v1/runs", `{}`)
	var run map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	id := run["id"].(string)

	w = do(t, h, http.MethodPost, "/v1/runs/"+id+"/cancel", `{"reason":"manual"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["status"] != "cancelled" {
		t.Fatalf("status after cancel = %v", got["status"])
	}
}

func TestRequestApproval_PollResolve(t *testing.T) {
	srv, _, _ := newTestServer(t, "") // gate is DefaultSafe:true
	h := srv.Handler()
	w := do(t, h, http.MethodPost, "/v1/runs", `{}`)
	var run map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	id := run["id"].(string)

	// A side-effecting tool needs approval → 201 pending.
	w = do(t, h, http.MethodPost, "/v1/runs/"+id+"/approvals",
		`{"tool":"mcp://shell","sideEffect":"exec","arguments":{"cmd":"ls"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("request approval status = %d, body=%s", w.Code, w.Body.String())
	}
	var ap map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &ap)
	apID := ap["id"].(string)
	if ap["status"] != "pending" {
		t.Fatalf("approval status = %v", ap["status"])
	}

	// Poll → still pending.
	w = do(t, h, http.MethodGet, "/v1/approvals/"+apID, "")
	_ = json.Unmarshal(w.Body.Bytes(), &ap)
	if ap["status"] != "pending" {
		t.Fatalf("poll status = %v", ap["status"])
	}

	// Resolve via the HITL endpoint, then poll → approved.
	w = do(t, h, http.MethodPost, "/v1/runs/"+id+"/approve",
		`{"approvalId":"`+apID+`","decision":"approve","decidedBy":"tester"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d", w.Code)
	}
	w = do(t, h, http.MethodGet, "/v1/approvals/"+apID, "")
	_ = json.Unmarshal(w.Body.Bytes(), &ap)
	if ap["status"] != "approved" {
		t.Fatalf("final status = %v", ap["status"])
	}
}

func TestRequestApproval_NotRequired(t *testing.T) {
	srv, _, _ := newTestServer(t, "")
	h := srv.Handler()
	w := do(t, h, http.MethodPost, "/v1/runs", `{}`)
	var run map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	id := run["id"].(string)

	// Read-only tool (no side effect) → auto-approved, no pending row.
	w = do(t, h, http.MethodPost, "/v1/runs/"+id+"/approvals", `{"tool":"mcp://fs","sideEffect":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (auto-approve)", w.Code)
	}
	var ap map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &ap)
	if ap["status"] != "approved" || ap["required"] != false {
		t.Fatalf("auto-approve response = %v", ap)
	}
}
