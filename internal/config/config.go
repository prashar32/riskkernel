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

// DefaultPort is the daemon's default listen port (CLAUDE.md: 7070).
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

	cfg := &Config{
		Port:            port,
		DataDir:         getenvDefault("RISKKERNEL_DATA_DIR", "./data"),
		APIToken:        os.Getenv("RISKKERNEL_API_TOKEN"),
		DefaultProvider: getenvDefault("RISKKERNEL_DEFAULT_PROVIDER", "anthropic"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
	}
	return cfg, nil
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
