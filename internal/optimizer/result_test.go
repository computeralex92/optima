package optimizer

import (
	"math"
	"testing"

	"github.com/bartolsthoorn/gohighs/highs"
)

// TestCleanValue is the analytic clean-value case from the brief: house gt=0, PV
// ft=0, one battery, flat p_N=0.3, goal of 500 Wh at the last step, p_a=0, eta=1.
// Reaching the goal is the only way to spend money, so import = 500 Wh and the
// clean value is -500*0.3 = -150 (no penalties, no preferences).
func TestCleanValue(t *testing.T) {
	p := toyGoalProblem()
	mo := buildModel(p)
	mo.m.ColCosts = mo.costCoeffs()
	sol, err := mo.m.Solve(highs.WithOutput(false))
	if err != nil || !sol.HasSolution() {
		t.Fatalf("solve failed: %v", err)
	}
	want := -(500.0 * 0.3)
	if got := mo.cleanValue(sol); math.Abs(got-want) > 1e-6 {
		t.Fatalf("cleanValue = %v, want %v", got, want)
	}
}

// TestCostCoeffsIsSparseRowExpanded: costCoeffs is the sparse costRow expanded to
// a dense vector of NumVars, zeros elsewhere (ruling R1: single source).
func TestCostCoeffsIsSparseRowExpanded(t *testing.T) {
	p := toyGoalProblem()
	mo := buildModel(p)
	got := mo.costCoeffs()
	if len(got) != mo.m.NumVars() {
		t.Fatalf("len(costCoeffs) = %d, want %d", len(got), mo.m.NumVars())
	}
	// import coefficient: -p_N at step 0.
	if want := -p.Series.PN[0]; math.Abs(got[mo.n[0]]-want) > 1e-12 {
		t.Fatalf("costCoeffs[n[0]] = %v, want %v", got[mo.n[0]], want)
	}
}

// TestAssembleNonSolved: on every non-solved status the payload has empty (non-nil)
// arrays, a nil objective and no limit violations.
func TestAssembleNonSolved(t *testing.T) {
	mo := buildModel(toyGoalProblem())
	for _, status := range []string{"Infeasible", "Unbounded", "Undefined", "Not Solved"} {
		res := mo.assemble(nil, status)
		if res.Status != status {
			t.Fatalf("%s: status = %q", status, res.Status)
		}
		if res.ObjectiveValue != nil {
			t.Fatalf("%s: objective_value must be nil, got %v", status, *res.ObjectiveValue)
		}
		for name, n := range map[string]int{
			"batteries":             len(res.Batteries),
			"grid_import":           len(res.GridImport),
			"grid_export":           len(res.GridExport),
			"flow_direction":        len(res.FlowDirection),
			"grid_import_overshoot": len(res.GridImportOvershoot),
			"grid_export_overshoot": len(res.GridExportOvershoot),
		} {
			if n != 0 {
				t.Fatalf("%s: %s = %d values, want empty", status, name, n)
			}
		}
		if res.GridImport == nil || res.GridExport == nil ||
			res.Batteries == nil || res.GridImportOvershoot == nil ||
			res.GridExportOvershoot == nil || res.FlowDirection == nil {
			t.Fatalf("%s: slices must be non-nil", status)
		}
		if res.LimitViolations.GridImportLimitExceeded || res.LimitViolations.GridExportLimitHit {
			t.Fatalf("%s: violations must be false", status)
		}
	}
}

// TestAssembleOvershootPresence pins the presence rule: overshoot arrays exist only
// when the matching p_max is set, and grid_import folds in excImp only when the
// demand rate (prc_p_exc_imp) is active -- a bare limit leaves excImp out of
// grid_import and reports it only as overshoot.
func TestAssembleOvershootPresence(t *testing.T) {
	prc := 0.001

	t.Run("no limits", func(t *testing.T) {
		p, _ := twoStepProblem()
		mo := buildModel(p)
		res := mo.assemble(blankSol(mo), "Optimal")
		if len(res.GridImportOvershoot) != 0 || len(res.GridExportOvershoot) != 0 {
			t.Fatalf("overshoot must be empty without p_max: %v %v", res.GridImportOvershoot, res.GridExportOvershoot)
		}
		if res.GridImportOvershoot == nil || res.GridExportOvershoot == nil {
			t.Fatal("overshoot slices must be non-nil")
		}
	})

	t.Run("import limit only", func(t *testing.T) {
		p, _ := gridCapProblem()
		mo := buildModel(p)
		sol := blankSol(mo)
		sol.ColValues[mo.n[0]] = 700
		sol.ColValues[mo.excImp[0]] = 300
		res := mo.assemble(sol, "Optimal")
		if len(res.GridImportOvershoot) != mo.T || len(res.GridExportOvershoot) != 0 {
			t.Fatalf("overshoot shape: imp=%v exp=%v", res.GridImportOvershoot, res.GridExportOvershoot)
		}
		if math.Abs(res.GridImportOvershoot[0]-300) > 1e-9 {
			t.Fatalf("overshoot = %v, want 300", res.GridImportOvershoot[0])
		}
		// demand rate inactive: grid_import is n only.
		if math.Abs(res.GridImport[0]-700) > 1e-9 {
			t.Fatalf("grid_import = %v, want 700 (n only)", res.GridImport[0])
		}
		if !res.LimitViolations.GridImportLimitExceeded {
			t.Fatal("import limit exceeded must be true")
		}
	})

	t.Run("demand rate active", func(t *testing.T) {
		p, _ := gridCapProblem()
		p.Grid.PrcPExcImp = &prc
		mo := buildModel(p)
		sol := blankSol(mo)
		sol.ColValues[mo.n[0]] = 700
		sol.ColValues[mo.excImp[0]] = 300
		res := mo.assemble(sol, "Optimal")
		// demand rate active: grid_import = n + excImp.
		if math.Abs(res.GridImport[0]-1000) > 1e-9 {
			t.Fatalf("grid_import = %v, want 1000 (n + excImp)", res.GridImport[0])
		}
		if !res.LimitViolations.GridImportLimitExceeded {
			t.Fatal("import limit exceeded must be true")
		}
	})
}

