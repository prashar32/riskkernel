package otel

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/pricing"
	"github.com/prashar32/riskkernel/internal/runs"
)

// --- helpers to build OTLP spans ---

func kvStr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
func kvInt(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}
func kvDouble(k string, v float64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}}
}

// export wraps spans into an ExportTraceServiceRequest, optionally with
// resource-level attributes.
func export(resAttrs []*commonpb.KeyValue, spans ...*tracepb.Span) *coltracepb.ExportTraceServiceRequest {
	var res *resourcepb.Resource
	if resAttrs != nil {
		res = &resourcepb.Resource{Attributes: resAttrs}
	}
	return &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource:   res,
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}},
	}
}

func newIngress(t *testing.T, budget governor.Budget, prices *pricing.Table) (*Ingress, *runs.Manager) {
	t.Helper()
	mgr := runs.NewManager(budget)
	return NewIngress(mgr, prices, slog.New(slog.NewTextHandler(io.Discard, nil))), mgr
}

// --- tests ---

func TestIngress_MetersGenAISpanToRun(t *testing.T) {
	in, mgr := newIngress(t, governor.Budget{Tokens: 100000}, pricing.NewTable(nil))

	span := &tracepb.Span{
		Name: "chat claude-sonnet-4-5",
		Attributes: []*commonpb.KeyValue{
			kvStr(attrRunID, "run-1"),
			kvStr(attrGenAISystem, "anthropic"),
			kvStr(attrGenAIResponseModel, "claude-sonnet-4-5-20250101"),
			kvInt(attrGenAIInputTokens, 100),
			kvInt(attrGenAIOutputTokens, 50),
		},
	}
	res := in.ingest(export(nil, span))
	if res.metered != 1 || res.unattributed != 0 {
		t.Fatalf("ingest result = %+v, want metered=1", res)
	}

	run, ok := mgr.Get("run-1")
	if !ok {
		t.Fatal("run-1 was not created/metered")
	}
	v := run.View()
	if v.Usage.PromptTokens != 100 || v.Usage.CompletionTokens != 50 {
		t.Fatalf("usage = %+v, want 100/50", v.Usage)
	}
	if v.Usage.Tokens() != 150 {
		t.Fatalf("total tokens = %d, want 150", v.Usage.Tokens())
	}
}

func TestIngress_PricesConsumedCall(t *testing.T) {
	// A model with a known rate must produce a non-zero, priced cost in the ledger.
	prices := pricing.NewTable(map[string]pricing.Rate{
		"gpt-4o": {InputPerM: 5, OutputPerM: 15}, // $ per 1M tokens
	})
	in, mgr := newIngress(t, governor.Budget{}, prices)

	span := &tracepb.Span{Attributes: []*commonpb.KeyValue{
		kvStr(attrRunID, "r"),
		kvStr(attrGenAISystem, "openai"),
		kvStr(attrGenAIRequestModel, "gpt-4o"),
		kvInt(attrGenAIInputTokens, 1_000_000),
		kvInt(attrGenAIOutputTokens, 0),
	}}
	in.ingest(export(nil, span))

	run, _ := mgr.Get("r")
	if got := run.View().Usage.Dollars; got != 5.0 {
		t.Fatalf("metered dollars = %v, want 5.0", got)
	}
}

func TestIngress_RunIDFromResourceAttributes(t *testing.T) {
	// Some instrumenters set riskkernel.run.id once on the resource, not per span.
	in, mgr := newIngress(t, governor.Budget{}, pricing.NewTable(nil))
	span := &tracepb.Span{Attributes: []*commonpb.KeyValue{
		kvStr(attrGenAIRequestModel, "claude-sonnet-4-5"),
		kvInt(attrGenAIInputTokens, 10),
		kvInt(attrGenAIOutputTokens, 5),
	}}
	res := in.ingest(export([]*commonpb.KeyValue{kvStr(attrRunID, "res-run")}, span))
	if res.metered != 1 {
		t.Fatalf("metered = %d, want 1 (run id from resource)", res.metered)
	}
	if _, ok := mgr.Get("res-run"); !ok {
		t.Fatal("run from resource attribute was not metered")
	}
}

