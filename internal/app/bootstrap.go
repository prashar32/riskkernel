// Package app holds bootstrap wiring shared across CLI subcommands — building the
// provider registry from config, constructing the logger, etc. It contains no
// enforcement logic; that lives in the governor and related internal packages.
package app

import (
	"log/slog"
	"os"

	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/provider"
)

// NewLogger returns the daemon's structured logger writing to stderr.
func NewLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