// TestAssembleSolvedShapes checks the remaining field mappings on a solved payload.
func TestAssembleSolvedShapes(t *testing.T) {
	p, _ := twoStepProblem()
	mo := buildModel(p)
	mo.m.ColCosts = mo.costCoeffs()
	sol, err := mo.m.Solve(highs.WithOutput(false))
	if err != nil || !sol.HasSolution() {
		t.Fatalf("solve failed: %v", err)
	}
	res := mo.assemble(sol, "Optimal")
	if res.ObjectiveValue == nil {
		t.Fatal("objective_value must be set for Optimal")
	}
	if len(res.GridImport) != mo.T || len(res.GridExport) != mo.T || len(res.FlowDirection) != mo.T {
		t.Fatalf("horizon-length arrays: imp=%d exp=%d flow=%d T=%d",
			len(res.GridImport), len(res.GridExport), len(res.FlowDirection), mo.T)
	}
	if len(res.Batteries) != len(p.Batteries) {
		t.Fatalf("batteries = %d, want %d", len(res.Batteries), len(p.Batteries))
	}
	br := res.Batteries[0]
	for step := 0; step < mo.T; step++ {
		if br.ChargingPower[step] != snap(sol.ColValues[mo.c[0][step]]) ||
			br.DischargingPower[step] != snap(sol.ColValues[mo.d[0][step]]) ||
			br.StateOfCharge[step] != snap(sol.ColValues[mo.s[0][step]]) {
			t.Fatalf("battery mapping at step=%d", step)
		}
		if res.FlowDirection[step] != int(math.Round(sol.ColValues[mo.y[step]])) {
			t.Fatalf("flow_direction[%d] = %d", step, res.FlowDirection[step])
		}
	}
}

// TestAssembleRoundsNearlyIntegerFlow: HiGHS returns a binary a hair off the
// integer (0.99999999999999). flow_direction must round it to 1; truncating
// reports a step that exports energy as importing.
func TestAssembleRoundsNearlyIntegerFlow(t *testing.T) {
	mo := buildModel(toyGoalProblem())
	sol := blankSol(mo)
	sol.ColValues[mo.e[0]] = 42
	sol.ColValues[mo.y[0]] = 0.99999999999999134
	sol.ColValues[mo.y[1]] = 1e-13
	res := mo.assemble(sol, "Optimal")
	if res.FlowDirection[0] != 1 {
		t.Fatalf("flow_direction[0] = %d, want 1 (export)", res.FlowDirection[0])
	}
	if res.FlowDirection[1] != 0 {
		t.Fatalf("flow_direction[1] = %d, want 0 (import)", res.FlowDirection[1])
	}
}

// --- helpers ---

// TestSnap floors solver noise and leaves real values untouched.
func TestSnap(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0, 0}, {1e-13, 0}, {-5.68e-14, 0}, {1e-7, 0}, {1e-5, 1e-5}, {-42, -42}, {3.5, 3.5},
	} {
		if got := snap(c.in); got != c.want {
			t.Fatalf("snap(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestAssembleSnapsNoise: the payload must carry no tiny negative powers, and a
// real value must survive. Also proves the assembly is wired to snap.
func TestAssembleSnapsNoise(t *testing.T) {
	mo := buildModel(toyGoalProblem())
	sol := blankSol(mo)
	sol.ColValues[mo.n[0]] = 1e-13       // noise import
	sol.ColValues[mo.n[1]] = -5.68e-14   // noise, negative
	sol.ColValues[mo.c[0][0]] = -1.7e-13 // noise charge
	sol.ColValues[mo.c[0][1]] = 42       // real charge
	res := mo.assemble(sol, "Optimal")
	if res.GridImport[0] != 0 || res.GridImport[1] != 0 {
		t.Fatalf("noise import not snapped: %v %v", res.GridImport[0], res.GridImport[1])
	}
	if res.Batteries[0].ChargingPower[0] != 0 {
		t.Fatalf("noise charge not snapped: %v", res.Batteries[0].ChargingPower[0])
	}
	if res.Batteries[0].ChargingPower[1] != 42 {
		t.Fatalf("real charge changed: %v", res.Batteries[0].ChargingPower[1])
	}
}

func blankSol(mo *model) *highs.Solution {
	return &highs.Solution{ColValues: make([]float64, mo.m.NumVars())}
}

func toyGoalProblem() Problem {
	return Problem{
		Strategy: Strategy{ChargingStrategy: "none", DischargingStrategy: "none"},
		EtaC:     1, EtaD: 1,
		Series: Series{
			DT: []int{3600, 3600},
			GT: []float64{0, 0},
			FT: []float64{0, 0},
			PN: []float64{0.3, 0.3},
			PE: []float64{0, 0},
		},
		Batteries: []Bat{{
			ChargeFromGrid: true,
			SCapacity:      5000, SMin: 0, SMax: 5000, SInitial: 0,
			CMin: 0, CMax: 1000, DMax: 0, PA: 0,
			SGoal: []float64{0, 500},
		}},
	}
}
