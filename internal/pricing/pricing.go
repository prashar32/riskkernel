// Package pricing prices token usage deterministically. It is the basis of the
// dollar budget the governor enforces and the cost ledger records. Prices are
// static, defensible list prices (USD per 1M tokens) and are overridable via
// config — RiskKernel never asks an LLM what something costs.
package pricing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Rate is a model's price in USD per 1,000,000 tokens. The JSON tags are the
// pricing-override file format (see LoadOverrides).
type Rate struct {
	InputPerM  float64 `json:"inputPerM"`
	OutputPerM float64 `json:"outputPerM"`
}

// defaultRates holds built-in list prices in USD per 1M tokens, as published on the
// providers' pricing pages. Refreshed 2026-06-19 from:
//
//	Anthropic: https://platform.claude.com/docs/en/about-claude/pricing
//	OpenAI:    https://developers.openai.com/api/docs/pricing
//
// Matching is case-insensitive LONGEST-prefix, so dated snapshots (e.g.
// "claude-opus-4-8-20260101") and minor versions resolve to the right rate, and a
// more specific key wins over a shorter one. Provider prices drift — override or
// extend via config (RISKKERNEL_PRICING_FILE); unknown models price to zero and
// report ok=false so the caller can decide how to treat them.
var defaultRates = map[string]Rate{
	// Anthropic (native). Opus 4.5+ dropped to $5/$25; Opus 4 / 4.1 stay $15/$75,
	// so the newer versions get explicit (longer-prefix) entries.
	"claude-fable-5":   {InputPerM: 10.0, OutputPerM: 50.0},
	"claude-opus-4-8":  {InputPerM: 5.0, OutputPerM: 25.0},
	"claude-opus-4-7":  {InputPerM: 5.0, OutputPerM: 25.0},
	"claude-opus-4-6":  {InputPerM: 5.0, OutputPerM: 25.0},
	"claude-opus-4-5":  {InputPerM: 5.0, OutputPerM: 25.0},
	"claude-opus-4":    {InputPerM: 15.0, OutputPerM: 75.0}, // Opus 4 / 4.1
	"claude-sonnet-4":  {InputPerM: 3.0, OutputPerM: 15.0},  // Sonnet 4 / 4.5 / 4.6
	"claude-haiku-4":   {InputPerM: 1.0, OutputPerM: 5.0},   // Haiku 4.5
	"claude-3-5-haiku": {InputPerM: 0.8, OutputPerM: 4.0},
	// OpenAI (native). The gpt-5.5 / 5.4 family is current; 4o kept as legacy.
	"gpt-5.5":      {InputPerM: 5.0, OutputPerM: 30.0},
	"gpt-5.4-mini": {InputPerM: 0.75, OutputPerM: 4.5},
	"gpt-5.4-nano": {InputPerM: 0.2, OutputPerM: 1.25},
	"gpt-5.4":      {InputPerM: 2.5, OutputPerM: 15.0},
	"gpt-4o-mini":  {InputPerM: 0.15, OutputPerM: 0.6},
	"gpt-4o":       {InputPerM: 2.5, OutputPerM: 10.0},
}

// Table prices models. The zero value is usable (built-in rates only); use
// NewTable to layer config overrides on top.
type Table struct {
	rates map[string]Rate
}

// NewTable returns a Table seeded with the built-in rates, with overrides applied
// on top (override keys are matched by the same prefix rule).
func NewTable(overrides map[string]Rate) *Table {
	m := make(map[string]Rate, len(defaultRates)+len(overrides))
	for k, v := range defaultRates {
		m[strings.ToLower(k)] = v
	}
	for k, v := range overrides {
		m[strings.ToLower(k)] = v
	}
	return &Table{rates: m}
}

// LoadOverrides reads model→Rate overrides from a JSON file — the hook for keeping
// prices current as providers change them, without recompiling. The format is a
// map of model name (or prefix) to its rate in USD per 1M tokens:
//
//	{
//	  "claude-sonnet-4": { "inputPerM": 3.0, "outputPerM": 15.0 },
//	  "my-finetuned-model": { "inputPerM": 0.5, "outputPerM": 1.0 }
//	}
//
// These layer on top of the built-in list prices (pass the result to NewTable).
// A missing, unreadable, malformed, or negative-rate file is an error: pricing is
// the dollar budget's basis, so the daemon refuses to start rather than silently
// misprice.
func LoadOverrides(path string) (map[string]Rate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	overrides := make(map[string]Rate)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&overrides); err != nil {
		return nil, fmt.Errorf("parsing pricing file %s: %w", path, err)
	}
	for model, r := range overrides {
		if r.InputPerM < 0 || r.OutputPerM < 0 {
			return nil, fmt.Errorf("pricing file %s: model %q has a negative rate (%+v)", path, model, r)
		}
	}
	return overrides, nil
}

// Rate returns the rate for a model and whether it was found. Matching is by
// longest case-insensitive prefix so dated snapshots resolve to their family.
func (t *Table) Rate(model string) (Rate, bool) {
	rates := t.rates
	if rates == nil {
		rates = lowerDefaults()
	}
	m := strings.ToLower(model)
	var best string
	for prefix := range rates {
		if strings.HasPrefix(m, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return Rate{}, false
	}
	return rates[best], true
}

// Cost returns the USD cost of the given token counts for a model, and whether
// the model was priced. When ok is false the cost is 0 — the caller decides
// whether to treat an unpriced model as a dollar-budget enforcement gap.
func (t *Table) Cost(model string, promptTokens, completionTokens int64) (usd float64, ok bool) {
	r, found := t.Rate(model)
	if !found {
		return 0, false
	}
	usd = float64(promptTokens)/1e6*r.InputPerM + float64(completionTokens)/1e6*r.OutputPerM
	return usd, true
}

func lowerDefaults() map[string]Rate {
	m := make(map[string]Rate, len(defaultRates))
	for k, v := range defaultRates {
		m[strings.ToLower(k)] = v
	}
	return m
}
