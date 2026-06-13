package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prashar32/riskkernel/internal/config"
)

func TestCheckProvider(t *testing.T) {
	cases := []struct {
		provider, anthropicKey, openaiKey, want string
	}{
		{"anthropic", "k", "", diagOK},
		{"anthropic", "", "", diagWarn},
		{"openai", "", "k", diagOK},
		{"openai", "", "", diagWarn},
		{"ollama", "", "", diagOK},
		{"bedrock", "", "", diagWarn},
		{"martian", "", "", diagFail},
	}
	for _, c := range cases {
		got := checkProvider(&config.Config{
			DefaultProvider: c.provider, AnthropicAPIKey: c.anthropicKey, OpenAIAPIKey: c.openaiKey,
		})
		if got.Status != c.want {
			t.Errorf("provider %q (anthropic=%q openai=%q): status = %q, want %q", c.provider, c.anthropicKey, c.openaiKey, got.Status, c.want)
		}
	}
}

func TestCheckBudget(t *testing.T) {
	if got := checkBudget(&config.Config{DefaultBudget: config.BudgetConfig{Defaulted: true}}); got.Status != diagOK {
		t.Errorf("safe defaults: %q", got.Status)
	}
	if got := checkBudget(&config.Config{DefaultBudget: config.BudgetConfig{}}); got.Status != diagWarn {
		t.Errorf("explicit unlimited should warn: %q", got.Status)
	}
	if got := checkBudget(&config.Config{DefaultBudget: config.BudgetConfig{Dollars: 5}}); got.Status != diagOK {
		t.Errorf("bounded budget: %q", got.Status)
	}
}

func TestCheckAPIToken(t *testing.T) {
	if got := checkAPIToken(&config.Config{}); got.Status != diagWarn {
		t.Errorf("no token should warn: %q", got.Status)
	}
	if got := checkAPIToken(&config.Config{APIToken: "t"}); got.Status != diagOK {
		t.Errorf("token set: %q", got.Status)
	}
}

func TestCheckPolicyFile(t *testing.T) {
	if got := checkPolicyFile(&config.Config{}); got.Status != diagOK {
		t.Errorf("no policy file should be ok: %q", got.Status)
	}

	dir := t.TempDir()
	valid := filepath.Join(dir, "ok.yaml")
	_ = os.WriteFile(valid, []byte("schemaVersion: 1\npolicies:\n  - name: dev\n"), 0o600)
	if got := checkPolicyFile(&config.Config{PolicyFile: valid}); got.Status != diagOK {
		t.Errorf("valid policy file: %q (%s)", got.Status, got.Detail)
	}

	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("schemaVersion: 2\npolicies: []\n"), 0o600)
	if got := checkPolicyFile(&config.Config{PolicyFile: bad}); got.Status != diagFail {
		t.Errorf("invalid policy file should fail: %q", got.Status)
	}
	if got := checkPolicyFile(&config.Config{PolicyFile: filepath.Join(dir, "missing.yaml")}); got.Status != diagFail {
		t.Errorf("missing policy file should fail: %q", got.Status)
	}
}

func TestCheckDataDir(t *testing.T) {
	if got := checkDataDir(&config.Config{DataDir: t.TempDir()}); got.Status != diagOK {
		t.Errorf("writable temp dir: %q (%s)", got.Status, got.Detail)
	}
	// A path whose parent is a file can't be created as a directory.
	f := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if got := checkDataDir(&config.Config{DataDir: filepath.Join(f, "sub")}); got.Status != diagFail {
		t.Errorf("uncreatable data dir should fail: %q", got.Status)
	}
}

func TestDiagnose_CoversChecks(t *testing.T) {
	cfg := &config.Config{
		DataDir: t.TempDir(), DefaultProvider: "ollama",
		DefaultBudget: config.BudgetConfig{Defaulted: true},
	}
	got := diagnose(cfg)
	if len(got) != 5 {
		t.Fatalf("expected 5 diagnoses, got %d", len(got))
	}
	for _, d := range got {
		if d.Status == diagFail {
			t.Errorf("unexpected fail for a healthy config: %s — %s", d.Name, d.Detail)
		}
		if !strings.ContainsAny(d.Status, "okwarnfail") {
			t.Errorf("bad status %q", d.Status)
		}
	}
}
