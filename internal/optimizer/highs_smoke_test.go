// internal/optimizer/highs_smoke_test.go
package optimizer

import (
	"testing"

	"github.com/bartolsthoorn/gohighs/highs"
)

func TestHighsSolvesTrivialMILP(t *testing.T) {
	m := highs.Model{
		Maximize: true,
		ColCosts: []float64{1, 1},
		ColLower: []float64{0, 0},
		ColUpper: []float64{10, 10},
		VarTypes: []highs.VariableType{highs.Integer, highs.Integer},
	}
	m.AddLeRow([]float64{1, 1}, 3)
	sol, err := m.Solve(highs.WithOutput(false))
	if err != nil {
		t.Fatal(err)
	}
	if !sol.IsOptimal() {
		t.Fatalf("status = %s", sol.Status)
	}
	if got := sol.ColValues[0] + sol.ColValues[1]; got != 3 {
		t.Fatalf("x+y = %v, want 3", got)
	}
}
