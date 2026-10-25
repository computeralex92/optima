package optimizer

import (
	"math"
	"testing"
	"time"

	"github.com/bartolsthoorn/gohighs/highs"
	"github.com/computeralex92/optima/internal/settings"
)

// preferenceBudgetInput is a two-step case whose cost optimum is unique (import
// at the cheap step 0) but whose discharge_before_import preference strictly
// prefers importing at the later, more expensive step 1. With the default budget
// the preference stage cannot move off the cost optimum; a raised budget lets it
// spend currency for the better preference value.
//
// gt = [1000, 1000], p_N = [0.1, 0.3], one battery with 1000 Wh stored and a
// 1000 Wh/step discharge. The only variable is where the 1000 Wh of import lands:
// n0 = 1000 costs -100, n1 = 1000 costs -300.
func preferenceBudgetInput() Input {
	grid := false
	return Input{
		Strategy: Strategy{ChargingStrategy: "none", DischargingStrategy: "discharge_before_import"},
		Batteries: []Battery{{
			ChargeFromGrid: &grid, DischargeToGrid: &grid,
			SCapacity: f64(10000), SMin: f64(0), SMax: f64(10000), SInitial: f64(1000),
			CMin: f64(0), CMax: f64(0), DMax: f64(1000), PA: f64(0),
		}},
		Series: Series{
			DT: []int{3600, 3600},
			GT: []float64{1000, 1000}, FT: []float64{0, 0},
			PN: []float64{0.1, 0.3}, PE: []float64{0, 0},
		},
		EtaC: f64(1), EtaD: f64(1),
	}
}

// TestCostBoundSlack pins the cost-pin slack the preference budget produces.
func TestCostBoundSlack(t *testing.T) {
	if got := costBoundSlack(0); got != pinEps {
		t.Fatalf("costBoundSlack(0) = %v, want pinEps %v", got, pinEps)
	}
	if got := costBoundSlack(pinEps / 2); got != pinEps {
		t.Fatalf("costBoundSlack(pinEps/2) = %v, want pinEps %v", got, pinEps)
	}
	if got := costBoundSlack(250); got != 250 {
		t.Fatalf("costBoundSlack(250) = %v, want 250", got)
	}
}

// TestPreferenceBudgetAllowsCostSpend is the behavioral test for
// OPTIMIZER_PREFERENCE_BUDGET: the discharge_before_import improvement is blocked
// at budget 0 and taken once the budget covers the extra cost, and the resulting
// objective stays within the budget of the no-budget optimum.
func TestPreferenceBudgetAllowsCostSpend(t *testing.T) {
	in := preferenceBudgetInput()

	t.Run("budget 0 stays cost optimal", func(t *testing.T) {
		got, err := Solve(in, settings.Settings{})
		if err != nil {
			t.Fatal(err)
		}
		if got.ObjectiveValue == nil {
			t.Fatal("objective_value is nil")
		}
		if math.Abs(*got.ObjectiveValue-(-100)) > 1e-6 {
			t.Fatalf("objective %v, want -100 (cost optimum)", *got.ObjectiveValue)
		}
		if first := firstAbove(got.GridImport); first != 0 {
			t.Fatalf("first import at step %d, want 0 (cost optimum import at t=0)", first)
		}
	})

	t.Run("budget 250 trades cost for preference", func(t *testing.T) {
		got, err := Solve(in, settings.Settings{PreferenceBudget: 250})
		if err != nil {
			t.Fatal(err)
		}
		if got.ObjectiveValue == nil {
			t.Fatal("objective_value is nil")
		}
		v := *got.ObjectiveValue
		// The preference pushes the import to the expensive step 1 (cost -300).
		if math.Abs(v-(-300)) > 1e-6 {
			t.Fatalf("objective %v, want -300 (preference spends to import late)", v)
		}
		if d := math.Abs(v - (-100)); d > 250+1e-6 {
			t.Fatalf("objective %v is %v from the no-budget optimum, beyond budget 250", v, d)
		}
		if first := firstAbove(got.GridImport); first != 1 {
			t.Fatalf("first import at step %d, want 1 (preference imports late)", first)
		}
	})
}

// TestTimeLimitAllZeroNoIncumbentNotFeasible is the regression for gohighs'
// HasSolution(): it is true on TimeLimit even with no MIP incumbent, so a
// no-incumbent stop could look Feasible. The safety net must downgrade the
// all-zero, model-violating vector to Not Solved.
func TestTimeLimitAllZeroNoIncumbentNotFeasible(t *testing.T) {
	p, _ := twoStepProblem() // net load ft-gt = 1000 at step 0, so zero violates balance
	mo := buildModel(p)

	// A full-length all-zero vector is exactly the no-incumbent shape; the status
	// claims a usable solution the solver never actually found.
	sol := &highs.Solution{
		Status:    highs.ModelStatusTimeLimit,
		ColValues: make([]float64, mo.m.NumVars()),
	}
	if !sol.HasSolution() {
		t.Fatal("precondition: TimeLimit without an incumbent reports HasSolution true")
	}
	if mo.safetyCheck(sol) {
		t.Fatal("safetyCheck accepted an all-zero vector that violates the balance rows")
	}
	if mo.retain(sol) {
		t.Fatal("retain accepted a no-incumbent time-limit vector")
	}
	if got := mo.finalizeStatus(sol, sol.Status).Status; got != "Not Solved" {
		t.Fatalf("no-incumbent time-limit reported %q, want Not Solved", got)
	}
}

