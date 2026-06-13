// Package policy loads declarative RiskKernel policy bundles from a riskkernel.yaml
// file — the "policy-as-code" form of the POST /v1/policies bundles, reviewable in
// PRs and applied on startup — and dry-runs a bundle against a recorded run so you
// can see what it WOULD have gated or halted before adopting it. All evaluation is
// deterministic; no LLM is consulted.
package policy

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/prashar32/riskkernel/internal/storage"
)

// SchemaVersion is the only riskkernel.yaml schema version understood today.
const SchemaVersion = 1

// File is a parsed riskkernel.yaml.
type File struct {
	SchemaVersion int      `yaml:"schemaVersion"`
	Policies      []Bundle `yaml:"policies"`
}

// Bundle mirrors the api/v1 Policy schema in YAML form.
type Bundle struct {
	Name           string         `yaml:"name"`
	Budget         Budget         `yaml:"budget"`
	ToolAllowlist  []string       `yaml:"toolAllowlist"`
	ApprovalPolicy ApprovalPolicy `yaml:"approvalPolicy"`
}

type Budget struct {
	Tokens  int64   `yaml:"tokens"`
	Dollars float64 `yaml:"dollars"`
	Loops   int32   `yaml:"loops"`
	Seconds int32   `yaml:"seconds"`
}

type ApprovalPolicy struct {
	RequireFor []Rule `yaml:"requireFor"`
}

type Rule struct {
	Tool       string `yaml:"tool"`
	SideEffect string `yaml:"sideEffect"`
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

// requiresApproval reports whether the bundle's approval policy gates a call.
func (b Bundle) requiresApproval(tool, sideEffect string) bool {
	for _, r := range b.ApprovalPolicy.RequireFor {
		if r.matches(tool, sideEffect) {
			return true
		}
	}
	return false
}

// allows reports whether the bundle's tool allowlist permits a tool. An empty
// allowlist allows everything.
func (b Bundle) allows(tool string) bool {
	if len(b.ToolAllowlist) == 0 {
		return true
	}
	for _, t := range b.ToolAllowlist {
		if t == tool {
			return true
		}
	}
	return false
}

// Load reads and validates a riskkernel.yaml policy file.
func Load(filePath string) (*File, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", filePath, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // reject unknown fields — a typo fails loudly, not silently
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("policy: parse %s: %w", filePath, err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Validate checks schema version, names, and budgets.
func (f *File) Validate() error {
	if f.SchemaVersion != SchemaVersion {
		return fmt.Errorf("policy: unsupported schemaVersion %d (want %d)", f.SchemaVersion, SchemaVersion)
	}
	if len(f.Policies) == 0 {
		return fmt.Errorf("policy: no policies defined")
	}
	seen := make(map[string]bool, len(f.Policies))
	for i, b := range f.Policies {
		if strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("policy: policies[%d] has no name", i)
		}
		if seen[b.Name] {
			return fmt.Errorf("policy: duplicate policy name %q", b.Name)
		}
		seen[b.Name] = true
		if b.Budget.Tokens < 0 || b.Budget.Dollars < 0 || b.Budget.Loops < 0 || b.Budget.Seconds < 0 {
			return fmt.Errorf("policy: %q has a negative budget value", b.Name)
		}
	}
	return nil
}

// Bundle returns the named bundle, or (_, false).
func (f *File) Bundle(name string) (Bundle, bool) {
	for _, b := range f.Policies {
		if b.Name == name {
			return b, true
		}
	}
	return Bundle{}, false
}

// Record converts a bundle into the storage form for registration.
func (b Bundle) Record(now time.Time) storage.PolicyRecord {
	rules := make([]storage.ApprovalRule, 0, len(b.ApprovalPolicy.RequireFor))
	for _, r := range b.ApprovalPolicy.RequireFor {
		rules = append(rules, storage.ApprovalRule{Tool: r.Tool, SideEffect: r.SideEffect})
	}
	return storage.PolicyRecord{
		Name:          b.Name,
		BudgetTokens:  b.Budget.Tokens,
		BudgetDollars: b.Budget.Dollars,
		BudgetLoops:   b.Budget.Loops,
		BudgetSeconds: b.Budget.Seconds,
		ToolAllowlist: b.ToolAllowlist,
		ApprovalRules: rules,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
