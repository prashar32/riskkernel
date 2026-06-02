package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
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

func TestListToolCalls(t *testing.T) {
	srv, mgr, _ := newTestServer(t, "")
	h := srv.Handler()
	mgr.Create(runs.CreateOptions{ID: "run-tools"})
	if err := mgr.Store().AppendToolCall(context.Background(), storage.ToolCallRecord{
		ID: "tc-1", RunID: "run-tools", StepIndex: 1, Tool: "mcp://shell",
		SideEffect: "exec", Arguments: map[string]any{"cmd": "ls"}, Status: "approved",
	}); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, http.MethodGet, "/v1/runs/run-tools/tool-calls", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 {
		t.Fatalf("tool calls = %+v", body)
	}
	if body[0]["id"] != "tc-1" || body[0]["runId"] != "run-tools" ||
		body[0]["stepIndex"].(float64) != 1 || body[0]["sideEffect"] != "exec" ||
		body[0]["status"] != "approved" {
		t.Fatalf("tool call body = %+v", body[0])
	}
	args := body[0]["arguments"].(map[string]any)
	if args["cmd"] != "ls" {
		t.Fatalf("arguments = %+v", args)
	}

	w = do(t, h, http.MethodGet, "/v1/runs/missing/tool-calls", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing run status = %d, want 404", w.Code)
	}
}
