package optimizer

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/alex/optimizer-v2/internal/settings"
	"github.com/bartolsthoorn/gohighs/highs"
)

// caseFile is the test_cases JSON wrapper: the raw request plus the golden
// response. Only the request is needed here; Solve assembles its own Result.
type caseFile struct {
	Request Input `json:"request"`
}

// loadCase reads a vendored golden case and returns its request Input.
func loadCase(t *testing.T, path string) Input {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read case %s: %v", path, err)
	}
	var c caseFile
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("decode case %s: %v", path, err)
	}
	return c.Request
}

// TestSolve024CostObjective is the known-answer cost case: attenuate_demand_peaks
// with no p_max_imp, so only the cost stage runs and the reported clean value
// must match the golden file.
func TestSolve024CostObjective(t *testing.T) {
	in := loadCase(t, "../../test_cases/024-attenuate-demand-peaks.json")
	if _, err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	got, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" && got.Status != "Feasible" {
		t.Fatalf("status %s", got.Status)
	}
	if got.ObjectiveValue == nil {
		t.Fatal("objective_value is nil")
	}
	if math.Abs(*got.ObjectiveValue-(-1263.1579199999999)) > 1e-3 {
		t.Fatalf("objective %v", *got.ObjectiveValue)
	}
}

// TestOvershootSeriesPresence is Review Focus 3: the overshoot arrays exist only
// when the corresponding p_max is configured. An absent p_max_exp must yield a
// zero-length (non-nil) grid_export_overshoot; an explicit p_max_exp = 0 must
// yield a length-T series (the limit is zero but present).
func TestOvershootSeriesPresence(t *testing.T) {
	base := Input{
		Batteries: []Battery{{
			SMin: f64(0), SMax: f64(5000), SInitial: f64(0),
			CMin: f64(0), CMax: f64(1000), DMax: f64(1000), PA: f64(0),
		}},
		Series: Series{
			DT: []int{3600, 3600},
			GT: []float64{0, 0}, FT: []float64{0, 0},
			PN: []float64{0.3, 0.3}, PE: []float64{0, 0},
		},
	}

	t.Run("absent p_max_exp", func(t *testing.T) {
		got, err := Solve(base, settings.Settings{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.GridExportOvershoot) != 0 {
			t.Fatalf("grid_export_overshoot = %d values, want 0", len(got.GridExportOvershoot))
		}
		if got.GridExportOvershoot == nil {
			t.Fatal("grid_export_overshoot must be non-nil")
		}
	})

	t.Run("present p_max_exp zero", func(t *testing.T) {
		in := base
		zero := 0.0
		in.Grid.PmaxExp = &zero
		got, err := Solve(in, settings.Settings{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.GridExportOvershoot) != len(in.Series.GT) {
			t.Fatalf("grid_export_overshoot = %d values, want %d",
				len(got.GridExportOvershoot), len(in.Series.GT))
		}
	})
}

// TestMapStatus covers every status mapping in spec 6.1, including the guard that
// Optimal is never reported without a solution.
func TestMapStatus(t *testing.T) {
	cases := []struct {
		name string
		st   highs.ModelStatus
		has  bool
		want string
	}{
		{"optimal with solution", highs.ModelStatusOptimal, true, "Optimal"},
		{"optimal without solution", highs.ModelStatusOptimal, false, "Not Solved"},
		{"time limit with solution", highs.ModelStatusTimeLimit, true, "Feasible"},
		{"time limit without solution", highs.ModelStatusTimeLimit, false, "Not Solved"},
		{"iteration limit with solution", highs.ModelStatusIterationLimit, true, "Feasible"},
		{"iteration limit without solution", highs.ModelStatusIterationLimit, false, "Not Solved"},
		{"objective bound with solution", highs.ModelStatusObjectiveBound, true, "Feasible"},
		{"objective bound without solution", highs.ModelStatusObjectiveBound, false, "Not Solved"},
		{"objective target with solution", highs.ModelStatusObjectiveTarget, true, "Feasible"},
		{"objective target without solution", highs.ModelStatusObjectiveTarget, false, "Not Solved"},
		{"infeasible", highs.ModelStatusInfeasible, false, "Infeasible"},
		{"infeasible with solution", highs.ModelStatusInfeasible, true, "Infeasible"},
		{"unbounded or infeasible", highs.ModelStatusUnboundedOrInfeasible, false, "Infeasible"},
		{"unbounded", highs.ModelStatusUnbounded, true, "Unbounded"},
		{"not set", highs.ModelStatusNotSet, false, "Not Solved"},
		{"unknown", highs.ModelStatusUnknown, false, "Not Solved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapStatus(tc.st, tc.has); got != tc.want {
				t.Fatalf("mapStatus(%s, %v) = %q, want %q", tc.st, tc.has, got, tc.want)
			}
		})
	}
}

// TestSafetyPassDowngradesBrokenSchedule drives the final status decision: a valid
// solve is reported Optimal, while a solution whose balance row is violated is
// downgraded to Not Solved with an empty payload.
func TestSafetyPassDowngradesBrokenSchedule(t *testing.T) {
	mo := buildModel(toyGoalProblem())
	mo.m.ColCosts = mo.costCoeffs()
	sol, err := mo.m.Solve(
		highs.WithOutput(false),
		highs.WithPresolve("on"),
		highs.WithMIPRelGap(0),
		highs.WithMIPAbsGap(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !mo.safetyCheck(sol) {
		t.Fatal("safety pass rejected a valid schedule")
	}
	if got := mo.finalize(sol).Status; got != "Optimal" {
		t.Fatalf("valid schedule status = %q, want Optimal", got)
	}

	// Break the energy balance (spec 5.2.1) by inflating grid import at step 0.
	broken := &highs.Solution{
		Status:    sol.Status,
		ColValues: append([]float64(nil), sol.ColValues...),
	}
	broken.ColValues[mo.n[0]] += 1000
	if mo.safetyCheck(broken) {
		t.Fatal("safety pass accepted a schedule that breaks the balance rows")
	}
	res := mo.finalize(broken)
	if res.Status != "Not Solved" {
		t.Fatalf("broken schedule status = %q, want Not Solved", res.Status)
	}
	if res.ObjectiveValue != nil {
		t.Fatal("downgraded result must carry a nil objective")
	}
}
