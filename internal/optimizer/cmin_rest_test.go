package optimizer

import (
	"math"
	"testing"

	"github.com/computeralex92/optima/internal/settings"
)

// TestCMinRestWithChargeDemand pins the upstream #309 regression: a battery
// short of a full c_min slot fills with an inactive p_demand exactly as it does
// without one.
func TestCMinRestWithChargeDemand(t *testing.T) {
	plain := loadCase(t, "../../test_cases/030-c_min-short-first-slot.json")

	demand := plain
	demand.Batteries[0].PDemand = make([]float64, len(plain.Series.GT))

	got, err := Solve(plain, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	withDemand, err := Solve(demand, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectiveValue == nil || withDemand.ObjectiveValue == nil {
		t.Fatal("missing objective")
	}
	if math.Abs(*withDemand.ObjectiveValue-*got.ObjectiveValue) > 0.01 {
		t.Fatalf("objective with p_demand %v vs plain %v", *withDemand.ObjectiveValue, *got.ObjectiveValue)
	}
}
