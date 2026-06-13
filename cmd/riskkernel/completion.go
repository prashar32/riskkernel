package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// topLevelCommands are the riskkernel subcommands offered as the first
// completion. Kept in sync with the dispatch switch in main.go.
var topLevelCommands = []string{
	"init", "serve", "chat", "runs", "audit", "policy",
	"approvals", "memory", "doctor", "healthcheck", "version", "help",
}

// subCommands maps a top-level command to its sub-subcommands, so completion can
// offer e.g. `runs list|resume`. Commands without sub-subcommands are absent.
// Kept in sync with the per-command dispatch switches (admin.go, policy.go,
// approvals.go, memory.go).
var subCommands = map[string][]string{
	"runs":      {"list", "resume"},
	"audit":     {"export", "tools", "compliance"},
	"policy":    {"validate", "dry-run"},
	"approvals": {"list", "approve", "deny"},
	"memory":    {"list", "show"},
}

// runCompletion implements `riskkernel completion <bash|zsh|fish>`: it prints a
// shell completion script to stdout. The scripts are hand-written static strings
// (no cobra) that complete the top-level subcommands and their sub-subcommands.
func runCompletion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: riskkernel completion <bash|zsh|fish>")
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletion())
	case "zsh":
		fmt.Print(zshCompletion())
	case "fish":
		fmt.Print(fishCompletion())
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, completionHelp)
		return nil
	default:
		return fmt.Errorf("unknown shell %q (want bash|zsh|fish)", args[0])
	}
	return nil
}

const completionHelp = `Generate a shell completion script for riskkernel.

Usage:
  riskkernel completion <bash|zsh|fish>

Install (bash):
  riskkernel completion bash > /etc/bash_completion.d/riskkernel
  # or, per-user, source it from your ~/.bashrc:
  riskkernel completion bash > ~/.riskkernel-completion.bash
  echo 'source ~/.riskkernel-completion.bash' >> ~/.bashrc

Install (zsh):
  riskkernel completion zsh > "${fpath[1]}/_riskkernel"
  # ensure compinit runs in your ~/.zshrc:
  #   autoload -U compinit && compinit

Install (fish):
  riskkernel completion fish > ~/.config/fish/completions/riskkernel.fish
`

// sortedTopLevel returns the top-level commands in a deterministic order so the
// generated script is stable across builds.
func sortedTopLevel() []string {
	out := append([]string(nil), topLevelCommands...)
	sort.Strings(out)
	return out
}