func TestIngress_SkipsNonGenAISpansAndUnattributed(t *testing.T) {
	in, mgr := newIngress(t, governor.Budget{}, pricing.NewTable(nil))

	// A non-GenAI span (no usage) — ignored entirely.
	toolSpan := &tracepb.Span{Name: "execute_tool write_file", Attributes: []*commonpb.KeyValue{
		kvStr(attrRunID, "r"), kvStr("gen_ai.tool.name", "write_file"),
	}}
	// A GenAI span with usage but NO run id — observed but not metered.
	orphan := &tracepb.Span{Attributes: []*commonpb.KeyValue{
		kvStr(attrGenAIRequestModel, "gpt-4o"),
		kvInt(attrGenAIInputTokens, 10), kvInt(attrGenAIOutputTokens, 2),
	}}
	res := in.ingest(export(nil, toolSpan, orphan))
	if res.metered != 0 {
		t.Fatalf("metered = %d, want 0", res.metered)
	}
	if res.unattributed != 1 {
		t.Fatalf("unattributed = %d, want 1", res.unattributed)
	}
	if _, ok := mgr.Get("r"); ok {
		t.Fatal("the tool span must not create a run")
	}
}

func TestIngress_LenientNumericAttrTypes(t *testing.T) {
	// Token counts emitted as a double or a numeric string must still be metered.
	in, mgr := newIngress(t, governor.Budget{}, pricing.NewTable(nil))
	span := &tracepb.Span{Attributes: []*commonpb.KeyValue{
		kvStr(attrRunID, "r"),
		kvStr(attrGenAIRequestModel, "m"),
		kvDouble(attrGenAIInputTokens, 42),
		kvStr(attrGenAIOutputTokens, "8"),
	}}
	in.ingest(export(nil, span))
	v, _ := mgr.Get("r")
	if u := v.View().Usage; u.PromptTokens != 42 || u.CompletionTokens != 8 {
		t.Fatalf("usage = %+v, want 42/8 (double + string parsed)", u)
	}
}

func TestIngress_HandleTraces_Protobuf(t *testing.T) {
	in, mgr := newIngress(t, governor.Budget{Tokens: 1000}, pricing.NewTable(nil))
	span := &tracepb.Span{Attributes: []*commonpb.KeyValue{
		kvStr(attrRunID, "http-run"),
		kvStr(attrGenAIResponseModel, "claude"),
		kvInt(attrGenAIInputTokens, 20), kvInt(attrGenAIOutputTokens, 5),
	}}
	body, err := proto.Marshal(export(nil, span))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	in.HandleTraces(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-protobuf" {
		t.Errorf("response content-type = %q", ct)
	}
	var resp coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not a valid OTLP protobuf: %v", err)
	}
	if resp.GetPartialSuccess().GetRejectedSpans() != 0 {
		t.Errorf("unexpected rejected spans: %d", resp.GetPartialSuccess().GetRejectedSpans())
	}
	if _, ok := mgr.Get("http-run"); !ok {
		t.Fatal("span POSTed over HTTP was not metered")
	}
}

func TestIngress_HandleTraces_JSONPartialSuccess(t *testing.T) {
	in, _ := newIngress(t, governor.Budget{}, pricing.NewTable(nil))
	// A GenAI usage span with no run id → reported as a rejected (unattributed) span.
	orphan := &tracepb.Span{Attributes: []*commonpb.KeyValue{
		kvStr(attrGenAIRequestModel, "m"),
		kvInt(attrGenAIInputTokens, 1), kvInt(attrGenAIOutputTokens, 1),
	}}
	body, err := protojson.Marshal(export(nil, orphan))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	in.HandleTraces(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("response content-type = %q", ct)
	}
	var resp coltracepb.ExportTraceServiceResponse
	if err := protojson.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid OTLP JSON: %v", err)
	}
	if resp.GetPartialSuccess().GetRejectedSpans() != 1 {
		t.Errorf("rejected spans = %d, want 1", resp.GetPartialSuccess().GetRejectedSpans())
	}
}

func TestIngress_HandleTraces_BadBody(t *testing.T) {
	in, _ := newIngress(t, governor.Budget{}, pricing.NewTable(nil))
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte("not protobuf")))
	r.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	in.HandleTraces(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
