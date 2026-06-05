package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInit_ScaffoldsWorkingFiles(t *testing.T) {
	dir := t.TempDir()
	if err := runInit([]string{dir}); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, name := range []string{".env", "quickstart.py"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("expected %s to be created: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	// The .env carries the budget + data-dir config; quickstart wires a governed run.
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	for _, key := range []string{"RISKKERNEL_DEFAULT_DOLLARS", "RISKKERNEL_DATA_DIR", "ANTHROPIC_API_KEY"} {
		if !strings.Contains(string(env), key) {
			t.Fatalf(".env is missing %s:\n%s", key, env)
		}
	}
	if qs, _ := os.ReadFile(filepath.Join(dir, "quickstart.py")); !strings.Contains(string(qs), "governed_run") {
		t.Fatalf("quickstart.py doesn't wire a governed_run:\n%s", qs)
	}
}

func TestRunInit_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	// A pre-existing .env with the user's own content must be preserved.
	envPath := filepath.Join(dir, ".env")
	mine := "ANTHROPIC_API_KEY=sk-mine\n"
	if err := os.WriteFile(envPath, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInit([]string{dir}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if got, _ := os.ReadFile(envPath); string(got) != mine {
		t.Fatalf("init overwrote an existing .env: %q", got)
	}
	// …while the missing quickstart.py is still scaffolded.
	if _, err := os.Stat(filepath.Join(dir, "quickstart.py")); err != nil {
		t.Fatalf("quickstart.py should still be created: %v", err)
	}
}
