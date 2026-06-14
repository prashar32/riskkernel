// Package gateway implements Surface 1 — the zero-code on-ramp. It exposes an
// OpenAI-compatible endpoint (/v1/chat/completions) and an Anthropic-compatible
// endpoint (/v1/messages). A user changes ONE env var
// (OPENAI_BASE_URL=http://localhost:7070/v1) and every call is intercepted:
// tagged with a run-id/step-id, governed by the deterministic budget enforcer,
// priced into the cost ledger, and forwarded to the real provider with the
// user's key.
//
// Streaming (`stream:true`) is supported on both endpoints (OpenAI
// /v1/chat/completions and Anthropic /v1/messages): the budget is enforced before
// the stream opens, the provider's SSE is forwarded to the client verbatim while
// token usage is metered from it, and the run's context (time budget / kill switch
// / client disconnect) cuts a live stream. A provider whose backend doesn't
// implement streaming rejects a stream request with a clear error rather than
// silently degrading.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/otel"
	"github.com/prashar32/riskkernel/internal/pricing"
	"github.com/prashar32/riskkernel/internal/provider"
	"github.com/prashar32/riskkernel/internal/runs"
)

// Middleware wraps a handler (e.g. with auth). Identity is a no-op default.
type Middleware func(http.HandlerFunc) http.HandlerFunc

// Run-grouping request header and the response headers the gateway stamps onto
// every governed call. These names are part of the proxy's stable surface.
const (
	HeaderRunID      = "X-RiskKernel-Run-Id"
	headerStep       = "X-RiskKernel-Step"
	headerCostUSD    = "X-RiskKernel-Cost-Usd"
	headerTokens     = "X-RiskKernel-Tokens"
	headerHaltReason = "X-RiskKernel-Halt-Reason"
)

// Gateway holds the dependencies for the proxy handlers.
type Gateway struct {
	providers *provider.Registry
	runs      *runs.Manager
	prices    *pricing.Table
	tracer    *otel.Tracer
	log       *slog.Logger
}

// New constructs a Gateway. tracer may be otel.Disabled() to skip span export.
func New(providers *provider.Registry, mgr *runs.Manager, prices *pricing.Table, tracer *otel.Tracer, log *slog.Logger) *Gateway {
	if tracer == nil {
		tracer = otel.Disabled()
	}
	return &Gateway{providers: providers, runs: mgr, prices: prices, tracer: tracer, log: log}
}

// Register mounts the proxy routes on mux, each wrapped by mw (e.g. auth). A nil
// mw means no wrapping.
func (g *Gateway) Register(mux *http.ServeMux, mw Middleware) {
	if mw == nil {
		mw = func(h http.HandlerFunc) http.HandlerFunc { return h }
	}
	mux.HandleFunc("POST /v1/chat/completions", mw(g.handleChatCompletions))
	mux.HandleFunc("POST /v1/messages", mw(g.handleMessages))
}

// gwError is an internal error carrying an HTTP status and api/v1 Error fields.
type gwError struct {
	status  int
	code    string
	message string
}

// callMeta is the governance bookkeeping returned alongside a successful call.
type callMeta struct {
	step   int32
	cost   float64
	priced bool
	halt   governor.HaltReason // non-empty if THIS call exhausted a budget
}

