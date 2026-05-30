package approval

import "testing"

func TestPolicyRequires(t *testing.T) {
	p := Policy{
		RequireFor: []Rule{
			{Tool: "mcp://shell"},
			{SideEffect: "*write*"},
			{Tool: "mcp://github/create_pull_request"},
		},
	}
	cases := []struct {
		tool, sideEffect string
		want             bool
	}{
		{"mcp://shell", "", true},                 // exact tool
		{"mcp://filesystem", "file_write", true},  // side-effect glob
		{"mcp://filesystem", "overwrite_x", true}, // glob substring
		{"mcp://github/create_pull_request", "", true},
		{"mcp://filesystem", "read", false}, // read-only, no rule
		{"mcp://search", "", false},         // unlisted, no side effect
	}
	for _, c := range cases {
		if got := p.Requires(c.tool, c.sideEffect); got != c.want {
			t.Errorf("Requires(%q,%q) = %v, want %v", c.tool, c.sideEffect, got, c.want)
		}
	}
}

func TestPolicyDefaultSafe(t *testing.T) {
	p := Policy{DefaultSafe: true}
	if !p.Requires("mcp://anything", "write") {
		t.Error("default-safe should require approval for any side effect")
	}
	if p.Requires("mcp://anything", "") {
		t.Error("default-safe should NOT gate read-only (empty side effect) calls")
	}

	open := Policy{DefaultSafe: false}
	if open.Requires("mcp://anything", "write") {
		t.Error("non-default-safe + no rule should not require approval")
	}
}
