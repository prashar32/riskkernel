package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/policy"
)

// diagnosis is one check result. Status is "ok", "warn", or "fail".
type diagnosis struct {
	Name   string
	Status string
	Detail string
}

const (
	diagOK   = "ok"
	diagWarn = "warn"
	diagFail = "fail"
)

// runDoctor implements `riskkernel doctor` — diagnose a setup before you rely on
// it. Prints a checklist and exits non-zero if any hard check fails.
func runDoctor(_ []string) error {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("✗ config — could not load configuration:", err)
		return fmt.Errorf("configuration failed to load")
	}

	results := diagnose(cfg)
	results = append(results, checkDaemon(cfg)) // live probe (info/warn only)

	failed := 0
	for _, d := range results {
		fmt.Printf("%s %s%s\n", diagSymbol(d.Status), d.Name, detailSuffix(d.Detail))
		if d.Status == diagFail {
			failed++
		}
	}
	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Println("setup looks good.")
	return nil
}

// diagnose runs the deterministic config + filesystem checks. Kept side-effect
// light (only a data-dir write probe) and dependency-free so it is unit-testable.
func diagnose(cfg *config.Config) []diagnosis {
	return []diagnosis{
		checkDataDir(cfg),
		checkProvider(cfg),
		checkBudget(cfg),
		checkAPIToken(cfg),
		checkPolicyFile(cfg),
	}
}

// checkDataDir verifies the state directory is creatable and writable — the file
// the user owns must be persistable.
func checkDataDir(cfg *config.Config) diagnosis {
	d := diagnosis{Name: "data dir (" + cfg.DataDir + ")"}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return diagnosis{d.Name, diagFail, "not creatable: " + err.Error()}
	}
	probe := filepath.Join(cfg.DataDir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return diagnosis{d.Name, diagFail, "not writable: " + err.Error()}
	}
	_ = os.Remove(probe)
	return diagnosis{d.Name, diagOK, "writable"}
}

// checkProvider verifies the default provider is known and has the credential it
// needs (Ollama is key-free; Bedrock is a stub).
func checkProvider(cfg *config.Config) diagnosis {
	name := "default provider (" + cfg.DefaultProvider + ")"
	switch cfg.DefaultProvider {
	case "anthropic":
		if cfg.AnthropicAPIKey == "" {
			return diagnosis{name, diagWarn, "ANTHROPIC_API_KEY not set — model calls will fail"}
		}
		return diagnosis{name, diagOK, "ANTHROPIC_API_KEY set"}
	case "openai":
		if cfg.OpenAIAPIKey == "" {
			return diagnosis{name, diagWarn, "OPENAI_API_KEY not set — model calls will fail"}
		}
		return diagnosis{name, diagOK, "OPENAI_API_KEY set"}
	case "ollama":
		return diagnosis{name, diagOK, "key-free (local); ensure Ollama is running"}
	case "bedrock":
		return diagnosis{name, diagWarn, "bedrock is a stub — model calls return not-implemented"}
	default:
		return diagnosis{name, diagFail, "unknown provider"}
	}
}

// checkBudget flags a fully-unlimited explicit default budget — a reliability
// runtime should not run unbounded by default.
func checkBudget(cfg *config.Config) diagnosis {
	b := cfg.DefaultBudget
	if b.Defaulted {
		return diagnosis{"default budget", diagOK, "safe defaults applied (no RISKKERNEL_DEFAULT_* set)"}
	}
	if b.Tokens == 0 && b.Dollars == 0 && b.Loops == 0 && b.Seconds == 0 {
		return diagnosis{"default budget", diagWarn, "explicitly unlimited — runs have no default ceiling"}
	}
	return diagnosis{"default budget", diagOK, fmt.Sprintf("$%.2f / %d loops / %d tokens / %ds", b.Dollars, b.Loops, b.Tokens, b.Seconds)}
}

// checkAPIToken warns when the API is unauthenticated.
func checkAPIToken(cfg *config.Config) diagnosis {
	if cfg.APIToken == "" {
		return diagnosis{"api token", diagWarn, "RISKKERNEL_API_TOKEN not set — the API is unauthenticated; don't expose this port"}
	}
	return diagnosis{"api token", diagOK, "set"}
}

// checkPolicyFile validates a configured riskkernel.yaml; a bad one would fail
// daemon startup.
func checkPolicyFile(cfg *config.Config) diagnosis {
	if cfg.PolicyFile == "" {
		return diagnosis{"policy file", diagOK, "none configured (RISKKERNEL_POLICY_FILE unset)"}
	}
	f, err := policy.Load(cfg.PolicyFile)
	if err != nil {
		return diagnosis{"policy file (" + cfg.PolicyFile + ")", diagFail, err.Error()}
	}
	return diagnosis{"policy file (" + cfg.PolicyFile + ")", diagOK, fmt.Sprintf("valid — %d bundle(s)", len(f.Policies))}
}

// checkDaemon probes a running daemon's /healthz. Info/warn only — the doctor is
// useful whether or not the daemon is up.
func checkDaemon(cfg *config.Config) diagnosis {
	name := fmt.Sprintf("daemon (:%d)", cfg.Port)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/healthz", cfg.Port))
	if err != nil {
		return diagnosis{name, diagWarn, "not reachable (start it with `riskkernel serve`)"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return diagnosis{name, diagOK, "responding on /healthz"}
	}
	return diagnosis{name, diagWarn, fmt.Sprintf("unexpected /healthz status %d", resp.StatusCode)}
}

func diagSymbol(status string) string {
	switch status {
	case diagOK:
		return "✓"
	case diagWarn:
		return "⚠"
	default:
		return "✗"
	}
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return "  — " + detail
}
