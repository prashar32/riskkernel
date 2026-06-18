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

func TestCost_CurrentModels(t *testing.T) {
	tbl := NewTable(nil)
	cases := []struct {
		model   string
		in, out int64
		want    float64
	}{
		// Anthropic (per platform.claude.com pricing, 2026-06-19).
		{"claude-fable-5", 1_000_000, 1_000_000, 60.0},           // 10 + 50
		{"claude-opus-4-8-20260101", 1_000_000, 1_000_000, 30.0}, // 5 + 25 (current Opus)
		{"claude-opus-4-5", 1_000_000, 0, 5.0},                   // 5 (current Opus)
		{"claude-opus-4-1-20250805", 1_000_000, 1_000_000, 90.0}, // 15 + 75 (deprecated 4.1)
		{"claude-opus-4", 1_000_000, 0, 15.0},                    // retired 4.0
		{"claude-haiku-4-5", 1_000_000, 1_000_000, 6.0},          // 1 + 5
		// OpenAI (per developers.openai.com pricing, 2026-06-19).
		{"gpt-5.5", 1_000_000, 1_000_000, 35.0},      // 5 + 30
		{"gpt-5.4", 1_000_000, 1_000_000, 17.5},      // 2.5 + 15
		{"gpt-5.4-mini", 1_000_000, 1_000_000, 5.25}, // 0.75 + 4.5
		{"gpt-5.4-nano", 1_000_000, 0, 0.2},          // 0.2
	}
	for _, c := range cases {
		usd, ok := tbl.Cost(c.model, c.in, c.out)
		if !ok || !approx(usd, c.want) {
			t.Errorf("Cost(%q) = %v ok=%v, want %v", c.model, usd, ok, c.want)
		}
	}
}

func TestCost_OpusVersionPrefixSplit(t *testing.T) {
	// Longest-prefix must price current Opus (4.5+) at $5/$25 while deprecated
	// 4.0/4.1 stay $15/$75 — a regression guard, since the shorter "claude-opus-4"
	// prefix would otherwise misprice the newer (cheaper) models.
	tbl := NewTable(nil)
	if r, _ := tbl.Rate("claude-opus-4-8"); !approx(r.InputPerM, 5.0) || !approx(r.OutputPerM, 25.0) {
		t.Errorf("opus-4-8 rate = %+v, want 5/25", r)
	}
	if r, _ := tbl.Rate("claude-opus-4-1-20250805"); !approx(r.InputPerM, 15.0) || !approx(r.OutputPerM, 75.0) {
		t.Errorf("opus-4-1 rate = %+v, want 15/75", r)
	}
}

func TestCost_SupersededGpt5Unpriced(t *testing.T) {
	// Bare "gpt-5" (5.0) is superseded by 5.4/5.5 and no longer published, so it's
	// unpriced (the user adds an override) rather than carrying a stale rate — while
	// the 5.4/5.5 family that shares the stem is priced.
	tbl := NewTable(nil)
	if _, ok := tbl.Rate("gpt-5"); ok {
		t.Error("bare gpt-5 should be unpriced after the refresh")
	}
	if _, ok := tbl.Rate("gpt-5.5"); !ok {
		t.Error("gpt-5.5 should be priced")
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
