package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/memory"
)

// runMemory implements `riskkernel memory <list|show>` — read-only inspection of
// the git-native memory directory (the files you own on disk).
func runMemory(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: riskkernel memory <list [namespace] | show <name> [namespace]>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	reader := memory.NewReader(cfg.Memory.Dir)

	switch args[0] {
	case "list":
		ns := ""
		if len(args) > 1 {
			ns = args[1]
		}
		entries, err := reader.List(ns)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Printf("no memory entries under %s\n", reader.Root())
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tFORMAT\tTITLE")
		for _, e := range entries {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, e.Format, e.Title)
		}
		return tw.Flush()

	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: riskkernel memory show <name> [namespace]")
		}
		name := args[1]
		ns := ""
		if len(args) > 2 {
			ns = args[2]
		}
		content, _, err := reader.Read(ns, name)
		if err != nil {
			return err
		}
		fmt.Print(content)
		if len(content) > 0 && content[len(content)-1] != '\n' {
			fmt.Println()
		}
		return nil

	default:
		return fmt.Errorf("unknown memory subcommand %q (want list|show)", args[0])
	}
}