// sortedSubKeys returns the commands that have sub-subcommands, sorted.
func sortedSubKeys() []string {
	keys := make([]string, 0, len(subCommands))
	for k := range subCommands {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bashCompletion returns a bash completion script. It completes the first word
// against the top-level commands and the second word against a command's
// sub-subcommands.
func bashCompletion() string {
	var b strings.Builder
	b.WriteString("# bash completion for riskkernel\n")
	b.WriteString("# install: riskkernel completion bash > /etc/bash_completion.d/riskkernel\n")
	b.WriteString("_riskkernel() {\n")
	b.WriteString("    local cur\n")
	b.WriteString("    COMPREPLY=()\n")
	b.WriteString(`    cur="${COMP_WORDS[COMP_CWORD]}"` + "\n")
	b.WriteString("    local cmds=\"" + strings.Join(sortedTopLevel(), " ") + "\"\n\n")
	b.WriteString("    if [[ ${COMP_CWORD} -eq 1 ]]; then\n")
	b.WriteString(`        COMPREPLY=( $(compgen -W "${cmds}" -- "${cur}") )` + "\n")
	b.WriteString("        return 0\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    if [[ ${COMP_CWORD} -eq 2 ]]; then\n")
	b.WriteString(`        case "${COMP_WORDS[1]}" in` + "\n")
	for _, cmd := range sortedSubKeys() {
		subs := append([]string(nil), subCommands[cmd]...)
		sort.Strings(subs)
		b.WriteString("            " + cmd + ")\n")
		b.WriteString(`                COMPREPLY=( $(compgen -W "` + strings.Join(subs, " ") + `" -- "${cur}") )` + "\n")
		b.WriteString("                return 0\n")
		b.WriteString("                ;;\n")
	}
	b.WriteString("            completion)\n")
	b.WriteString(`                COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )` + "\n")
	b.WriteString("                return 0\n")
	b.WriteString("                ;;\n")
	b.WriteString("        esac\n")
	b.WriteString("    fi\n")
	b.WriteString("    return 0\n")
	b.WriteString("}\n")
	b.WriteString("complete -F _riskkernel riskkernel\n")
	b.WriteString("complete -F _riskkernel rk\n")
	return b.String()
}

// zshCompletion returns a zsh completion script using the standard `#compdef`
// header and `_describe`/`compadd` machinery.
func zshCompletion() string {
	var b strings.Builder
	b.WriteString("#compdef riskkernel rk\n")
	b.WriteString("# zsh completion for riskkernel\n")
	b.WriteString(`# install: riskkernel completion zsh > "${fpath[1]}/_riskkernel"` + "\n")
	b.WriteString("_riskkernel() {\n")
	b.WriteString("    local -a commands\n")
	b.WriteString("    commands=(\n")
	for _, cmd := range sortedTopLevel() {
		b.WriteString("        '" + cmd + ":riskkernel " + cmd + "'\n")
	}
	b.WriteString("    )\n\n")
	b.WriteString("    if (( CURRENT == 2 )); then\n")
	b.WriteString("        _describe 'command' commands\n")
	b.WriteString("        return\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    if (( CURRENT == 3 )); then\n")
	b.WriteString("        case \"${words[2]}\" in\n")
	for _, cmd := range sortedSubKeys() {
		subs := append([]string(nil), subCommands[cmd]...)
		sort.Strings(subs)
		b.WriteString("            " + cmd + ")\n")
		b.WriteString("                compadd " + strings.Join(subs, " ") + "\n")
		b.WriteString("                return\n")
		b.WriteString("                ;;\n")
	}
	b.WriteString("            completion)\n")
	b.WriteString("                compadd bash zsh fish\n")
	b.WriteString("                return\n")
	b.WriteString("                ;;\n")
	b.WriteString("        esac\n")
	b.WriteString("    fi\n")
	b.WriteString("}\n\n")
	b.WriteString("_riskkernel \"$@\"\n")
	return b.String()
}

// fishCompletion returns a fish completion script. fish completions are a list of
// `complete` directives; sub-subcommands are gated on the parent with
// `__fish_seen_subcommand_from`.
func fishCompletion() string {
	var b strings.Builder
	b.WriteString("# fish completion for riskkernel\n")
	b.WriteString("# install: riskkernel completion fish > ~/.config/fish/completions/riskkernel.fish\n\n")

	// Top-level commands: only when no subcommand has been typed yet.
	b.WriteString("function __riskkernel_no_subcommand\n")
	b.WriteString("    set -l cmd (commandline -opc)\n")
	b.WriteString("    test (count $cmd) -eq 1\n")
	b.WriteString("end\n\n")

	for _, cmd := range sortedTopLevel() {
		b.WriteString("complete -c riskkernel -f -n __riskkernel_no_subcommand -a " + cmd +
			" -d 'riskkernel " + cmd + "'\n")
	}
	b.WriteString("\n")

	for _, cmd := range sortedSubKeys() {
		subs := append([]string(nil), subCommands[cmd]...)
		sort.Strings(subs)
		for _, s := range subs {
			b.WriteString("complete -c riskkernel -f -n '__fish_seen_subcommand_from " + cmd +
				"' -a " + s + " -d '" + cmd + " " + s + "'\n")
		}
	}
	b.WriteString("complete -c riskkernel -f -n '__fish_seen_subcommand_from completion'" +
		" -a 'bash zsh fish' -d 'shell'\n")
	return b.String()
}
