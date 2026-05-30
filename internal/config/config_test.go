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

// --- helpers ---

// withCleanEnv clears the env vars Load reads so tests are hermetic. t.Setenv
// restores originals at test end.
func withCleanEnv(t *testing.T) {
	for _, k := range []string{
		"RISKKERNEL_PORT", "RISKKERNEL_DATA_DIR", "RISKKERNEL_API_TOKEN",
		"RISKKERNEL_DEFAULT_PROVIDER", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
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
