// Package approval implements the human-in-the-loop gate (CLAUDE.md headline
// feature). A side-effecting tool call that policy gates pauses until a human
// approves or denies it. Policy evaluation is deterministic — no LLM decides
// whether something needs approval.
package approval

import "path"

// Rule matches a tool call by exact tool name or by a side-effect glob (e.g.
// "*write*"). A rule with both set matches if EITHER matches.
type Rule struct {
	Tool       string
	SideEffect string
}

func (r Rule) matches(tool, sideEffect string) bool {
	if r.Tool != "" && r.Tool == tool {
		return true
	}
	if r.SideEffect != "" {
		if ok, err := path.Match(r.SideEffect, sideEffect); err == nil && ok {
			return true
		}
	}
	return false
}

// Policy is the deterministic rule set deciding which calls need approval. It
// mirrors the api/v1 ApprovalPolicy schema.
type Policy struct {
	// RequireFor lists match rules; a call needs approval if it matches ANY rule.
	RequireFor []Rule
	// DefaultSafe, when true, requires approval for ANY call with a non-empty side
	// effect even if no rule matched — fail closed on side effects.
	DefaultSafe bool
}

// Requires reports whether a tool call needs human approval.
func (p Policy) Requires(tool, sideEffect string) bool {
	for _, r := range p.RequireFor {
		if r.matches(tool, sideEffect) {
			return true
		}
	}
	return p.DefaultSafe && sideEffect != ""
}
