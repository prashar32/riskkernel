package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func writeTempPricing(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

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

func TestLoadOverrides_AppliedToCost(t *testing.T) {
	p := writeTempPricing(t, `{"claude-sonnet-4": {"inputPerM": 1.0, "outputPerM": 2.0}}`)
	ov, err := LoadOverrides(p)
	if err != nil {
		t.Fatal(err)
	}
	tbl := NewTable(ov)
	usd, ok := tbl.Cost("claude-sonnet-4-5", 1_000_000, 1_000_000) // 1.0 + 2.0
	if !ok || !approx(usd, 3.0) {
		t.Fatalf("overridden cost = %v ok=%v, want 3.0", usd, ok)
	}
}

func TestLoadOverrides_AddsNewModel(t *testing.T) {
	// A model the built-ins don't know, priced via the override file (prefix match).
	p := writeTempPricing(t, `{"my-finetune": {"inputPerM": 0.5, "outputPerM": 1.0}}`)
	ov, err := LoadOverrides(p)
	if err != nil {
		t.Fatal(err)
	}
	usd, ok := NewTable(ov).Cost("my-finetune-v2", 1_000_000, 0)
	if !ok || !approx(usd, 0.5) {
		t.Fatalf("new-model cost = %v ok=%v, want 0.5", usd, ok)
	}
}

func TestLoadOverrides_Malformed(t *testing.T) {
	if _, err := LoadOverrides(writeTempPricing(t, `{not valid json`)); err == nil {
		t.Fatal("malformed JSON should error")
	}
}

func TestLoadOverrides_UnknownFieldRejected(t *testing.T) {
	// A typo'd rate field must fail loudly — otherwise the model would silently
	// price to $0 and slip past the dollar budget.
	if _, err := LoadOverrides(writeTempPricing(t, `{"m": {"input_per_m": 3.0}}`)); err == nil {
		t.Fatal("unknown rate field should error")
	}
}

func TestLoadOverrides_NegativeRateRejected(t *testing.T) {
	if _, err := LoadOverrides(writeTempPricing(t, `{"m": {"inputPerM": -1.0, "outputPerM": 2.0}}`)); err == nil {
		t.Fatal("negative rate should error")
	}
}

func TestLoadOverrides_MissingFile(t *testing.T) {
	if _, err := LoadOverrides(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing file should error")
	}
}