// governedCall runs the full deterministic governance cycle around one provider
// call: begin step (loop/time budget) → pre-call hard ceiling → route by model →
// forward → price → record usage. The provider call's context is cancelled if the
// run is killed/expires OR the client disconnects.
func (g *Gateway) governedCall(httpReq *http.Request, run *runs.Run, preq provider.Request) (*provider.Response, callMeta, *gwError) {
	step, err := run.BeginStep()
	if err != nil {
		return nil, callMeta{}, budgetError(err)
	}
	if err := run.CanProceed(); err != nil {
		return nil, callMeta{}, budgetError(err)
	}

	provName := routeModel(preq.Model)
	prov, err := g.providers.Get(provName)
	if err != nil {
		return nil, callMeta{}, &gwError{http.StatusBadRequest, "unknown_provider", err.Error()}
	}

	// The call dies if the run is governed-cancelled/expired (parent) or the
	// client goes away.
	callCtx, cancel := context.WithCancel(run.Context())
	defer cancel()
	stop := context.AfterFunc(httpReq.Context(), cancel)
	defer stop()

	start := time.Now()
	resp, err := prov.Chat(callCtx, preq)
	end := time.Now()
	if err != nil {
		// Emit an error span for the attempted call, then translate the error.
		g.tracer.RecordCall(context.Background(), otel.Call{
			RunID: run.ID, StepIndex: step, Provider: prov.Name(), Operation: "chat",
			RequestModel: preq.Model, MaxTokens: preq.MaxTokens, Temperature: preq.Temperature,
			Err: err, Start: start, End: end,
		})
		// If the governor halted mid-call (time budget / kill switch), report that
		// rather than a generic provider error.
		if run.Halted() {
			return nil, callMeta{}, haltGWError(run.HaltReason())
		}
		return nil, callMeta{}, &gwError{http.StatusBadGateway, "provider_error", err.Error()}
	}

	cost, priced := g.prices.Cost(resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)

	meta := callMeta{step: step, cost: cost, priced: priced}
	// The call already happened and was paid for — never discard its result. If
	// this usage exhausted a budget, surface the halt via a header so the NEXT
	// call is refused, but return the response the user paid for. RecordCall also
	// writes the auditable ledger entry through to storage.
	recErr := run.RecordCall(runs.Call{
		StepIndex:        step,
		Provider:         prov.Name(),
		Model:            resp.Model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		Dollars:          cost,
		Priced:           priced,
		ResponseID:       resp.ID,
	})
	if recErr != nil {
		meta.halt = haltReasonOf(recErr)
	}

	// Emit the GenAI span with the pinned attribute set (Surface 3).
	g.emitCallSpan(run, step, prov.Name(), preq, resp, cost, priced, meta.halt, start, end)
	return resp, meta, nil
}

// emitCallSpan exports a gen_ai.* + riskkernel.* span for a successful call,
// computing remaining budget from the run's post-call view.
func (g *Gateway) emitCallSpan(run *runs.Run, step int32, providerName string, preq provider.Request, resp *provider.Response, cost float64, priced bool, halt governor.HaltReason, start, end time.Time) {
	if !g.tracer.Enabled() {
		return
	}
	v := run.View()
	call := otel.Call{
		RunID: run.ID, StepIndex: step, Provider: providerName, Operation: "chat",
		RequestModel: preq.Model, ResponseModel: resp.Model,
		MaxTokens: preq.MaxTokens, Temperature: preq.Temperature,
		PromptTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens,
		CostUSD: cost, Priced: priced,
		FinishReason: resp.FinishReason, ResponseID: resp.ID,
		HaltReason: string(halt), Start: start, End: end,
	}
	if v.Budget.Tokens > 0 {
		call.BudgetTokensLimit = v.Budget.Tokens
		call.BudgetTokensRemaining = max64(0, v.Budget.Tokens-v.Usage.Tokens())
	}
	if v.Budget.Dollars > 0 {
		call.BudgetDollarsLimit = v.Budget.Dollars
		call.BudgetDollarsRemaining = maxF(0, v.Budget.Dollars-v.Usage.Dollars)
	}
	g.tracer.RecordCall(context.Background(), call)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// streamCall proxies a streaming completion: enforce the budget before opening the
// stream, forward the provider's SSE chunks verbatim to the client (flushing each),
// and meter the call from the stream's final usage. The run's context (time budget
// / kill switch) and client disconnect cut a live stream. Only providers that
// implement provider.Streamer support this; others get a clear 501. Dollar/token
// budgets are checked before the stream and recorded after (so the next call is
// refused if over); the time budget and kill switch cut mid-stream via the context.
func (g *Gateway) streamCall(w http.ResponseWriter, r *http.Request, run *runs.Run, preq provider.Request) {
	step, err := run.BeginStep()
	if err != nil {
		budgetError(err).write(w)
		return
	}
	if err := run.CanProceed(); err != nil {
		budgetError(err).write(w)
		return
	}

	prov, err := g.providers.Get(routeModel(preq.Model))
	if err != nil {
		(&gwError{http.StatusBadRequest, "unknown_provider", err.Error()}).write(w)
		return
	}
	streamer, ok := prov.(provider.Streamer)
	if !ok {
		(&gwError{http.StatusNotImplemented, "streaming_unsupported",
			"streaming is not supported for provider " + prov.Name() + "; set stream:false"}).write(w)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		(&gwError{http.StatusInternalServerError, "internal_error", "response writer does not support streaming"}).write(w)
		return
	}

	// The stream dies if the run is governed-cancelled/expired (parent) or the
	// client goes away.
	callCtx, cancel := context.WithCancel(run.Context())
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()

	start := time.Now()
	stream, serr := streamer.ChatStream(callCtx, preq)
	if serr != nil {
		if run.Halted() {
			haltGWError(run.HaltReason()).write(w)
			return
		}
		(&gwError{http.StatusBadGateway, "provider_error", serr.Error()}).write(w)
		return
	}
	defer stream.Close()

	// Past here the response is committed (200 + SSE); all budget pre-checks passed.
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set(HeaderRunID, run.ID)
	h.Set(headerStep, strconv.Itoa(int(step)))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		chunk, rerr := stream.Recv()
		if len(chunk) > 0 {
			if _, werr := w.Write(chunk); werr != nil {
				break // client disconnected
			}
			flusher.Flush()
		}
		if rerr != nil {
			break // io.EOF (clean end) or an upstream/context error (truncates the stream)
		}
	}
	end := time.Now()

	// Meter the (possibly truncated) call from the usage the stream reported, so the
	// ledger and budget reflect it and the next call is refused if it went over.
	model := stream.Model()
	if model == "" {
		model = preq.Model
	}
	usage := stream.Usage()
	cost, priced := g.prices.Cost(model, usage.PromptTokens, usage.CompletionTokens)
	_ = run.RecordCall(runs.Call{
		StepIndex: step, Provider: prov.Name(), Model: model,
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		Dollars: cost, Priced: priced,
	})
	g.emitCallSpan(run, step, prov.Name(), preq, &provider.Response{Model: model, Usage: usage}, cost, priced, run.HaltReason(), start, end)
}

