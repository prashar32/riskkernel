package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPolicyRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	p := PolicyRecord{
		Name:          "developer",
		BudgetTokens:  200000,
		BudgetDollars: 5,
		BudgetLoops:   50,
		BudgetSeconds: 1800,
		ToolAllowlist: []string{"mcp://github", "mcp://filesystem"},
		ApprovalRules: []ApprovalRule{{Tool: "mcp://shell"}, {SideEffect: "*write*"}},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.UpsertPolicy(ctx, p); err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}

	got, err := s.GetPolicy(ctx, "developer")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.BudgetTokens != 200000 || got.BudgetDollars != 5 || got.BudgetLoops != 50 || got.BudgetSeconds != 1800 {
		t.Fatalf("budget mismatch: %+v", got)
	}
	if len(got.ToolAllowlist) != 2 || got.ToolAllowlist[0] != "mcp://github" {
		t.Fatalf("allowlist mismatch: %v", got.ToolAllowlist)
	}
	if len(got.ApprovalRules) != 2 || got.ApprovalRules[0].Tool != "mcp://shell" || got.ApprovalRules[1].SideEffect != "*write*" {
		t.Fatalf("rules mismatch: %v", got.ApprovalRules)
	}
}

func TestPolicyUpsert_PreservesCreatedAt(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	if err := s.UpsertPolicy(ctx, PolicyRecord{Name: "p", BudgetLoops: 10, CreatedAt: created, UpdatedAt: created}); err != nil {
		t.Fatal(err)
	}
	// Re-register with a different budget and a later timestamp.
	updated := created.Add(time.Hour)
	if err := s.UpsertPolicy(ctx, PolicyRecord{Name: "p", BudgetLoops: 99, CreatedAt: updated, UpdatedAt: updated}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPolicy(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if got.BudgetLoops != 99 {
		t.Fatalf("update did not apply: loops = %d", got.BudgetLoops)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at not preserved on update: got %v, want %v", got.CreatedAt, created)
	}
}

func TestGetPolicy_NotFound(t *testing.T) {
	s := openTemp(t)
	if _, err := s.GetPolicy(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPolicy(nope) err = %v, want ErrNotFound", err)
	}
}

func TestListPolicies(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, n := range []string{"a", "b"} {
		if err := s.UpsertPolicy(ctx, PolicyRecord{Name: n, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListPolicies len = %d, want 2", len(list))
	}
}
