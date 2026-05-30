package provider

import "testing"

func TestRegistry(t *testing.T) {
	a := NewAnthropic("k")
	o := NewOpenAI("k")
	r, err := NewRegistry("anthropic", a, o)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Empty name resolves to default.
	got, err := r.Get("")
	if err != nil || got.Name() != "anthropic" {
		t.Fatalf("default lookup = %v, %v", got, err)
	}
	if r.Default().Name() != "anthropic" {
		t.Errorf("Default() = %q", r.Default().Name())
	}

	if got, _ := r.Get("openai"); got != o {
		t.Errorf("openai lookup mismatch")
	}
	if _, err := r.Get("nope"); err == nil {
		t.Errorf("expected error for unknown provider")
	}
	if len(r.Names()) != 2 {
		t.Errorf("Names() = %v", r.Names())
	}
}

func TestRegistry_DefaultMustExist(t *testing.T) {
	if _, err := NewRegistry("openai", NewAnthropic("k")); err == nil {
		t.Fatal("expected error when default provider is not registered")
	}
}

func TestRegistry_NilSkipped(t *testing.T) {
	r, err := NewRegistry("anthropic", NewAnthropic("k"), nil)
	if err != nil {
		t.Fatalf("NewRegistry with nil: %v", err)
	}
	if len(r.Names()) != 1 {
		t.Errorf("nil provider not skipped: %v", r.Names())
	}
}
