// Package config loads RiskKernel's daemon configuration from the environment
// and an optional .env file. Secrets (provider API keys, the API token) come
// only from here — never from the SQLite state, never logged, never committed.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultPort is the daemon's default listen port.
const DefaultPort = 7070

// Config is the resolved daemon configuration. Field documentation notes the
// environment variable each value is read from.
type Config struct {
	// Port is the HTTP listen port. Env: RISKKERNEL_PORT (default 7070).
	Port int
	// DataDir is where the SQLite state file lives. Env: RISKKERNEL_DATA_DIR
	// (default "./data"). The file in here is the one the user owns.
	DataDir string
	// APIToken is the single-tenant bearer token guarding the API. Env:
	// RISKKERNEL_API_TOKEN. Empty means auth is disabled (local-only use).
	APIToken string

	// DefaultProvider selects which provider unspecified requests route to.
	// Env: RISKKERNEL_DEFAULT_PROVIDER (default "anthropic").
	DefaultProvider string

	// Provider API keys. Each is read from its conventional env var so existing
	// setups need no change. Never stored or logged.
	AnthropicAPIKey string // ANTHROPIC_API_KEY
	OpenAIAPIKey    string // OPENAI_API_KEY

	// DefaultBudget is applied to runs created without an explicit budget — e.g.
	// proxy calls that supply only a run-id. Any zero field is unlimited.
	DefaultBudget BudgetConfig

	// OTel configures OpenTelemetry GenAI span export (Surface 3). Disabled unless
	// an endpoint is set — RiskKernel never emits telemetry unless the user points
	// it at their own OTLP backend.
	OTel OTelConfig

	// Approval configures the human-in-the-loop gate.
	Approval ApprovalConfig

	// MCP configures the MCP gateway (tool governance). Disabled unless an upstream
	// MCP server URL is set.
	MCP MCPConfig
}

// MCPConfig configures the MCP gateway: a JSON-RPC reverse proxy in front of an
// upstream MCP server that governs tools/call.
type MCPConfig struct {
	// Upstream is the real MCP server's HTTP endpoint. Empty disables the gateway.
	// Read from RISKKERNEL_MCP_UPSTREAM.
	Upstream string
	// Allowlist limits which tools may be called (exact name or glob). Empty means
	// all tools are allowed. Read from RISKKERNEL_MCP_ALLOWLIST (comma-separated).
	Allowlist []string
	// ReadOnly names tools that are read-only and therefore never require approval.
	// Everything else is treated as side-effecting. Read from
	// RISKKERNEL_MCP_READONLY (comma-separated).
	ReadOnly []string
	// ApprovalTimeoutSeconds bounds how long a gated tools/call waits for a human.
	// Read from RISKKERNEL_MCP_APPROVAL_TIMEOUT (default 110, under the server
	// write timeout).
	ApprovalTimeoutSeconds int
}

// ApprovalConfig configures the human-in-the-loop approval gate.
type ApprovalConfig struct {
	// DefaultSafe requires approval for any side-effecting tool call not otherwise
	// allowed. Read from RISKKERNEL_APPROVAL_DEFAULT_SAFE (default true — fail
	// closed on side effects).
	DefaultSafe bool
	// WebhookURL, if set, receives a JSON POST when an approval becomes pending.
	// Read from RISKKERNEL_APPROVAL_WEBHOOK. User-configured egress only.
	WebhookURL string
}

// OTelConfig configures OTLP trace export, using standard OpenTelemetry env vars
// so existing setups need no new configuration.
type OTelConfig struct {
	// Endpoint is the OTLP endpoint. Empty disables export entirely. Read from
	// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, then OTEL_EXPORTER_OTLP_ENDPOINT.
	Endpoint string
	// Protocol is "grpc" (default) or "http" (a.k.a. "http/protobuf"). Read from
	// OTEL_EXPORTER_OTLP_PROTOCOL.
	Protocol string
	// Insecure disables TLS. Defaults true for http:// endpoints, else read from
	// OTEL_EXPORTER_OTLP_INSECURE.
	Insecure bool
	// ServiceName tags exported spans. Read from OTEL_SERVICE_NAME (default
	// "riskkernel").
	ServiceName string
}

