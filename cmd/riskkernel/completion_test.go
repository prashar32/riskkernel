package main

import (
	"strings"
	"testing"
)

// generators maps a shell to its script generator so the table tests can iterate.
var generators = map[string]func() string{
	"bash": bashCompletion,
	"zsh":  zshCompletion,
	"fish": fishCompletion,
}

// keySubcommands must appear in every generated script — these are the surfaces
// users will tab-complete most, so a regression that drops one is a real bug.
var keySubcommands = []string{
	"serve", "runs", "policy", "audit", "approvals", "memory", "doctor",
	"completion",
}

func TestCompletionScriptsContainCommands(t *testing.T) {
	for shell, gen := range generators {
		t.Run(shell, func(t *testing.T) {
			out := gen()
			if strings.TrimSpace(out) == "" {
				t.Fatalf("%s completion script is empty", shell)
			}
			for _, cmd := range keySubcommands {
				if !strings.Contains(out, cmd) {
					t.Errorf("%s completion script missing subcommand %q", shell, cmd)
				}
			}
		})
	}
}

// TestCompletionScriptsContainSubSubcommands checks the second-level commands
// (e.g. `runs list`, `audit export`) are offered too.
func TestCompletionScriptsContainSubSubcommands(t *testing.T) {
	wantPairs := [][2]string{
		{"runs", "resume"},
		{"audit", "compliance"},
		{"policy", "dry-run"},
		{"approvals", "approve"},
		{"memory", "show"},
	}
	for shell, gen := range generators {
		out := gen()
		for _, p := range wantPairs {
			if !strings.Contains(out, p[1]) {
				t.Errorf("%s completion script missing sub-subcommand %q (under %q)", shell, p[1], p[0])
			}
		}
	}
}

func TestRunCompletionValidShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		if err := runCompletion([]string{shell}); err != nil {
			t.Errorf("runCompletion(%q) returned error: %v", shell, err)
		}
	}
}

func TestRunCompletionInvalidShell(t *testing.T) {
	err := runCompletion([]string{"powershell"})
	if err == nil {
		t.Fatal("runCompletion with an unknown shell should return an error")
	}
	if !strings.Contains(err.Error(), "bash|zsh|fish") {
		t.Errorf("error should name the supported shells, got: %v", err)
	}
}

func TestRunCompletionNoArg(t *testing.T) {
	if err := runCompletion(nil); err == nil {
		t.Fatal("runCompletion with no shell argument should return a usage error")
	}
	if err := runCompletion([]string{"bash", "extra"}); err == nil {
		t.Fatal("runCompletion with extra arguments should return a usage error")
	}
}

// TestBashCompletionHeader is a light shape check: the bash script must register
// the completion function against both the `riskkernel` and `rk` commands.
func TestBashCompletionHeader(t *testing.T) {
	out := bashCompletion()
	for _, want := range []string{"complete -F _riskkernel riskkernel", "complete -F _riskkernel rk"} {
		if !strings.Contains(out, want) {
			t.Errorf("bash script missing %q", want)
		}
	}
}

// TestZshCompletionHeader checks the zsh script carries the required #compdef
// directive so zsh autoloads it correctly.
func TestZshCompletionHeader(t *testing.T) {
	out := zshCompletion()
	if !strings.HasPrefix(out, "#compdef riskkernel") {
		t.Errorf("zsh script must start with a #compdef directive, got: %q", firstLine(out))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
