// Package otel implements Surface 3 — OpenTelemetry GenAI export. It emits one
// span per governed model call carrying the attribute set pinned in
// api/v1/otel-genai.md (gen_ai.* + riskkernel.*), so a run becomes observable in
// whatever OTLP backend the user already runs (Grafana Tempo, SigNoz, Jaeger,
// Honeycomb, Datadog, …).
//
// It is OFF unless the user configures an OTLP endpoint. RiskKernel never emits
// telemetry on its own — spans go only to the endpoint the user points it at.
// This is the only package besides internal/provider permitted outbound network.
package otel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/prashar32/riskkernel/internal/config"
)

// Pinned attribute keys (api/v1/otel-genai.md). Stable per COMPATIBILITY.md.
const (
	attrGenAISystem        = "gen_ai.system"
	attrGenAIOperation     = "gen_ai.operation.name"
	attrGenAIRequestModel  = "gen_ai.request.model"
	attrGenAIResponseModel = "gen_ai.response.model"
	attrGenAIMaxTokens     = "gen_ai.request.max_tokens"
	attrGenAITemperature   = "gen_ai.request.temperature"
	attrGenAIInputTokens   = "gen_ai.usage.input_tokens"
	attrGenAIOutputTokens  = "gen_ai.usage.output_tokens"
	attrGenAIFinishReasons = "gen_ai.response.finish_reasons"
	attrGenAIResponseID    = "gen_ai.response.id"
	attrErrorType          = "error.type"

	attrRunID           = "riskkernel.run.id"
	attrStepIndex       = "riskkernel.step.index"
	attrCostUSD         = "riskkernel.cost.usd"
	attrBudgetTokLimit  = "riskkernel.budget.tokens.limit"
	attrBudgetTokRemain = "riskkernel.budget.tokens.remaining"
	attrBudgetDolLimit  = "riskkernel.budget.dollars.limit"
	attrBudgetDolRemain = "riskkernel.budget.dollars.remaining"
	attrHaltReason      = "riskkernel.halt.reason"

	attrGenAIToolName  = "gen_ai.tool.name"
	attrToolSideEffect = "riskkernel.tool.side_effect"
	attrToolStatus     = "riskkernel.tool.status" // approved | blocked | denied | timeout
)

// Tracer emits governed-call spans. The zero value and Disabled() are safe no-ops.
type Tracer struct {
	enabled bool
	tracer  trace.Tracer
	tp      *sdktrace.TracerProvider
}

// Disabled returns a no-op Tracer (used when no OTLP endpoint is configured and
// in tests).
func Disabled() *Tracer { return &Tracer{} }

// New builds a Tracer. If cfg.Endpoint is empty it returns a disabled no-op
// Tracer (no exporter, no network). Otherwise it wires an OTLP exporter (grpc or
// http) to the configured endpoint.
func New(ctx context.Context, cfg config.OTelConfig, log *slog.Logger) (*Tracer, error) {
	if cfg.Endpoint == "" {
		log.Info("otel export disabled (no OTLP endpoint configured)")
		return Disabled(), nil
	}

	exp, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("otel: build exporter: %w", err)
	}

	res := resource.NewSchemaless(attribute.String("service.name", cfg.ServiceName))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	log.Info("otel export enabled", "endpoint", cfg.Endpoint, "protocol", cfg.Protocol)
	return &Tracer{
		enabled: true,
		tracer:  tp.Tracer("github.com/prashar32/riskkernel"),
		tp:      tp,
	}, nil
}

// NewWithProcessor builds an enabled Tracer around a caller-supplied span
// processor — useful for custom pipelines and for tests (e.g. an in-memory span
// recorder).
func NewWithProcessor(sp sdktrace.SpanProcessor, serviceName string) *Tracer {
	res := resource.NewSchemaless(attribute.String("service.name", serviceName))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sp),
		sdktrace.WithResource(res),
	)
	return &Tracer{
		enabled: true,
		tracer:  tp.Tracer("github.com/prashar32/riskkernel"),
		tp:      tp,
	}
}

func newExporter(ctx context.Context, cfg config.OTelConfig) (*otlptrace.Exporter, error) {
	switch cfg.Protocol {
	case "http", "http/protobuf":
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		return otlptracehttp.New(ctx, opts...)
	default: // grpc
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
		}
		return otlptracegrpc.New(ctx, opts...)
	}
}

// Enabled reports whether spans are being exported.
func (t *Tracer) Enabled() bool { return t != nil && t.enabled }

// Shutdown flushes buffered spans and releases the exporter.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if !t.Enabled() {
		return nil
	}
	return t.tp.Shutdown(ctx)
}

