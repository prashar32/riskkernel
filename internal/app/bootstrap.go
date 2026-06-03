// Package app holds bootstrap wiring shared across CLI subcommands — building the
// provider registry, run manager, pricing table, and gateway from config. It
// contains no enforcement logic; that lives in the governor and related internal
// packages.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/prashar32/riskkernel/internal/approval"
	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/gateway"
	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/mcp"
	"github.com/prashar32/riskkernel/internal/memory"
	"github.com/prashar32/riskkernel/internal/otel"
	"github.com/prashar32/riskkernel/internal/pricing"
	"github.com/prashar32/riskkernel/internal/provider"
	"github.com/prashar32/riskkernel/internal/runs"
	"github.com/prashar32/riskkernel/internal/storage"
)

// Deps holds the constructed dependency graph for the daemon and CLI.
type Deps struct {
	Config    *config.Config
	Log       *slog.Logger
	Providers *provider.Registry
	Pricing   *pricing.Table
	Runs      *runs.Manager
	Gateway   *gateway.Gateway
	Store     storage.Store
	Tracer    *otel.Tracer
	Approvals *approval.Gate
	MCP       *mcp.Gateway // nil when no upstream is configured
	Memory    *memory.Reader
}

// Close releases dependencies that hold resources (the tracer's buffered spans,
// then the store).
func (d *Deps) Close() error {
	if d.Tracer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Tracer.Shutdown(ctx)
	}
	if d.Store != nil {
		return d.Store.Close()
	}
	return nil
}

// NewLogger returns the daemon's structured logger writing to stderr.
func NewLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Build constructs the full dependency graph from config, opening and migrating
// the durable store.
func Build(cfg *config.Config) (*Deps, error) {
	log := NewLogger()

	registry, err := BuildRegistry(cfg)
	if err != nil {
		return nil, err
	}

	store, err := OpenStore(cfg, log)
	if err != nil {
		return nil, err
	}

	tracer, err := otel.New(context.Background(), cfg.OTel, log)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	prices := pricing.NewTable(nil)
	if cfg.DefaultBudget.Defaulted {
		log.Warn("no default budget configured — applying safe defaults",
			"dollars", cfg.DefaultBudget.Dollars,
			"loops", cfg.DefaultBudget.Loops,
			"seconds", cfg.DefaultBudget.Seconds,
			"hint", "set RISKKERNEL_DEFAULT_DOLLARS / _LOOPS / _SECONDS / _TOKENS to take explicit control (0 = unlimited)")
	} else {
		log.Info("default budget configured",
			"tokens", cfg.DefaultBudget.Tokens,
			"dollars", cfg.DefaultBudget.Dollars,
			"loops", cfg.DefaultBudget.Loops,
			"seconds", cfg.DefaultBudget.Seconds)
	}
	mgr := runs.NewManager(toGovernorBudget(cfg.DefaultBudget)).WithStore(store, log)
	gw := gateway.New(registry, mgr, prices, tracer, log)

	var notifier approval.Notifier
	if wh := approval.NewWebhookNotifier(cfg.Approval.WebhookURL, log); wh != nil {
		notifier = wh
	}
	gate := approval.NewGate(store, approval.Policy{DefaultSafe: cfg.Approval.DefaultSafe}, notifier, log)

	var mcpGW *mcp.Gateway
	if cfg.MCP.Upstream != "" {
		mcpGW = mcp.New(cfg.MCP.Upstream, cfg.MCP.Allowlist, cfg.MCP.ReadOnly, gate, mgr, store,
			time.Duration(cfg.MCP.ApprovalTimeoutSeconds)*time.Second, log)
		log.Info("mcp gateway enabled", "upstream", cfg.MCP.Upstream,
			"allowlist", len(cfg.MCP.Allowlist), "readonly", len(cfg.MCP.ReadOnly))
	}

	memReader := memory.NewReader(cfg.Memory.Dir)
	log.Info("memory layer ready", "dir", memReader.Root())
	if cfg.Memory.Embeddings {
		log.Warn("RISKKERNEL_MEMORY_EMBEDDINGS is set but embeddings are not implemented in v0.1; using deterministic keyword search")
	}

	return &Deps{
		Config:    cfg,
		Log:       log,
		Providers: registry,
		Pricing:   prices,
		Runs:      mgr,
		Gateway:   gw,
		Store:     store,
		Tracer:    tracer,
		Approvals: gate,
		MCP:       mcpGW,
		Memory:    memReader,
	}, nil
}

// OpenStore creates the data directory and opens the SQLite store (the file the
// user owns), running forward migrations on the way up.
func OpenStore(cfg *config.Config, log *slog.Logger) (storage.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating data dir %q: %w", cfg.DataDir, err)
	}
	dbPath := filepath.Join(cfg.DataDir, "riskkernel.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	log.Info("state store ready", "path", dbPath)
	return store, nil
}

// BuildRegistry constructs the provider registry from config. Anthropic and
// OpenAI are implemented natively; Bedrock/Ollama are stubs config can reference.
// The default provider must be usable.
func BuildRegistry(cfg *config.Config) (*provider.Registry, error) {
	// Anthropic is always registered (native provider). When the key is absent the
	// daemon still boots — health and routing work — and only an actual Chat call
	// returns a clear "missing API key" error. OpenAI is registered (native) when a
	// key is present; Bedrock/Ollama are stubs config can name before they're built
	// out.
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
