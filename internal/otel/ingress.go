package otel

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/prashar32/riskkernel/internal/pricing"
	"github.com/prashar32/riskkernel/internal/runs"
)

// maxTraceBodyBytes caps a single OTLP export payload (a generous batch of spans).
// The ingress reads the same pinned attribute keys the egress emits (attrRunID,
// attrGenAI*) — defined in otel.go — so consume and emit stay in lockstep.
const maxTraceBodyBytes = 8 << 20 // 8 MiB

// Ingress is the OTLP/HTTP trace receiver — the consume side of Surface 3. It
// accepts GenAI spans from apps already instrumented (OpenLLMetry, the OpenAI
// Agents SDK, the Vercel AI SDK), correlates each model-call span to a governed
// run by riskkernel.run.id, and meters its token usage and cost into the ledger.
// This lets RiskKernel make spend visible for apps it never directly proxied.
//
// Scope is observe + meter: a consumed span records against the run's ledger but
// does not retroactively block a call that already happened (governing consumed
// spans is a separate, future step). The receiver is off by default and mounted
// only when RISKKERNEL_OTEL_INGRESS_ENABLED is set.
type Ingress struct {
	runs   *runs.Manager
	prices *pricing.Table
	log    *slog.Logger
}

// NewIngress constructs the OTLP trace receiver. mgr and prices must be non-nil.
func NewIngress(mgr *runs.Manager, prices *pricing.Table, log *slog.Logger) *Ingress {
	return &Ingress{runs: mgr, prices: prices, log: log}
}

// ingestResult tallies one export for the OTLP partial-success reply and logging.
type ingestResult struct {
	metered      int // GenAI call spans metered against a run
	unattributed int // GenAI call spans with no riskkernel.run.id (observed, not metered)
}

// HandleTraces implements POST /v1/traces, the standard OTLP/HTTP traces path —
// point any OTLP exporter (OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:7070) at
// it. It accepts protobuf (application/x-protobuf, the OTLP default) or JSON
// (application/json), meters the GenAI model-call spans it recognizes, and replies
// with an OTLP ExportTraceServiceResponse in the request's encoding.
func (in *Ingress) HandleTraces(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTraceBodyBytes))
	if err != nil {
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}

	isJSON := strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
	var req coltracepb.ExportTraceServiceRequest
	if isJSON {
		if err := protojson.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid OTLP JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		// An unset or application/x-protobuf content type decodes as protobuf, the
		// OTLP default.
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid OTLP protobuf: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	res := in.ingest(&req)
	if in.log != nil && (res.metered > 0 || res.unattributed > 0) {
		in.log.Debug("otlp trace ingress", "metered", res.metered, "unattributed", res.unattributed)
	}

	// OTLP partial success: spans we couldn't attribute to a run are still a 200
	// (partial success is not an error), but reported as rejected so the caller can
	// see they were observed and not metered.
	resp := &coltracepb.ExportTraceServiceResponse{}
	if res.unattributed > 0 {
		resp.PartialSuccess = &coltracepb.ExportTracePartialSuccess{
			RejectedSpans: int64(res.unattributed),
			ErrorMessage:  "GenAI spans without riskkernel.run.id were observed but not metered to a run",
		}
	}
	writeOTLPResponse(w, isJSON, resp)
}

// ingest walks the export and meters each recognized GenAI model-call span against
// its run. The run id is taken from the span, falling back to its resource (some
// instrumenters set riskkernel.run.id once on the resource).
func (in *Ingress) ingest(req *coltracepb.ExportTraceServiceRequest) ingestResult {
	var res ingestResult
	for _, rs := range req.GetResourceSpans() {
		resAttrs := otlpAttrMap(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				attrs := otlpAttrMap(span.GetAttributes())
				// A GenAI model call is identified by carrying token usage; spans
				// without usage (tool calls, retrieval, framework spans) are ignored
				// for metering.
				if _, hasIn := attrs[attrGenAIInputTokens]; !hasIn {
					if _, hasOut := attrs[attrGenAIOutputTokens]; !hasOut {
						continue
					}
				}
				runID := otlpStringAttr(attrs, attrRunID)
				if runID == "" {
					runID = otlpStringAttr(resAttrs, attrRunID)
				}
				if runID == "" {
					res.unattributed++
					continue
				}
				in.meter(runID, attrs)
				res.metered++
			}
		}
	}
	return res
}

// meter records one consumed model call against its run's ledger. The run is
// created lazily under the default budget if unknown — mirroring how the proxy
// resolves a run from the run-id header — so spend shows up against a run either
// way. A budget crossed by the recorded usage marks the run halted (visible in
// runs list / audit); it cannot un-spend a call that already happened.
func (in *Ingress) meter(runID string, attrs map[string]*commonpb.AnyValue) {
	model := otlpStringAttr(attrs, attrGenAIResponseModel)
	if model == "" {
		model = otlpStringAttr(attrs, attrGenAIRequestModel)
	}
	inTok := otlpIntAttr(attrs, attrGenAIInputTokens)
	outTok := otlpIntAttr(attrs, attrGenAIOutputTokens)
	cost, priced := in.prices.Cost(model, inTok, outTok)

	run := in.runs.GetOrCreate(runID)
	_ = run.RecordCall(runs.Call{
		Provider:         otlpStringAttr(attrs, attrGenAISystem),
		Model:            model,
		PromptTokens:     inTok,
		CompletionTokens: outTok,
		Dollars:          cost,
		Priced:           priced,
		ResponseID:       otlpStringAttr(attrs, attrGenAIResponseID),
	})
}

// writeOTLPResponse replies with an ExportTraceServiceResponse in the request's
// encoding, as the OTLP/HTTP spec requires.
func writeOTLPResponse(w http.ResponseWriter, isJSON bool, resp *coltracepb.ExportTraceServiceResponse) {
	var (
		out []byte
		err error
		ct  string
	)
	if isJSON {
		out, err = protojson.Marshal(resp)
		ct = "application/json"
	} else {
		out, err = proto.Marshal(resp)
		ct = "application/x-protobuf"
	}
	if err != nil {
		http.Error(w, "encoding OTLP response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// otlpAttrMap indexes a span's (or resource's) attributes by key for lookup.
func otlpAttrMap(kvs []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	m := make(map[string]*commonpb.AnyValue, len(kvs))
	for _, kv := range kvs {
		if kv != nil {
			m[kv.GetKey()] = kv.GetValue()
		}
	}
	return m
}

// otlpStringAttr returns a string attribute, or "" if absent or not a string.
func otlpStringAttr(m map[string]*commonpb.AnyValue, key string) string {
	if v, ok := m[key].GetValue().(*commonpb.AnyValue_StringValue); ok {
		return v.StringValue
	}
	return ""
}

// otlpIntAttr returns an integer attribute. Token counts are ints per the convention,
// but some instrumenters emit them as doubles or numeric strings, so accept those
// too rather than silently dropping the usage to zero.
func otlpIntAttr(m map[string]*commonpb.AnyValue, key string) int64 {
	switch v := m[key].GetValue().(type) {
	case *commonpb.AnyValue_IntValue:
		return v.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return int64(v.DoubleValue)
	case *commonpb.AnyValue_StringValue:
		n, _ := strconv.ParseInt(strings.TrimSpace(v.StringValue), 10, 64)
		return n
	default:
		return 0
	}
}
