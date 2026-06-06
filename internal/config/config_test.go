package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_BudgetClampsInt32Overflow(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t)
	// A value beyond int32 must clamp to MaxInt32, not silently overflow/wrap.
	t.Setenv("RISKKERNEL_DEFAULT_LOOPS", "5000000000") // > math.MaxInt32
	t.Setenv("RISKKERNEL_DEFAULT_SECONDS", "5000000000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultBudget.Loops != math.MaxInt32 {
		t.Errorf("Loops = %d, want clamp to %d", cfg.DefaultBudget.Loops, int32(math.MaxInt32))
	}
	if cfg.DefaultBudget.Seconds != math.MaxInt32 {
		t.Errorf("Seconds = %d, want clamp to %d", cfg.DefaultBudget.Seconds, int32(math.MaxInt32))
	}
}

func TestLoad_Defaults(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t) // no .env present

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.DefaultProvider != "anthropic" {
		t.Errorf("DefaultProvider = %q", cfg.DefaultProvider)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	// With no RISKKERNEL_DEFAULT_* set, the safe default budget must apply.
	b := cfg.DefaultBudget
	if !b.Defaulted {
		t.Error("Defaulted = false, want true when no budget env vars are set")
	}
	if b.Dollars != SafeDefaultDollars || b.Loops != SafeDefaultLoops || b.Seconds != SafeDefaultSeconds {
		t.Errorf("safe defaults = $%v/%d loops/%ds, want $%v/%d/%d",
			b.Dollars, b.Loops, b.Seconds, SafeDefaultDollars, SafeDefaultLoops, SafeDefaultSeconds)
	}
	if b.Tokens != 0 {
		t.Errorf("Tokens = %d, want 0 (unlimited — dollars caps spend)", b.Tokens)
	}
}

func TestLoad_ExplicitBudgetDisablesSafeDefaults(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t)
	// Setting any one variable — even to 0 — is explicit control: no safe
	// defaults, unset dimensions are unlimited.
	t.Setenv("RISKKERNEL_DEFAULT_DOLLARS", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := cfg.DefaultBudget
	if b.Defaulted {
		t.Error("Defaulted = true, want false when a budget env var is set")
	}
	if b.Dollars != 0 || b.Loops != 0 || b.Seconds != 0 || b.Tokens != 0 {
		t.Errorf("explicit budget = %+v, want all-zero (unlimited)", b)
	}
}

func TestLoad_PartialExplicitBudget(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t)
	t.Setenv("RISKKERNEL_DEFAULT_DOLLARS", "2.50")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := cfg.DefaultBudget
	if b.Defaulted {
		t.Error("Defaulted = true, want false")
	}
	if b.Dollars != 2.50 {
		t.Errorf("Dollars = %v, want 2.50", b.Dollars)
	}
	if b.Loops != 0 || b.Seconds != 0 || b.Tokens != 0 {
		t.Errorf("unset dimensions should stay unlimited, got %+v", b)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t)
	t.Setenv("RISKKERNEL_PORT", "9999")
	t.Setenv("ANTHROPIC_API_KEY", "from-env")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9999 {
		t.Errorf("Port = %d", cfg.Port)
	}
	if cfg.AnthropicAPIKey != "from-env" {
		t.Errorf("AnthropicAPIKey = %q", cfg.AnthropicAPIKey)
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t)
	t.Setenv("RISKKERNEL_PORT", "not-a-port")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestLoad_DotEnvDoesNotOverrideRealEnv(t *testing.T) {
	withCleanEnv(t)
	dir := chdirTemp(t)
	writeFile(t, filepath.Join(dir, ".env"), "ANTHROPIC_API_KEY=from-dotenv\nOPENAI_API_KEY='quoted-key'\n# comment\nexport RISKKERNEL_DEFAULT_PROVIDER=openai\n")

	// Real env must win over .env.
	t.Setenv("ANTHROPIC_API_KEY", "from-real-env")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AnthropicAPIKey != "from-real-env" {
		t.Errorf("real env should win: got %q", cfg.AnthropicAPIKey)
	}
	if cfg.OpenAIAPIKey != "quoted-key" {
		t.Errorf("quoted .env value = %q", cfg.OpenAIAPIKey)
	}
	if cfg.DefaultProvider != "openai" {
		t.Errorf("export-prefixed .env value = %q", cfg.DefaultProvider)
	}
}

func TestParseOTLPHeaders(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{"", nil},
		{"   ", nil},
		{"noequals", nil},
		{"authorization=Bearer abc", map[string]string{"authorization": "Bearer abc"}},
		{"a=1,b=2", map[string]string{"a": "1", "b": "2"}},
		{"  a = 1 , b = 2 ", map[string]string{"a": "1", "b": "2"}},
		{"x-honeycomb-team=key123", map[string]string{"x-honeycomb-team": "key123"}},
		{"token=a=b=c", map[string]string{"token": "a=b=c"}}, // value may contain '='
		{"=nokey,a=1", map[string]string{"a": "1"}},          // skip empty key
		{"bad,a=1", map[string]string{"a": "1"}},             // skip malformed pair
	}
	for _, c := range cases {
		got := parseOTLPHeaders(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseOTLPHeaders(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("parseOTLPHeaders(%q)[%q] = %q, want %q", c.in, k, got[k], v)
			}
		}
	}
}

func TestLoad_OTLPHeaders(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://api.honeycomb.io")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-honeycomb-team=secret-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OTel.Headers["x-honeycomb-team"] != "secret-key" {
		t.Errorf("headers = %v", cfg.OTel.Headers)
	}

	// The traces-specific var takes precedence over the general one.
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "authorization=Bearer t1")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OTel.Headers["authorization"] != "Bearer t1" {
		t.Errorf("traces headers should win: %v", cfg.OTel.Headers)
	}
	if _, ok := cfg.OTel.Headers["x-honeycomb-team"]; ok {
		t.Errorf("general headers should be replaced, not merged: %v", cfg.OTel.Headers)
	}
}

func TestLoad_ProviderBaseURLs(t *testing.T) {
	withCleanEnv(t)
	chdirTemp(t)
	t.Setenv("RISKKERNEL_ANTHROPIC_BASE_URL", "http://localhost:9001")
	t.Setenv("RISKKERNEL_OPENAI_BASE_URL", "http://localhost:9002")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AnthropicBaseURL != "http://localhost:9001" || cfg.OpenAIBaseURL != "http://localhost:9002" {
		t.Errorf("base URLs = %q / %q", cfg.AnthropicBaseURL, cfg.OpenAIBaseURL)
	}
}

// --- helpers ---

// withCleanEnv clears the env vars Load reads so tests are hermetic. t.Setenv
// restores originals at test end.
func withCleanEnv(t *testing.T) {
	for _, k := range []string{
		"RISKKERNEL_PORT", "RISKKERNEL_DATA_DIR", "RISKKERNEL_API_TOKEN",
		"RISKKERNEL_DEFAULT_PROVIDER", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"RISKKERNEL_DEFAULT_TOKENS", "RISKKERNEL_DEFAULT_DOLLARS",
		"RISKKERNEL_DEFAULT_LOOPS", "RISKKERNEL_DEFAULT_SECONDS",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS",
		"RISKKERNEL_ANTHROPIC_BASE_URL", "RISKKERNEL_OPENAI_BASE_URL",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// chdirTemp switches into a fresh temp dir for the test and returns it.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
