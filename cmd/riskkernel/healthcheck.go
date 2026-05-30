package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prashar32/riskkernel/internal/config"
)

// runHealthcheck probes the daemon's /healthz endpoint and exits non-zero if it
// is not OK. It backs the Docker HEALTHCHECK — the distroless image has no shell
// or curl, so the binary checks itself.
func runHealthcheck(_ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: unhealthy (status %d)", resp.StatusCode)
	}
	return nil
}
