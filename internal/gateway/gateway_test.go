package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/otel"
	"github.com/prashar32/riskkernel/internal/pricing"
	"github.com/prashar32/riskkernel/internal/provider"
	"github.com/prashar32/riskkernel/internal/runs"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// fakeProvider is a deterministic stand-in for a real provider.
type fakeProvider struct {
	name  string
	resp  *provider.Response
	err   error
	calls int32
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Chat(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	atomic.AddInt32(&f.calls, 1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func newTestGateway(t *testing.T, budget governor.Budget, fp provider.Provider) *Gateway {
	t.Helper()
	reg, err := provider.NewRegistry("anthropic", fp)
	if err != nil {
		t.Fatal(err)
	}
	mgr := runs.NewManager(budget)
	return New(reg, mgr, pricing.NewTable(nil), otel.Disabled(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func postChat(g *Gateway, runID, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if runID != "" {
		r.Header.Set(HeaderRunID, runID)
	}
	w := httptest.NewRecorder()
	g.handleChatCompletions(w, r)
	return w
}

const sonnetBody = `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`

func sonnetResp() *provider.Response {
	return &provider.Response{
		ID:           "abc",
		Model:        "claude-sonnet-4-5",
		Content:      "hello",
		FinishReason: "end_turn",
		Usage:        provider.Usage{PromptTokens: 1000, CompletionTokens: 1000},
	}
}

func TestChatCompletions_Success(t *testing.T) {
	g := newTestGateway(t, governor.Budget{}, &fakeProvider{name: "anthropic", resp: sonnetResp()})
	w := postChat(g, "", sonnetBody)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp oaiChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Object != "chat.completion" || len(resp.Choices) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Choices[0].Message.Content != "hello" || resp.Choices[0].FinishReason != "stop" {
		t.Errorf("choice = %+v", resp.Choices[0])
	}
	if resp.Usage.TotalTokens != 2000 {
		t.Errorf("total tokens = %d", resp.Usage.TotalTokens)
	}
	// Governance headers stamped. Cost = 1000/1e6*3 + 1000/1e6*15 = 0.018.
	if got := w.Header().Get(headerCostUSD); got != "0.018000" {
		t.Errorf("cost header = %q, want 0.018000", got)
	}
	if got := w.Header().Get(headerTokens); got != "2000" {
		t.Errorf("tokens header = %q", got)
	}
	if got := w.Header().Get(headerStep); got != "1" {
		t.Errorf("step header = %q", got)
	}
	if w.Header().Get(HeaderRunID) == "" {
		t.Error("run-id header missing")
	}
}

func TestChatCompletions_BudgetHaltAcrossCalls(t *testing.T) {
	// Token budget 1500; one call consumes 2000 → halts. Same run is refused next.
	g := newTestGateway(t, governor.Budget{Tokens: 1500}, &fakeProvider{name: "anthropic", resp: sonnetResp()})

	w1 := postChat(g, "run-A", sonnetBody)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call status = %d", w1.Code)
	}
	// The breaching call still returns its paid-for result, but flags the halt.
	if got := w1.Header().Get(headerHaltReason); got != string(governor.HaltTokenBudget) {
		t.Errorf("halt header = %q, want token_budget_exceeded", got)
	}

	w2 := postChat(g, "run-A", sonnetBody)
	if w2.Code != http.StatusPaymentRequired {
		t.Fatalf("second call status = %d, want 402; body=%s", w2.Code, w2.Body.String())
	}
	var errBody struct{ Code, Message string }
	_ = json.Unmarshal(w2.Body.Bytes(), &errBody)
	if errBody.Code != string(governor.HaltTokenBudget) {
		t.Errorf("error code = %q", errBody.Code)
	}
}

func TestChatCompletions_RunGroupingSharesBudgetAndSteps(t *testing.T) {
	fp := &fakeProvider{name: "anthropic", resp: &provider.Response{
		ID: "x", Model: "claude-sonnet-4-5", Content: "ok",
		Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 10},
	}}
	g := newTestGateway(t, governor.Budget{}, fp)

	w1 := postChat(g, "shared", sonnetBody)
	w2 := postChat(g, "shared", sonnetBody)
	if w1.Header().Get(headerStep) != "1" || w2.Header().Get(headerStep) != "2" {
		t.Errorf("steps = %q, %q; want 1, 2", w1.Header().Get(headerStep), w2.Header().Get(headerStep))
	}
	if w1.Header().Get(HeaderRunID) != "shared" || w2.Header().Get(HeaderRunID) != "shared" {
		t.Errorf("run ids = %q, %q", w1.Header().Get(HeaderRunID), w2.Header().Get(HeaderRunID))
	}
}

func TestChatCompletions_StreamingRejected(t *testing.T) {
	g := newTestGateway(t, governor.Budget{}, &fakeProvider{name: "anthropic", resp: sonnetResp()})
	w := postChat(g, "", `{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func TestChatCompletions_BadJSON(t *testing.T) {
	g := newTestGateway(t, governor.Budget{}, &fakeProvider{name: "anthropic", resp: sonnetResp()})
	w := postChat(g, "", `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestChatCompletions_ProviderError(t *testing.T) {
	g := newTestGateway(t, governor.Budget{}, &fakeProvider{name: "anthropic", err: io.ErrUnexpectedEOF})
	w := postChat(g, "", sonnetBody)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestMessages_Success(t *testing.T) {
	g := newTestGateway(t, governor.Budget{}, &fakeProvider{name: "anthropic", resp: sonnetResp()})
	r := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	g.handleMessages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp antMessagesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Type != "message" || len(resp.Content) != 1 || resp.Content[0].Text != "hello" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Usage.InputTokens != 1000 || resp.Usage.OutputTokens != 1000 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestChatCompletions_EmitsOTelSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tracer := otel.NewWithProcessor(sr, "test")

	reg, _ := provider.NewRegistry("anthropic", &fakeProvider{name: "anthropic", resp: sonnetResp()})
	mgr := runs.NewManager(governor.Budget{Tokens: 5000})
	g := New(reg, mgr, pricing.NewTable(nil), tracer, slog.New(slog.NewTextHandler(io.Discard, nil)))

	w := postChat(g, "trace-run", sonnetBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	a := map[string]string{}
	ai := map[string]int64{}
	af := map[string]float64{}
	for _, kv := range spans[0].Attributes() {
		switch kv.Value.Type().String() {
		case "INT64":
			ai[string(kv.Key)] = kv.Value.AsInt64()
		case "FLOAT64":
			af[string(kv.Key)] = kv.Value.AsFloat64()
		default:
			a[string(kv.Key)] = kv.Value.AsString()
		}
	}
	if a["riskkernel.run.id"] != "trace-run" || a["gen_ai.system"] != "anthropic" {
		t.Errorf("span attrs = %v", a)
	}
	if ai["gen_ai.usage.input_tokens"] != 1000 || ai["gen_ai.usage.output_tokens"] != 1000 {
		t.Errorf("usage attrs = %v", ai)
	}
	// Budget 5000, used 2000 → remaining 3000.
	if ai["riskkernel.budget.tokens.limit"] != 5000 || ai["riskkernel.budget.tokens.remaining"] != 3000 {
		t.Errorf("budget attrs = %v", ai)
	}
	if c := af["riskkernel.cost.usd"]; c < 0.0179 || c > 0.0181 {
		t.Errorf("cost attr = %v, want ~0.018", c)
	}
}

func TestRouteModel(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-5": "anthropic",
		"gpt-4o":            "openai",
		"o1-preview":        "openai",
		"mystery-model":     "",
	}
	for model, want := range cases {
		if got := routeModel(model); got != want {
			t.Errorf("routeModel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestDecodeContent(t *testing.T) {
	if got := decodeContent(json.RawMessage(`"plain"`)); got != "plain" {
		t.Errorf("string content = %q", got)
	}
	blocks := json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)
	if got := decodeContent(blocks); got != "ab" {
		t.Errorf("block content = %q", got)
	}
	if got := decodeContent(nil); got != "" {
		t.Errorf("nil content = %q", got)
	}
}