// BudgetConfig holds raw budget values (no governor dependency here so config
// stays a leaf package). Zero in any field means unlimited for that dimension.
type BudgetConfig struct {
	Tokens  int64   // RISKKERNEL_DEFAULT_TOKENS
	Dollars float64 // RISKKERNEL_DEFAULT_DOLLARS
	Loops   int32   // RISKKERNEL_DEFAULT_LOOPS
	Seconds int32   // RISKKERNEL_DEFAULT_SECONDS
}

// Load resolves configuration. It first loads KEY=VALUE pairs from .env (if
// present) into the process environment without overriding values already set,
// then reads the resolved environment. A missing .env is not an error.
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, fmt.Errorf("loading .env: %w", err)
	}

	port := DefaultPort
	if v := os.Getenv("RISKKERNEL_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("RISKKERNEL_PORT must be a valid port 1-65535, got %q", v)
		}
		port = p
	}

	budget, err := loadBudget()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Port:            port,
		DataDir:         getenvDefault("RISKKERNEL_DATA_DIR", "./data"),
		APIToken:        os.Getenv("RISKKERNEL_API_TOKEN"),
		DefaultProvider: getenvDefault("RISKKERNEL_DEFAULT_PROVIDER", "anthropic"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		DefaultBudget:   budget,
		OTel:            loadOTel(),
		Approval: ApprovalConfig{
			DefaultSafe: envBoolDefault("RISKKERNEL_APPROVAL_DEFAULT_SAFE", true),
			WebhookURL:  os.Getenv("RISKKERNEL_APPROVAL_WEBHOOK"),
		},
		MCP: MCPConfig{
			Upstream:               os.Getenv("RISKKERNEL_MCP_UPSTREAM"),
			Allowlist:              splitList(os.Getenv("RISKKERNEL_MCP_ALLOWLIST")),
			ReadOnly:               splitList(os.Getenv("RISKKERNEL_MCP_READONLY")),
			ApprovalTimeoutSeconds: envIntDefault("RISKKERNEL_MCP_APPROVAL_TIMEOUT", 110),
		},
	}
	return cfg, nil
}

// splitList parses a comma-separated env value into a trimmed, non-empty slice.
func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envIntDefault parses an int env var, returning def when unset or invalid.
func envIntDefault(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// envBoolDefault parses a boolean env var, returning def when unset. Accepts
// "true"/"false" (case-insensitive) and "1"/"0".
func envBoolDefault(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	switch strings.ToLower(v) {
	case "":
		return def
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

// loadOTel resolves OpenTelemetry export config from standard OTEL_* env vars.
func loadOTel() OTelConfig {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	insecure := strings.EqualFold(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"), "true") ||
		strings.HasPrefix(endpoint, "http://")
	return OTelConfig{
		Endpoint:    endpoint,
		Protocol:    getenvDefault("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"),
		Insecure:    insecure,
		ServiceName: getenvDefault("OTEL_SERVICE_NAME", "riskkernel"),
	}
}

// loadBudget reads the optional default-budget env vars. All are optional; an
// unset or zero value means unlimited for that dimension.
func loadBudget() (BudgetConfig, error) {
	var b BudgetConfig
	var err error
	if b.Tokens, err = envInt64("RISKKERNEL_DEFAULT_TOKENS"); err != nil {
		return b, err
	}
	if b.Dollars, err = envFloat("RISKKERNEL_DEFAULT_DOLLARS"); err != nil {
		return b, err
	}
	var loops int64
	if loops, err = envInt64("RISKKERNEL_DEFAULT_LOOPS"); err != nil {
		return b, err
	}
	b.Loops = int32(loops)
	var secs int64
	if secs, err = envInt64("RISKKERNEL_DEFAULT_SECONDS"); err != nil {
		return b, err
	}
	b.Seconds = int32(secs)
	return b, nil
}

func envInt64(key string) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", key, v)
	}
	return n, nil
}

func envFloat(key string) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%s must be a non-negative number, got %q", key, v)
	}
	return f, nil
}

// loadDotEnv parses a simple .env file (KEY=VALUE per line, # comments, optional
// surrounding quotes) and sets any key not already present in the environment.
// Existing environment values always win.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = unquote(val)
		if key == "" {
			return fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		if _, present := os.LookupEnv(key); !present {
			if err := os.Setenv(key, val); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// unquote strips one layer of matching single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