// stampHeaders writes the governance headers onto a successful proxied response.
func stampHeaders(w http.ResponseWriter, run *runs.Run, resp *provider.Response, meta callMeta) {
	w.Header().Set(HeaderRunID, run.ID)
	w.Header().Set(headerStep, strconv.Itoa(int(meta.step)))
	w.Header().Set(headerCostUSD, strconv.FormatFloat(meta.cost, 'f', 6, 64))
	w.Header().Set(headerTokens, strconv.FormatInt(resp.Usage.Total(), 10))
	if meta.halt != governor.HaltNone {
		w.Header().Set(headerHaltReason, string(meta.halt))
	}
}

// resolveRun returns the run a request belongs to: the one named by the run-id
// header (lazily created under the default budget), or a fresh ephemeral run for
// an unGrouped single call.
func (g *Gateway) resolveRun(httpReq *http.Request) *runs.Run {
	if rid := httpReq.Header.Get(HeaderRunID); rid != "" {
		return g.runs.GetOrCreate(rid)
	}
	return g.runs.Create(runs.CreateOptions{Name: "proxy"})
}

// routeModel maps a model id to a provider name by prefix. An empty result routes
// to the registry's default provider. Anthropic and OpenAI are implemented
// natively; front the long tail with LiteLLM as an upstream forwarder.
func routeModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"):
		return "openai"
	default:
		return "" // default provider
	}
}

func (g *gwError) write(w http.ResponseWriter) {
	httpx.WriteError(w, g.status, g.code, g.message)
}

// budgetError converts a *governor.HaltError into a 402 gwError, or returns a
// generic 500 if err is not a halt (should not happen on the governance path).
func budgetError(err error) *gwError {
	return haltGWError(haltReasonOf(err))
}

func haltGWError(reason governor.HaltReason) *gwError {
	if reason == governor.HaltNone {
		return &gwError{http.StatusInternalServerError, "internal_error", "run not runnable"}
	}
	return &gwError{
		status:  http.StatusPaymentRequired, // 402: the run is out of budget
		code:    string(reason),
		message: "run halted: " + string(reason),
	}
}

func haltReasonOf(err error) governor.HaltReason {
	var he *governor.HaltError
	if errors.As(err, &he) {
		return he.Reason
	}
	return governor.HaltNone
}

// --- shared body helpers ---

// decodeContent extracts text from a message content field that may be a plain
// string or an array of typed blocks (OpenAI and Anthropic both allow both).
func decodeContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			sb.WriteString(b.Text)
		}
		return sb.String()
	}
	return ""
}

func readBody(r *http.Request, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, max))
}

const maxBodyBytes = 10 << 20 // 10 MiB

func unixNow() int64 { return time.Now().Unix() }
