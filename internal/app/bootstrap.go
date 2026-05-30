// Package app holds bootstrap wiring shared across CLI subcommands — building the
// provider registry, run manager, pricing table, and gateway from config. It
// contains no enforcement logic; that lives in the governor and related internal
// packages.
package app

import (
	"log/slog"
	"os"

	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/gateway"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/pricing"
	"github.com/prashar32/riskkernel/internal/provider"
	"github.com/prashar32/riskkernel/internal/runs"
)

// Deps holds the constructed dependency graph for the daemon and CLI.
type Deps struct {
	Config    *config.Config
	Log       *slog.Logger
	Providers *provider.Registry
	Pricing   *pricing.Table
	Runs      *runs.Manager
	Gateway   *gateway.Gateway
}

// NewLogger returns the daemon's structured logger writing to stderr.
func NewLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Build constructs the full dependency graph from config.
func Build(cfg *config.Config) (*Deps, error) {
	log := NewLogger()

	registry, err := BuildRegistry(cfg)
	if err != nil {
		return nil, err
	}

	prices := pricing.NewTable(nil)
	mgr := runs.NewManager(toGovernorBudget(cfg.DefaultBudget))
	gw := gateway.New(registry, mgr, prices, log)

	return &Deps{
		Config:    cfg,
		Log:       log,
		Providers: registry,
		Pricing:   prices,
		Runs:      mgr,
		Gateway:   gw,
	}, nil
}

// BuildRegistry constructs the provider registry from config. Anthropic is wired
// natively when a key is present; OpenAI/Bedrock/Ollama are registered as stubs
// so config and routing can reference them. The default provider must be usable.
func BuildRegistry(cfg *config.Config) (*provider.Registry, error) {
	// Anthropic is always registered (native v0.1 provider). When the key is
	// absent the daemon still boots — health and routing work — and only an actual
	// Chat call returns a clear "missing API key" error. OpenAI is registered when
	// a key is present; Bedrock/Ollama are stubs config can name before they're
	// built out.
	ps := []provider.Provider{
		provider.NewAnthropic(cfg.AnthropicAPIKey),
		provider.NewBedrock(),
		provider.NewOllama("http://localhost:11434"),
	}
	if cfg.OpenAIAPIKey != "" {
		ps = append(ps, provider.NewOpenAI(cfg.OpenAIAPIKey))
	}

	return provider.NewRegistry(cfg.DefaultProvider, ps...)
}

// toGovernorBudget converts the config's primitive budget into a governor.Budget.
func toGovernorBudget(b config.BudgetConfig) governor.Budget {
	return governor.Budget{
		Tokens:  b.Tokens,
		Dollars: b.Dollars,
		Loops:   b.Loops,
		Seconds: b.Seconds,
	}
}
