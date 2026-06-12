// Command riskkernel is the RiskKernel daemon and CLI (alias: rk).
//
// Usage:
//
//	riskkernel init [dir]       Scaffold a .env + runnable example to get started.
//	riskkernel serve            Run the governance daemon (default port 7070).
//	riskkernel chat "<prompt>"  One-shot model call — proves the provider path.
//	riskkernel version          Print build identity.
//
// The CLI is the primary human interface. Subcommand dispatch is hand-rolled to
// keep the dependency surface minimal (no cobra in v0.1).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/prashar32/riskkernel/internal/app"
	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/httpapi"
	"github.com/prashar32/riskkernel/internal/provider"
	"github.com/prashar32/riskkernel/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = runInit(args)
	case "serve":
		err = runServe(args)
	case "chat":
		err = runChat(args)
	case "runs":
		err = runRuns(args)
	case "audit":
		err = runAudit(args)
	case "policy":
		err = runPolicy(args)
	case "approvals":
		err = runApprovals(args)
	case "memory":
		err = runMemory(args)
	case "healthcheck":
		err = runHealthcheck(args)
	case "version", "--version", "-v":
		fmt.Println("riskkernel", version.String())
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `riskkernel — the risk engine for your AI agents

Usage:
  riskkernel init [dir]         Scaffold a .env + runnable example to get started
  riskkernel serve              Run the governance daemon (default :7070)
  riskkernel chat "<prompt>"    One-shot model call (proves the provider path)
  riskkernel runs list          List persisted governed runs
  riskkernel runs resume <id>   Show a run's resumable state after a crash
  riskkernel audit export <id>  Export a run's cost ledger as JSON
  riskkernel audit tools <id>   Export a run's governed tool calls as JSON
  riskkernel policy validate <file>          Validate a riskkernel.yaml policy file
  riskkernel policy dry-run <file> <run-id>  Show what a policy would gate/halt on a run
  riskkernel approvals list     List pending human-in-the-loop approvals
  riskkernel approvals approve <id> [--reason ...]  Approve a pending request
  riskkernel approvals deny <id> [--reason ...]     Deny a pending request
  riskkernel memory list [namespace]                List git-native memory entries
  riskkernel memory show <name> [namespace]         Print a memory file
  riskkernel healthcheck        Probe /healthz (used by the Docker HEALTHCHECK)
  riskkernel version            Print build identity
  riskkernel help               Show this help

Configuration comes from the environment and an optional .env file:
  RISKKERNEL_PORT             Daemon port (default 7070)
  RISKKERNEL_DATA_DIR         State directory (default ./data)
  RISKKERNEL_API_TOKEN        Bearer token guarding the API (optional)
  RISKKERNEL_DEFAULT_PROVIDER Default provider (default anthropic)
  ANTHROPIC_API_KEY           Anthropic key (native provider in v0.1)
`)
}

// runServe boots the governance daemon and blocks until interrupted.
func runServe(_ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	deps, err := app.Build(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = deps.Close() }()

	if cfg.APIToken == "" {
		deps.Log.Warn("RISKKERNEL_API_TOKEN is not set — the API is unauthenticated; do not expose this port to an untrusted network")
	}
	if cfg.AnthropicAPIKey == "" {
		deps.Log.Warn("ANTHROPIC_API_KEY is not set — model calls will fail until a key is provided")
	}

	// Crash-resume: reload any runs that were mid-flight before a restart so they
	// keep enforcing against the budget they already spent.
	if n, err := deps.Runs.Reload(context.Background()); err != nil {
		return fmt.Errorf("reloading runs: %w", err)
	} else if n > 0 {
		deps.Log.Info("resumed runs from store", "count", n)
	}

	// SIGINT/SIGTERM cancel the root context, which drains the server cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := httpapi.New(cfg, deps.Gateway, deps.Runs, deps.Approvals, deps.Slack, deps.MCP, deps.Memory, deps.Log)
	addr := fmt.Sprintf(":%d", cfg.Port)
	return srv.Serve(ctx, addr)
}

// runChat performs a single chat completion to prove the end-to-end provider
// path. It is a developer/diagnostic command, not a governed run.
func runChat(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: riskkernel chat \"<prompt>\" [--model <id>] [--provider <name>]")
	}

	model := "claude-sonnet-4-5"
	providerName := ""
	var promptParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 >= len(args) {
				return fmt.Errorf("--model requires a value")
			}
			i++
			model = args[i]
		case "--provider":
			if i+1 >= len(args) {
				return fmt.Errorf("--provider requires a value")
			}
			i++
			providerName = args[i]
		default:
			promptParts = append(promptParts, args[i])
		}
	}
	prompt := strings.TrimSpace(strings.Join(promptParts, " "))
	if prompt == "" {
		return fmt.Errorf("empty prompt")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	registry, err := app.BuildRegistry(cfg)
	if err != nil {
		return err
	}
	p, err := registry.Get(providerName)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	resp, err := p.Chat(ctx, provider.Request{
		Model:     model,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: prompt}},
		MaxTokens: 1024,
	})
	if err != nil {
		return err
	}

	fmt.Println(resp.Content)
	fmt.Fprintf(os.Stderr, "\n[%s/%s] tokens: %d in + %d out = %d  finish: %s\n",
		p.Name(), resp.Model,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.Total(),
		resp.FinishReason)
	return nil
}