// TestTimeLimitRetainsCostStage is the regression for a timeout in a later stage.
// The cost stage has a valid optimal schedule; the peak stage then expires without
// an incumbent. That partial vector must not replace the cost-stage solution, or
// the whole request degrades to Not Solved instead of returning a usable plan.
func TestTimeLimitRetainsCostStage(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a long horizon against a wall clock")
	}

	c := loadGolden(t, "../../test_cases/028-attenuate-grid-peaks-long-horizon.json")
	limit := 10.0
	got, err := Solve(c.Request, settings.Settings{TimeLimit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" && got.Status != "Feasible" {
		t.Fatalf("status = %q, want a usable result", got.Status)
	}
	if got.ObjectiveValue == nil {
		t.Fatal("objective_value is nil, want the cost-stage optimum")
	}
	if want := 6.037596089425657; math.Abs(*got.ObjectiveValue-want) > 1e-3 {
		t.Fatalf("objective %v, want the cost-stage optimum %v", *got.ObjectiveValue, want)
	}
}

// TestBatteryWithoutPowerIsSolved is upstream's zero-power battery regression:
// evcc sends c_max and d_max 0 when a battery's power limits read 0. The charge
// discharge gate then appears in no row, but the battery must still solve and
// keep its charge.
func TestBatteryWithoutPowerIsSolved(t *testing.T) {
	in := Input{
		Batteries: []Battery{
			{SMin: f64(0), SMax: f64(10000), SInitial: f64(5000),
				CMin: f64(0), CMax: f64(0), DMax: f64(0), PA: f64(0.0001)},
			{SMin: f64(0), SMax: f64(50000), SInitial: f64(0),
				CMin: f64(0), CMax: f64(11000), DMax: f64(0), PA: f64(0.0003)},
		},
		Series: Series{
			DT: []int{3600, 3600}, GT: []float64{1000, 1000}, FT: []float64{0, 2000},
			PN: []float64{0.0003, 0.0003}, PE: []float64{0.0001, 0.0001},
		},
	}
	got, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" {
		t.Fatalf("status = %q, want Optimal", got.Status)
	}
	for i, want := range []float64{5000, 5000} {
		if math.Abs(got.Batteries[0].StateOfCharge[i]-want) > 1e-6 {
			t.Fatalf("battery 0 SOC[%d] = %v, want %v", i, got.Batteries[0].StateOfCharge[i], want)
		}
	}
}

// demandRateInput models the demand-rate path: a 5000 Wh load the 1000 W import
// cap cannot cover, with prc_p_exc_imp set so the exceedance is charged per W.
// Import splits into n = 1000 (the cap) and excImp = 4000 (beyond it), and the
// demand-rate peak pMaxImpExc = 4000.
func demandRateInput() Input {
	pm := 1000.0
	prc := 1.0
	return Input{
		Batteries: []Battery{{
			SCapacity: f64(5000), SMin: f64(0), SMax: f64(5000), SInitial: f64(0),
			CMin: f64(0), CMax: f64(0), DMax: f64(0), PA: f64(0),
		}},
		Grid: Grid{PmaxImp: &pm, PrcPExcImp: &prc},
		Series: Series{
			DT: []int{3600}, GT: []float64{5000}, FT: []float64{0},
			PN: []float64{0.0002}, PE: []float64{0},
		},
		EtaC: f64(1), EtaD: f64(1),
	}
}

// TestDemandRateObjectiveAndFoldIn is the solver-level demand-rate case: the
// demand-rate charge (-prc_p_exc_imp * pMaxImpExc = -4000) must appear in the
// clean objective_value, and grid_import[t] must be n[t] + excImp[t] (the
// demand-rate fold-in), not the capped n alone.
func TestDemandRateObjectiveAndFoldIn(t *testing.T) {
	in := demandRateInput()
	p, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	mo := buildModel(p)
	sol, err := mo.stage(mo.costCoeffs(), 0, time.Time{}, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if sol == nil || !sol.HasSolution() {
		t.Fatal("no solution")
	}

	n := sol.Value(mo.n[0])
	exc := sol.Value(mo.excImp[0])
	if math.Abs(n-1000) > 1e-6 {
		t.Fatalf("n = %v, want 1000 (p_max_imp cap)", n)
	}
	if math.Abs(exc-4000) > 1e-6 {
		t.Fatalf("excImp = %v, want 4000", exc)
	}
	if peak := sol.Value(mo.pMaxImpExc); math.Abs(peak-4000) > 1e-6 {
		t.Fatalf("pMaxImpExc = %v, want 4000", peak)
	}

	res := mo.finalizeStatus(sol, sol.Status)
	if res.ObjectiveValue == nil {
		t.Fatal("objective_value is nil")
	}
	// clean = -(n+exc)*p_N - prc*pMaxImpExc = -1 - 4000.
	want := -5000*0.0002 - 1.0*4000
	if math.Abs(*res.ObjectiveValue-want) > 1e-6 {
		t.Fatalf("objective %v, want %v (demand-rate charge included)", *res.ObjectiveValue, want)
	}
	if math.Abs(res.GridImport[0]-(n+exc)) > 1e-9 {
		t.Fatalf("grid_import[0] = %v, want n+excImp = %v", res.GridImport[0], n+exc)
	}
	if math.Abs(res.GridImport[0]-5000) > 1e-6 {
		t.Fatalf("grid_import[0] = %v, want 5000", res.GridImport[0])
	}
}
