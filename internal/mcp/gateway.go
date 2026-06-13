// Package mcp implements RiskKernel's MCP gateway: a JSON-RPC reverse proxy that
// sits in front of an upstream MCP server and governs tools/call. Every other MCP
// method is forwarded transparently; tools/call is intercepted to enforce a
// per-tool allowlist, route side-effecting tools through the deterministic
// approval gate, and record an auditable tool_call. Point your MCP client at this
// gateway instead of the real server — the governance is invisible to allowed,
// approved calls.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/prashar32/riskkernel/internal/approval"
	"github.com/prashar32/riskkernel/internal/id"
	"github.com/prashar32/riskkernel/internal/otel"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

// HeaderRunID groups MCP calls into a governed run (same header as the proxy).
const HeaderRunID = "X-RiskKernel-Run-Id"

// Middleware wraps a handler (e.g. with auth).
type Middleware func(http.HandlerFunc) http.HandlerFunc

// Gateway governs MCP tools/call in front of an upstream MCP server.
type Gateway struct {
	upstream        string
	client          *http.Client
	allow           []string // empty = allow all; exact name or glob
	readonly        map[string]bool
	gate            *approval.Gate
	runs            *runs.Manager
	store           storage.Store
	tracer          *otel.Tracer
	log             *slog.Logger
	approvalTimeout time.Duration
}

// New constructs an MCP gateway. upstream must be non-empty.
func New(upstream string, allowlist, readonly []string, gate *approval.Gate,
	mgr *runs.Manager, store storage.Store, tracer *otel.Tracer, approvalTimeout time.Duration, log *slog.Logger) *Gateway {
	ro := make(map[string]bool, len(readonly))
	for _, t := range readonly {
		ro[t] = true
	}
	if approvalTimeout <= 0 {
		approvalTimeout = 110 * time.Second
	}
	return &Gateway{
		upstream:        strings.TrimRight(upstream, "/"),
		client:          &http.Client{Timeout: 130 * time.Second},
		allow:           allowlist,
		readonly:        ro,
		gate:            gate,
		runs:            mgr,
		store:           store,
		tracer:          tracer,
		log:             log,
		approvalTimeout: approvalTimeout,
	}
}

// Register mounts the gateway at POST /mcp.
func (g *Gateway) Register(mux *http.ServeMux, mw Middleware) {
	if mw == nil {
		mw = func(h http.HandlerFunc) http.HandlerFunc { return h }
	}
	mux.HandleFunc("POST /mcp", mw(g.handle))
}

// jsonrpcRequest is the subset of a JSON-RPC message we inspect.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (g *Gateway) handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	// Only tools/call is governed; everything else is forwarded verbatim.
	var req jsonrpcRequest
	if json.Unmarshal(body, &req) != nil || req.Method != "tools/call" {
		g.forward(w, r, body)
		return
	}

	var params toolsCallParams
	_ = json.Unmarshal(req.Params, &params)
	tool := params.Name

	run := g.resolveRun(r)
	stepIdx := run.View().Usage.Loops

	// If the run was created under a policy bundle (policyRef), that bundle's tool
	// allowlist and approval rules govern this call — not just the daemon-global
	// config. Bundle missing/unknown → fall back to the global config.
	bundle := g.runPolicy(r.Context(), run)

	// 1) Allowlist (deterministic). A blocked attempt is recorded too — a refused
	// tool call is part of the audit trail, not a silent drop.
	if !g.allowedFor(tool, bundle) {
		g.log.Warn("mcp tool blocked by allowlist", "tool", tool, "policy", run.PolicyRef)
		g.recordToolCall(r.Context(), start, run.ID, stepIdx, tool, "", params.Arguments, "blocked")
		writeRPCError(w, req.ID, -32001, "tool not allowed by policy: "+tool)
		return
	}

	sideEffect := g.sideEffect(tool)

	// 2) Approval gate. Side-effecting tools gate under the global policy by
	// default; a run's bundle can ADD a requirement (e.g. naming a normally
	// read-only tool), so consult the bundle's rules too.
	needsApproval := sideEffect != ""
	var bundlePol approval.Policy
	if bundle != nil {
		bundlePol = bundlePolicy(bundle)
		if bundlePol.Requires(tool, sideEffect) {
			needsApproval = true
		}
	}
	if needsApproval {
		ctx, cancel := context.WithTimeout(r.Context(), g.approvalTimeout)
		defer cancel()
		areq := approval.Request{
			RunID: run.ID, StepIndex: stepIdx, Tool: tool,
			SideEffect: sideEffect, Arguments: params.Arguments,
		}
		var decision approval.Decision
		var aerr error
		if bundle != nil {
			decision, _, aerr = g.gate.RequestUnder(ctx, areq, bundlePol)
		} else {
			decision, _, aerr = g.gate.Request(ctx, areq) // daemon-global policy
		}
		if aerr != nil {
			g.recordToolCall(r.Context(), start, run.ID, stepIdx, tool, sideEffect, params.Arguments, "timeout")
			writeRPCError(w, req.ID, -32002, "approval timed out or run cancelled for tool: "+tool)
			return
		}
		if !decision.Approved {
			g.recordToolCall(r.Context(), start, run.ID, stepIdx, tool, sideEffect, params.Arguments, "denied")
			writeRPCError(w, req.ID, -32003, "approval denied for tool: "+tool)
			return
		}
	}

	// 3) Forward to the real MCP server and record the (approved) call.
	g.recordToolCall(r.Context(), start, run.ID, stepIdx, tool, sideEffect, params.Arguments, "approved")
	g.forward(w, r, body)
}

