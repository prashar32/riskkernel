package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/prashar32/riskkernel/internal/storage"
)

// Prometheus text exposition format, version 0.0.4. We hand-roll it rather than
// pull in prometheus/client_golang: the surface is tiny (a handful of run/spend
// gauges scraped on demand), and a minimal dependency graph is a project rule.
//
// This is local metrics the user scrapes — it honors the no-telemetry posture:
// nothing is emitted anywhere, the numbers are derived on the fly from the
// SQLite state the user already owns, and no prompt content or PII is exposed.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// handleMetrics implements GET /metrics — a Prometheus scrape of the daemon's
// governed-run state, sourced from the durable Store. Registered only when a
// store is available (see Handler).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	store := s.runs.Store()
	if store == nil {
		http.Error(w, "no durable store configured", http.StatusServiceUnavailable)
		return
	}

	var mw metricsWriter
	if err := s.collectMetrics(r.Context(), store, &mw); err != nil {
		http.Error(w, "collect metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", metricsContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mw.bytes())
}

// collectMetrics derives the exposition body from the store. Kept separate from
// the HTTP plumbing so it's directly unit-testable.
func (s *Server) collectMetrics(ctx context.Context, store storage.Store, mw *metricsWriter) error {
	runRecs, err := store.ListRuns(ctx)
	if err != nil {
		return err
	}

	// riskkernel_runs_total{status="..."} — the live count of runs by lifecycle
	// status (running / halted / cancelled). A gauge, not a counter: runs change
	// status over their life, so the value can go down as well as up.
	byStatus := map[string]int{}
	for _, run := range runRecs {
		byStatus[run.Status]++
	}
	mw.help("riskkernel_runs_total", "Number of governed runs by lifecycle status.")
	mw.typ("riskkernel_runs_total", "gauge")
	if len(byStatus) == 0 {
		// Emit a zero so the series exists on a fresh daemon (a scrape never sees
		// an empty metric and reads it as "scrape failed").
		mw.sample("riskkernel_runs_total", map[string]string{"status": "running"}, 0)
	}
	for _, status := range sortedKeys(byStatus) {
		mw.sample("riskkernel_runs_total", map[string]string{"status": status}, float64(byStatus[status]))
	}

	// riskkernel_runs_halted_total{reason="..."} — halted runs broken out by halt
	// reason (token/dollar/loop/time budget, cancelled). The "why did it stop"
	// view platform teams want next to the status counts.
	byReason := map[string]int{}
	for _, run := range runRecs {
		if run.HaltReason != "" {
			byReason[run.HaltReason]++
		}
	}
	mw.help("riskkernel_runs_halted_total", "Number of halted runs by halt reason.")
	mw.typ("riskkernel_runs_halted_total", "gauge")
	for _, reason := range sortedKeys(byReason) {
		mw.sample("riskkernel_runs_halted_total", map[string]string{"reason": reason}, float64(byReason[reason]))
	}

	// Aggregate spend across all runs, summed from the per-run ledger totals (the
	// auditable source of truth for cost). riskkernel_spend_dollars_total and
	// riskkernel_tokens_total are monotonic over a run's life, so counters.
	var totalDollars float64
	var totalTokens, totalCalls int64
	for _, run := range runRecs {
		t, err := store.Totals(ctx, run.ID)
		if err != nil {
			return err
		}
		totalDollars += t.Dollars
		totalTokens += t.PromptTokens + t.CompletionTokens
		totalCalls += t.Calls
	}
	mw.help("riskkernel_spend_dollars_total", "Total spend in dollars across all runs, summed from the cost ledger.")
	mw.typ("riskkernel_spend_dollars_total", "counter")
	mw.sample("riskkernel_spend_dollars_total", nil, totalDollars)

	mw.help("riskkernel_tokens_total", "Total tokens (prompt + completion) across all runs.")
	mw.typ("riskkernel_tokens_total", "counter")
	mw.sample("riskkernel_tokens_total", nil, float64(totalTokens))

	mw.help("riskkernel_model_calls_total", "Total priced model calls recorded in the cost ledger.")
	mw.typ("riskkernel_model_calls_total", "counter")
	mw.sample("riskkernel_model_calls_total", nil, float64(totalCalls))

	// riskkernel_approvals_pending — the human-in-the-loop queue depth. A gauge:
	// it rises and falls as approvals are requested and resolved.
	pending, err := store.ListApprovals(ctx, storage.ApprovalPending)
	if err != nil {
		return err
	}
	mw.help("riskkernel_approvals_pending", "Number of pending human-in-the-loop approvals.")
	mw.typ("riskkernel_approvals_pending", "gauge")
	mw.sample("riskkernel_approvals_pending", nil, float64(len(pending)))

	return nil
}

// sortedKeys returns a map's keys sorted, for deterministic exposition output
// (stable scrapes and stable tests).
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// metricsWriter builds a Prometheus exposition body. Callers write a metric's
// HELP/TYPE lines once, then its samples.
type metricsWriter struct {
	b strings.Builder
}

func (mw *metricsWriter) help(name, text string) {
	mw.b.WriteString("# HELP ")
	mw.b.WriteString(name)
	mw.b.WriteByte(' ')
	mw.b.WriteString(escapeHelp(text))
	mw.b.WriteByte('\n')
}

func (mw *metricsWriter) typ(name, t string) {
	mw.b.WriteString("# TYPE ")
	mw.b.WriteString(name)
	mw.b.WriteByte(' ')
	mw.b.WriteString(t)
	mw.b.WriteByte('\n')
}

// sample writes one `name{labels} value` line. Labels are sorted by key so the
// output is deterministic regardless of map iteration order.
func (mw *metricsWriter) sample(name string, labels map[string]string, value float64) {
	mw.b.WriteString(name)
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		mw.b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				mw.b.WriteByte(',')
			}
			mw.b.WriteString(k)
			mw.b.WriteString(`="`)
			mw.b.WriteString(escapeLabelValue(labels[k]))
			mw.b.WriteByte('"')
		}
		mw.b.WriteByte('}')
	}
	mw.b.WriteByte(' ')
	mw.b.WriteString(formatValue(value))
	mw.b.WriteByte('\n')
}

func (mw *metricsWriter) bytes() []byte { return []byte(mw.b.String()) }

// formatValue renders a float in the Go default ('g') format; whole numbers come
// out without a trailing ".0", which is valid Prometheus and matches what
// counters of whole units (runs, tokens, calls) should look like.
func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// escapeHelp escapes a HELP string per the exposition format: backslash and
// newline only.
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// escapeLabelValue escapes a label value per the exposition format: backslash,
// double-quote, and newline.
func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}
