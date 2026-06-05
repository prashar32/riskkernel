package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed templates/env.tmpl
var envTemplate string

//go:embed templates/quickstart.py
var quickstartTemplate string

// runInit scaffolds a working starting point — a .env config and a runnable
// governed-loop example — into the target directory (default "."). It never
// overwrites an existing file, so it's safe to re-run.
func runInit(args []string) error {
	dir := "."
	if len(args) > 0 && args[0] != "" {
		dir = args[0]
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	files := []struct{ name, content string }{
		{".env", envTemplate},
		{"quickstart.py", quickstartTemplate},
	}
	var created, skipped []string
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if _, err := os.Stat(path); err == nil {
			skipped = append(skipped, f.name)
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(f.content), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		created = append(created, f.name)
	}

	for _, name := range created {
		fmt.Printf("  created  %s\n", filepath.Join(dir, name))
	}
	for _, name := range skipped {
		fmt.Printf("  kept     %s (already present)\n", filepath.Join(dir, name))
	}

	fmt.Print(`
Next:
  1. (optional) put your ANTHROPIC_API_KEY in .env — model calls need it; the loop demo doesn't
  2. riskkernel serve                 # start the governance daemon (reads .env)
  3. pip install "git+https://github.com/prashar32/riskkernel.git#subdirectory=sdks/python"
  4. python quickstart.py             # watch the governor stop a runaway loop
`)
	return nil
}