// runPolicy returns the policy bundle a run was created under, or nil if the run
// has no policyRef, there's no store, or the bundle is unknown (fall back to the
// daemon-global config).
func (g *Gateway) runPolicy(ctx context.Context, run *runs.Run) *storage.PolicyRecord {
	if run.PolicyRef == "" || g.store == nil {
		return nil
	}
	p, err := g.store.GetPolicy(ctx, run.PolicyRef)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			g.log.Error("mcp policy lookup failed", "run", run.ID, "policy", run.PolicyRef, "err", err)
		}
		return nil
	}
	return &p
}

// allowedFor reports whether the tool passes the effective allowlist: the run's
// bundle allowlist when it has one, else the daemon-global allowlist (empty = all).
func (g *Gateway) allowedFor(tool string, bundle *storage.PolicyRecord) bool {
	allow := g.allow
	if bundle != nil && len(bundle.ToolAllowlist) > 0 {
		allow = bundle.ToolAllowlist
	}
	return matchAllow(allow, tool)
}

func matchAllow(allow []string, tool string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, pat := range allow {
		if pat == tool {
			return true
		}
		if ok, err := path.Match(pat, tool); err == nil && ok {
			return true
		}
	}
	return false
}

// bundlePolicy is the run bundle's approval policy. DefaultSafe stays on: a run's
// bundle can ADD approval requirements but never silently drop the fail-closed
// gating of side-effecting tools.
func bundlePolicy(bundle *storage.PolicyRecord) approval.Policy {
	rules := make([]approval.Rule, 0, len(bundle.ApprovalRules))
	for _, r := range bundle.ApprovalRules {
		rules = append(rules, approval.Rule{Tool: r.Tool, SideEffect: r.SideEffect})
	}
	return approval.Policy{RequireFor: rules, DefaultSafe: true}
}

// sideEffect returns "" for read-only tools (no approval) and "tool" otherwise,
// so the approval policy (default-safe) decides whether to gate it.
func (g *Gateway) sideEffect(tool string) string {
	if g.readonly[tool] {
		return ""
	}
	return "tool"
}

func (g *Gateway) resolveRun(r *http.Request) *runs.Run {
	if rid := r.Header.Get(HeaderRunID); rid != "" {
		return g.runs.GetOrCreate(rid)
	}
	return g.runs.Create(runs.CreateOptions{Name: "mcp"})
}

func (g *Gateway) recordToolCall(ctx context.Context, start time.Time, runID string, step int32, tool, sideEffect string, args map[string]any, status string) {
	// Emit an OTLP span so the call (and its governance outcome) is visible in the
	// user's observability backend, next to the model-call spans.
	g.tracer.RecordToolCall(ctx, otel.ToolCall{
		RunID: runID, StepIndex: step, Tool: tool, SideEffect: sideEffect,
		Status: status, Start: start, End: time.Now(),
	})
	if g.store == nil {
		return
	}
	err := g.store.AppendToolCall(context.Background(), storage.ToolCallRecord{
		ID: id.NewUUID(), RunID: runID, StepIndex: step, Tool: tool,
		SideEffect: sideEffect, Arguments: args, Status: status, CreatedAt: time.Now(),
	})
	if err != nil {
		g.log.Error("persist tool call failed", "run", runID, "tool", tool, "err", err)
	}
}

// forward proxies the request body to the upstream MCP server and copies the
// response back (JSON or SSE).
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, body []byte) {
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, g.upstream, bytes.NewReader(body))
	if err != nil {
		writeRPCError(w, nil, -32603, "internal error building upstream request")
		return
	}
	// Forward content negotiation + MCP session headers.
	copyHeader(upReq.Header, r.Header, "Content-Type", "Accept", "Mcp-Session-Id", "MCP-Protocol-Version")
	if upReq.Header.Get("Content-Type") == "" {
		upReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.client.Do(upReq)
	if err != nil {
		if errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		writeRPCError(w, nil, -32603, "upstream MCP server unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header, "Content-Type", "Mcp-Session-Id")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyHeader(dst, src http.Header, keys ...string) {
	for _, k := range keys {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

// writeRPCError writes a JSON-RPC 2.0 error response.
func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // JSON-RPC errors ride a 200 envelope
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
			"data":    map[string]any{"source": "riskkernel"},
		},
	})
}
