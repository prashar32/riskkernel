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
	"github.com/prashar32/riskkernel/internal/policy"
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
	Slack     *approval.SlackNotifier // nil when the Slack channel isn't configured
	MCP       *mcp.Gateway            // nil when no upstream is configured
	Memory    *memory.Reader
	Ingress   *otel.Ingress // nil unless the OTLP trace receiver is enabled
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

	// Load any token→$ pricing overrides before acquiring resources, so a bad
	// pricing file fails fast. Pricing is the dollar budget's basis — refuse to
	// start on a malformed file rather than silently misprice.
	var priceOverrides map[string]pricing.Rate
	if cfg.PricingFile != "" {
		priceOverrides, err = pricing.LoadOverrides(cfg.PricingFile)
		if err != nil {
			return nil, fmt.Errorf("loading pricing overrides: %w", err)
		}
		log.Info("pricing overrides loaded", "file", cfg.PricingFile, "models", len(priceOverrides))
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

	prices := pricing.NewTable(priceOverrides)
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

	webhook := approval.NewWebhookNotifier(cfg.Approval.WebhookURL, log)
	slackNotifier := approval.NewSlackNotifier(cfg.Approval.SlackBotToken, cfg.Approval.SlackChannel,
		cfg.Approval.SlackSigningSecret, log)
	if slackNotifier != nil {
		log.Info("slack approval channel enabled", "channel", cfg.Approval.SlackChannel,
			"interactive", cfg.Approval.SlackSigningSecret != "")
	}
	notifier := approval.CombineNotifiers(webhook, slackNotifier)
	gate := approval.NewGate(store, approval.Policy{DefaultSafe: cfg.Approval.DefaultSafe}, notifier, log)

	// Register policy-as-code bundles from riskkernel.yaml (reviewable in PRs) so
	// runs can reference them by name. A bad file fails fast rather than booting
	// with a stale or partial policy.
	if cfg.PolicyFile != "" {
		n, err := registerPolicyFile(context.Background(), store, cfg.PolicyFile)
		if err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("loading policy file %q: %w", cfg.PolicyFile, err)
		}
		log.Info("policy bundles registered", "file", cfg.PolicyFile, "count", n)
	}

	var mcpGW *mcp.Gateway
	if cfg.MCP.Upstream != "" {
		mcpGW = mcp.New(cfg.MCP.Upstream, cfg.MCP.Allowlist, cfg.MCP.ReadOnly, gate, mgr, store, tracer,
			time.Duration(cfg.MCP.ApprovalTimeoutSeconds)*time.Second, log)
		log.Info("mcp gateway enabled", "upstream", cfg.MCP.Upstream,
			"allowlist", len(cfg.MCP.Allowlist), "readonly", len(cfg.MCP.ReadOnly))
	}

	memReader := memory.NewReader(cfg.Memory.Dir)
	log.Info("memory layer ready", "dir", memReader.Root())
	if cfg.Memory.Embeddings {
		log.Warn("RISKKERNEL_MEMORY_EMBEDDINGS is set but embeddings are not implemented in v0.1; using deterministic keyword search")
	}

	// OTLP trace receiver (Surface 3, consume side) — off unless explicitly enabled.
	var ingress *otel.Ingress
	if cfg.OTel.IngressEnabled {
		ingress = otel.NewIngress(mgr, prices, log)
		log.Info("otlp trace ingress enabled", "path", "POST /v1/traces")
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
		Slack:     slackNotifier,
		MCP:       mcpGW,
		Memory:    memReader,
		Ingress:   ingress,
	}, nil
}

// OpenStore opens the durable state backend, running forward migrations on the way
// up. SQLite (the zero-config file the user owns) is the default; setting
// RISKKERNEL_DATABASE_URL selects the opt-in Postgres backend instead, for
// multi-instance / HA deployments.
func OpenStore(cfg *config.Config, log *slog.Logger) (storage.Store, error) {
	if cfg.DatabaseURL != "" {
		store, err := storage.OpenPostgres(cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		log.Info("state store ready", "backend", "postgres")
		return store, nil
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating data dir %q: %w", cfg.DataDir, err)
	}
	dbPath := filepath.Join(cfg.DataDir, "riskkernel.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	log.Info("state store ready", "backend", "sqlite", "path", dbPath)
	return store, nil
}

// BuildRegistry constructs the provider registry from config. Anthropic, OpenAI,
// and Ollama are implemented natively; Bedrock is a stub config can reference.
// The default provider must be usable.
func BuildRegistry(cfg *config.Config) (*provider.Registry, error) {
	// Anthropic is always registered (native provider). When the key is absent the
	// daemon still boots — health and routing work — and only an actual Chat call
	// returns a clear "missing API key" error. Ollama is native and key-free
	// (local models); OpenAI is registered (native) when a key is present; Bedrock
	// is a stub config can name before it's built out.
	ps := []provider.Provider{
		provider.NewAnthropic(cfg.AnthropicAPIKey).WithBaseURL(cfg.AnthropicBaseURL),
		provider.NewBedrock(),
		provider.NewOllama(cfg.OllamaBaseURL), // empty → local default
	}
	if cfg.OpenAIAPIKey != "" {
		ps = append(ps, provider.NewOpenAI(cfg.OpenAIAPIKey).WithBaseURL(cfg.OpenAIBaseURL))
	}

	return provider.NewRegistry(cfg.DefaultProvider, ps...)
}

// registerPolicyFile loads a riskkernel.yaml and upserts each bundle into the
// store, returning how many were registered.
func registerPolicyFile(ctx context.Context, store storage.Store, path string) (int, error) {
	f, err := policy.Load(path)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	for _, b := range f.Policies {
		if err := store.UpsertPolicy(ctx, b.Record(now)); err != nil {
			return 0, err
		}
	}
	return len(f.Policies), nil
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