// Call is the data for one governed model-call span.
type Call struct {
	RunID         string
	StepIndex     int32
	Provider      string
	Operation     string // e.g. "chat"
	RequestModel  string
	ResponseModel string
	MaxTokens     int
	Temperature   *float64
	PromptTokens  int64
	OutputTokens  int64
	CostUSD       float64
	Priced        bool
	FinishReason  string
	ResponseID    string

	BudgetTokensLimit      int64
	BudgetTokensRemaining  int64
	BudgetDollarsLimit     float64
	BudgetDollarsRemaining float64

	HaltReason string // empty if none
	Err        error  // non-nil if the call failed

	Start time.Time
	End   time.Time
}

// RecordCall emits a span for one governed model call. No-op when disabled.
func (t *Tracer) RecordCall(ctx context.Context, c Call) {
	if !t.Enabled() {
		return
	}
	name := c.Operation + " " + c.RequestModel

	_, span := t.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(orNow(c.Start)),
	)

	attrs := []attribute.KeyValue{
		attribute.String(attrGenAISystem, c.Provider),
		attribute.String(attrGenAIOperation, c.Operation),
		attribute.String(attrGenAIRequestModel, c.RequestModel),
		attribute.String(attrRunID, c.RunID),
		attribute.Int(attrStepIndex, int(c.StepIndex)),
	}
	if c.ResponseModel != "" {
		attrs = append(attrs, attribute.String(attrGenAIResponseModel, c.ResponseModel))
	}
	if c.MaxTokens > 0 {
		attrs = append(attrs, attribute.Int(attrGenAIMaxTokens, c.MaxTokens))
	}
	if c.Temperature != nil {
		attrs = append(attrs, attribute.Float64(attrGenAITemperature, *c.Temperature))
	}
	if c.Err == nil {
		attrs = append(attrs,
			attribute.Int64(attrGenAIInputTokens, c.PromptTokens),
			attribute.Int64(attrGenAIOutputTokens, c.OutputTokens),
		)
		if c.FinishReason != "" {
			attrs = append(attrs, attribute.StringSlice(attrGenAIFinishReasons, []string{c.FinishReason}))
		}
		if c.ResponseID != "" {
			attrs = append(attrs, attribute.String(attrGenAIResponseID, c.ResponseID))
		}
		if c.Priced {
			attrs = append(attrs, attribute.Float64(attrCostUSD, c.CostUSD))
		}
	}

	if c.BudgetTokensLimit > 0 {
		attrs = append(attrs,
			attribute.Int64(attrBudgetTokLimit, c.BudgetTokensLimit),
			attribute.Int64(attrBudgetTokRemain, c.BudgetTokensRemaining),
		)
	}
	if c.BudgetDollarsLimit > 0 {
		attrs = append(attrs,
			attribute.Float64(attrBudgetDolLimit, c.BudgetDollarsLimit),
			attribute.Float64(attrBudgetDolRemain, c.BudgetDollarsRemaining),
		)
	}
	if c.HaltReason != "" {
		attrs = append(attrs, attribute.String(attrHaltReason, c.HaltReason))
	}
	span.SetAttributes(attrs...)

	if c.Err != nil {
		span.SetAttributes(attribute.String(attrErrorType, "provider_error"))
		span.RecordError(c.Err)
		span.SetStatus(codes.Error, c.Err.Error())
	}

	span.End(trace.WithTimestamp(orNow(c.End)))
}

// ToolCall is a governed MCP tool call to record as a span.
type ToolCall struct {
	RunID      string
	StepIndex  int32
	Tool       string
	SideEffect string // "" for read-only
	Status     string // approved | blocked | denied | timeout
	Start      time.Time
	End        time.Time
}

// RecordToolCall emits a span for one governed MCP tool call, so tool governance —
// allowlist blocks, approval denials, approved calls — is visible alongside model
// calls in the user's OTLP backend. No-op when disabled.
func (t *Tracer) RecordToolCall(ctx context.Context, tc ToolCall) {
	if !t.Enabled() {
		return
	}
	_, span := t.tracer.Start(ctx, "execute_tool "+tc.Tool,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(orNow(tc.Start)),
	)
	attrs := []attribute.KeyValue{
		attribute.String(attrGenAIOperation, "execute_tool"),
		attribute.String(attrGenAIToolName, tc.Tool),
		attribute.String(attrRunID, tc.RunID),
		attribute.Int(attrStepIndex, int(tc.StepIndex)),
		attribute.String(attrToolStatus, tc.Status),
	}
	if tc.SideEffect != "" {
		attrs = append(attrs, attribute.String(attrToolSideEffect, tc.SideEffect))
	}
	span.SetAttributes(attrs...)
	// A refused call (blocked / denied / timeout) is an error status so it stands
	// out in the trace UI.
	if tc.Status != "approved" {
		span.SetStatus(codes.Error, "tool "+tc.Status)
	}
	span.End(trace.WithTimestamp(orNow(tc.End)))
}

func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
