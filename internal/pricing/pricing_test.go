package pricing

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCost_KnownModel(t *testing.T) {
	tbl := NewTable(nil)
	// claude-sonnet-4: $3/M in, $15/M out. 1M in + 1M out = 3 + 15 = 18.
	usd, ok := tbl.Cost("claude-sonnet-4-5", 1_000_000, 1_000_000)
	if !ok {
		t.Fatal("expected sonnet to be priced")
	}
	if !approx(usd, 18.0) {
		t.Fatalf("cost = %v, want 18.0", usd)
	}
}

func TestCost_DatedSnapshotResolvesToFamily(t *testing.T) {
	tbl := NewTable(nil)
	usd, ok := tbl.Cost("claude-sonnet-4-5-20250101", 1_000_000, 0)
	if !ok || !approx(usd, 3.0) {
		t.Fatalf("dated snapshot cost = %v ok=%v, want 3.0", usd, ok)
	}
}

func TestCost_LongestPrefixWins(t *testing.T) {
	tbl := NewTable(nil)
	// "claude-3-5-haiku" ($0.8/M) must win over any shorter overlap.
	r, ok := tbl.Rate("claude-3-5-haiku-20241022")
	if !ok || !approx(r.InputPerM, 0.8) {
		t.Fatalf("rate = %+v ok=%v, want InputPerM 0.8", r, ok)
	}
}

func TestCost_UnknownModel(t *testing.T) {
	tbl := NewTable(nil)
	usd, ok := tbl.Cost("some-unknown-model", 1000, 1000)
	if ok || usd != 0 {
		t.Fatalf("unknown model should be unpriced: usd=%v ok=%v", usd, ok)
	}
}

func TestCost_Override(t *testing.T) {
	tbl := NewTable(map[string]Rate{"claude-sonnet-4": {InputPerM: 1.0, OutputPerM: 2.0}})
	usd, ok := tbl.Cost("claude-sonnet-4-5", 1_000_000, 1_000_000)
	if !ok || !approx(usd, 3.0) {
		t.Fatalf("override cost = %v, want 3.0", usd)
	}
}

func TestCost_ZeroValueTableUsesDefaults(t *testing.T) {
	var tbl Table // zero value
	usd, ok := tbl.Cost("gpt-4o-mini", 1_000_000, 0)
	if !ok || !approx(usd, 0.15) {
		t.Fatalf("zero-value table cost = %v ok=%v, want 0.15", usd, ok)
	}
}
