package otel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/prashar32/riskkernel/internal/config"
)

// newRecording builds a Tracer backed by an in-memory span recorder.
func newRecording() (*Tracer, *tracetest.SpanRecorder) {
	sr := tracetest.NewSpanRecorder()
	return NewWithProcessor(sr, "test"), sr
}

func attrMap(kvs []attribute.KeyValue) map[string]attribute.Value {
	m := make(map[string]attribute.Value, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func TestRecordCall_Attributes(t *testing.T) {
	tr, sr := newRecording()

	tr.RecordCall(context.Background(), Call{
		RunID: "r1", StepIndex: 2, Provider: "anthropic", Operation: "chat",
		RequestModel: "claude-sonnet-4-5", ResponseModel: "claude-sonnet-4-5-20250101",
		PromptTokens: 100, OutputTokens: 50, CostUSD: 0.012, Priced: true,
		FinishReason: "end_turn", ResponseID: "abc",
		BudgetTokensLimit: 1000, BudgetTokensRemaining: 850,
	})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	if s.Name() != "chat claude-sonnet-4-5" {
		t.Errorf("span name = %q", s.Name())
	}
	a := attrMap(s.Attributes())
	if a[attrGenAISystem].AsString() != "anthropic" {
		t.Errorf("gen_ai.system = %v", a[attrGenAISystem])
	}
	if a[attrGenAIInputTokens].AsInt64() != 100 || a[attrGenAIOutputTokens].AsInt64() != 50 {
		t.Errorf("usage attrs = %v / %v", a[attrGenAIInputTokens], a[attrGenAIOutputTokens])
	}
	if a[attrRunID].AsString() != "r1" || a[attrStepIndex].AsInt64() != 2 {
		t.Errorf("riskkernel run/step = %v / %v", a[attrRunID], a[attrStepIndex])
	}
	if a[attrCostUSD].AsFloat64() != 0.012 {
		t.Errorf("cost = %v", a[attrCostUSD])
	}
	if a[attrBudgetTokLimit].AsInt64() != 1000 || a[attrBudgetTokRemain].AsInt64() != 850 {
		t.Errorf("budget attrs = %v / %v", a[attrBudgetTokLimit], a[attrBudgetTokRemain])
	}
	if _, ok := a[attrHaltReason]; ok {
		t.Errorf("halt reason should be absent for a healthy call")
	}
}

func TestRecordCall_Error(t *testing.T) {
	tr, sr := newRecording()
	tr.RecordCall(context.Background(), Call{
		RunID: "r1", Provider: "anthropic", Operation: "chat", RequestModel: "m",
		Err: errors.New("boom"),
	})
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans", len(spans))
	}
	a := attrMap(spans[0].Attributes())
	if a[attrErrorType].AsString() != "provider_error" {
		t.Errorf("error.type = %v", a[attrErrorType])
	}
	// Usage attributes must be absent on a failed call.
	if _, ok := a[attrGenAIInputTokens]; ok {
		t.Errorf("input tokens should be absent on error")
	}
}

func TestRecordCall_HaltAttribute(t *testing.T) {
	tr, sr := newRecording()
	tr.RecordCall(context.Background(), Call{
		RunID: "r1", Provider: "anthropic", Operation: "chat", RequestModel: "m",
		PromptTokens: 10, OutputTokens: 5, HaltReason: "token_budget_exceeded",
	})
	a := attrMap(sr.Ended()[0].Attributes())
	if a[attrHaltReason].AsString() != "token_budget_exceeded" {
		t.Errorf("halt reason = %v", a[attrHaltReason])
	}
}

func TestRecordToolCall_Attributes(t *testing.T) {
	tr, sr := newRecording()
	tr.RecordToolCall(context.Background(), ToolCall{
		RunID: "r1", StepIndex: 3, Tool: "write_file", SideEffect: "tool", Status: "denied",
	})
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	if s.Name() != "execute_tool write_file" {
		t.Errorf("span name = %q", s.Name())
	}
	a := attrMap(s.Attributes())
	if a[attrGenAIOperation].AsString() != "execute_tool" || a[attrGenAIToolName].AsString() != "write_file" {
		t.Errorf("operation/tool = %v / %v", a[attrGenAIOperation], a[attrGenAIToolName])
	}
	if a[attrRunID].AsString() != "r1" || a[attrStepIndex].AsInt64() != 3 {
		t.Errorf("run/step = %v / %v", a[attrRunID], a[attrStepIndex])
	}
	if a[attrToolStatus].AsString() != "denied" || a[attrToolSideEffect].AsString() != "tool" {
		t.Errorf("status/side_effect = %v / %v", a[attrToolStatus], a[attrToolSideEffect])
	}
}

func TestExport_SendsConfiguredHeaders(t *testing.T) {
	// Prove the auth header reaches the OTLP endpoint on the wire — this is what
	// lets RiskKernel export to a backend that requires authentication (Honeycomb,
	// Grafana Cloud, a hosted dashboard).
	var mu sync.Mutex
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr, err := New(context.Background(), config.OTelConfig{
		Endpoint: srv.URL, Protocol: "http", Insecure: true, ServiceName: "test",
		Headers: map[string]string{"authorization": "Bearer xyz"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr.RecordCall(context.Background(), Call{RunID: "r1", Operation: "chat", RequestModel: "m", PromptTokens: 1, OutputTokens: 1})
	if err := tr.Shutdown(context.Background()); err != nil { // flushes the batch
		t.Fatalf("Shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "Bearer xyz" {
		t.Fatalf("Authorization header on the wire = %q, want %q", gotAuth, "Bearer xyz")
	}
}

func TestDisabled_NoOp(t *testing.T) {
	d := Disabled()
	if d.Enabled() {
		t.Fatal("Disabled() should not be enabled")
	}
	// Must not panic and must not export.
	d.RecordCall(context.Background(), Call{RunID: "x", Operation: "chat", RequestModel: "m"})
	if err := d.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown on disabled = %v", err)
	}
}
