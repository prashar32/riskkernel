package governor

import (
	"context"
	"math"
	"testing"
)

// These benchmarks measure the deterministic enforcement overhead RiskKernel adds
// in the hot path — the governor's guard methods on the allow path, with all four
// budgets active (large enough never to trip). This is the per-decision cost that
// sits in front of every model/tool call, the number platform teams ask about. The
// budgets are set near their type maxima so the comparisons all execute without
// halting within a benchmark's iteration count.

func benchBudget() Budget {
	return Budget{Tokens: math.MaxInt64, Dollars: math.MaxFloat64, Loops: math.MaxInt32, Seconds: math.MaxInt32}
}

func BenchmarkPreStep(b *testing.B) {
	r := New(context.Background(), benchBudget())
	defer r.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.PreStep()
	}
}

func BenchmarkCanProceed(b *testing.B) {
	r := New(context.Background(), benchBudget())
	defer r.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.CanProceed()
	}
}

func BenchmarkRecordUsage(b *testing.B) {
	r := New(context.Background(), benchBudget())
	defer r.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.RecordUsage(10, 5, 0.0001)
	}
}

// BenchmarkGovernedStep is the full per-call enforcement: register the loop
// iteration, check the ceiling before the call, and meter the result after — the
// total deterministic overhead RiskKernel adds around one governed model call.
func BenchmarkGovernedStep(b *testing.B) {
	r := New(context.Background(), benchBudget())
	defer r.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.PreStep()
		_ = r.CanProceed()
		_ = r.RecordUsage(10, 5, 0.0001)
	}
}
