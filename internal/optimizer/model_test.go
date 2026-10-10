package optimizer

import (
	"math"
	"testing"

	"github.com/bartolsthoorn/gohighs/highs"
)

func TestModelBalanceHolds(t *testing.T) {
	p, _ := twoStepProblem() // one battery, PV on, house load, no strategy
	mo := buildModel(p)
	mo.m.ColCosts = zeroCosts(mo) // any objective; feasibility is what we check
	mo.m.Maximize = true
	sol, err := mo.m.Solve(highs.WithOutput(false))
	if err != nil || !sol.HasSolution() {
		t.Fatalf("solve failed: %v %v", err, sol)
	}
	// RowValues[balanceRow[t]] must equal gt[t] - ft[t] + net, i.e. the row is satisfied.
	// Evaluate independently from ColValues instead: sum_i(d-c) + ft + (n+excImp) - (e+excExp) - gt == 0
	for step := 0; step < mo.T; step++ {
		bal := p.Series.FT[step] - p.Series.GT[step]
		for i := range p.Batteries {
			bal += value(mo, sol, mo.d[i][step]) - value(mo, sol, mo.c[i][step])
		}
		bal += gridImport(mo, sol, step) - gridExport(mo, sol, step)
		if math.Abs(bal) > 1e-3 {
			t.Fatalf("t=%d balance off by %v", step, bal)
		}
	}
}

// TestModelForcesDischargeFromOverfullBattery is Review Focus 2: an initial SOC above
// s_max makes the sMaxPen slack positive, and the cost objective penalizes it, so a cost
// solve must discharge toward s_max. Assert the final SOC is closer to s_max, i.e. below
// the initial value.
func TestModelForcesDischargeFromOverfullBattery(t *testing.T) {
	p, _ := overfullProblem()
	mo := buildModel(p)
	mo.m.ColCosts = expandRow(mo, mo.costRow)
	mo.m.Maximize = true
	sol, err := mo.m.Solve(highs.WithOutput(false))
	if err != nil || !sol.HasSolution() {
		t.Fatalf("solve failed: %v %v", err, sol)
	}
	initial := p.Batteries[0].SInitial
	final := value(mo, sol, mo.s[0][mo.T-1])
	if final >= initial {
		t.Fatalf("expected discharge toward s_max: final SOC %v not below initial %v", final, initial)
	}
}

// TestModelGridImportCapGatedByExceedance checks spec 5.2.3: n is capped at
// p_max_imp*h, and the excess is routed through excImp only once the cap is hit.
func TestModelGridImportCapGatedByExceedance(t *testing.T) {
	p, _ := gridCapProblem()
	mo := buildModel(p)
	mo.m.ColCosts = expandRow(mo, []coef{{mo.n[0], 1}}) // maximize import, force the cap
	mo.m.Maximize = true
	sol, err := mo.m.Solve(highs.WithOutput(false))
	if err != nil || !sol.HasSolution() {
		t.Fatalf("solve failed: %v %v", err, sol)
	}
	if n := value(mo, sol, mo.n[0]); n > 1000+1e-6 {
		t.Fatalf("import %v exceeds p_max_imp cap 1000", n)
	}
	if exc := value(mo, sol, mo.excImp[0]); math.Abs(exc-4000) > 1e-3 {
		t.Fatalf("exceedance %v, want 4000", exc)
	}
}

// --- helpers ---

func value(mo *model, sol *highs.Solution, col int) float64 {
	if col < 0 || col >= len(sol.ColValues) {
		return 0
	}
	return sol.ColValues[col]
}

func gridImport(mo *model, sol *highs.Solution, t int) float64 {
	v := value(mo, sol, mo.n[t])
	if mo.excImp != nil {
		v += value(mo, sol, mo.excImp[t])
	}
	return v
}

func gridExport(mo *model, sol *highs.Solution, t int) float64 {
	v := value(mo, sol, mo.e[t])
	if mo.excExp != nil {
		v += value(mo, sol, mo.excExp[t])
	}
	return v
}

func zeroCosts(mo *model) []float64 {
	return make([]float64, mo.m.NumVars())
}

// expandRow turns a sparse coefficient row into a dense ColCosts vector.
func expandRow(mo *model, row []coef) []float64 {
	costs := make([]float64, mo.m.NumVars())
	for _, c := range row {
		costs[c.col] += c.val
	}
	return costs
}

func twoStepProblem() (Problem, error) {
	p := Problem{
		Strategy: Strategy{ChargingStrategy: "none", DischargingStrategy: "none"},
		EtaC:     0.95,
		EtaD:     0.95,
		Series: Series{
			DT: []int{3600, 3600},
			GT: []float64{1000, 1000},
			FT: []float64{2000, 0},
			PN: []float64{0.0003, 0.0003},
			PE: []float64{0.0001, 0.0001},
		},
		Batteries: []Bat{{
			SCapacity: 5000, SMin: 0, SMax: 5000, SInitial: 1000,
			CMin: 0, CMax: 2000, DMax: 2000, PA: 0.0002,
		}},
	}
	return p, nil
}

func overfullProblem() (Problem, error) {
	p := Problem{
		Strategy: Strategy{ChargingStrategy: "none", DischargingStrategy: "none"},
		EtaC:     0.95,
		EtaD:     0.95,
		Series: Series{
			DT: []int{3600, 3600},
			GT: []float64{0, 0},
			FT: []float64{0, 0},
			PN: []float64{0.0002, 0.0002},
			PE: []float64{0, 0},
		},
		Batteries: []Bat{{
			DischargeToGrid: true,
			SCapacity:       10000, SMin: 0, SMax: 5000, SInitial: 8000,
			CMin: 0, CMax: 2000, DMax: 2000, PA: 0,
		}},
	}
	return p, nil
}

func gridCapProblem() (Problem, error) {
	pm := 1000.0
	p := Problem{
		Strategy: Strategy{ChargingStrategy: "none", DischargingStrategy: "none"},
		Grid:     Grid{PmaxImp: &pm},
		EtaC:     0.95,
		EtaD:     0.95,
		Series: Series{
			DT: []int{3600},
			GT: []float64{5000},
			FT: []float64{0},
			PN: []float64{0.0002},
			PE: []float64{0},
		},
		Batteries: []Bat{{
			SCapacity: 5000, SMin: 0, SMax: 5000, SInitial: 0,
			CMin: 0, CMax: 0, DMax: 0, PA: 0,
		}},
	}
	return p, nil
}
