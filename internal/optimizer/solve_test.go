package optimizer

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

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

// TestSolve024PeakFlat is the Task 10 smoothing known answer: case 024 is
// attenuate_demand_peaks over a flat, PV-less load, so the active import side must
// end up flat (the dev[t] roughness term) at the minimal horizon peak. The flat
// profile is what the peak stage plus smoothing produce; the golden file has this
// exact constant.
func TestSolve024PeakFlat(t *testing.T) {
	got, err := Solve(loadCase(t, "../../test_cases/024-attenuate-demand-peaks.json"), settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" {
		t.Fatalf("status %s", got.Status)
	}
	if spread := maxFloat(got.GridImport) - minFloat(got.GridImport); spread > 1 {
		t.Fatalf("grid_import spread %.4f, want <= 1; series %v", spread, got.GridImport)
	}
	if peak := maxFloat(got.GridImport); math.Abs(peak-1052.6316) > 1e-2 {
		t.Fatalf("import peak %.4f, want golden 1052.6316", peak)
	}
}

// TestSolvePeakActivePeakMinimized checks the remaining attenuate_* goldens: the
// active side's horizon maximum must sit at the golden minimum. 025 (feed-in) and
// 026 (both sides) are forced non-flat by the feed-in shape, so peak minimality,
// not flatness, is the property to lock.
func TestSolvePeakActivePeakMinimized(t *testing.T) {
	cases := []struct {
		name             string
		path             string
		impPeak, expPeak float64
	}{
		{"025 feedin peaks", "../../test_cases/025-attenuate-feedin-peaks.json", 0, 3894.7368},
		{"026 grid peaks", "../../test_cases/026-attenuate-grid-peaks.json", 1157.8947, 4000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Solve(loadCase(t, tc.path), settings.Settings{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "Optimal" {
				t.Fatalf("status %s", got.Status)
			}
			if peak := maxFloat(got.GridImport); math.Abs(peak-tc.impPeak) > 1e-2 {
				t.Fatalf("import peak %.4f, want %.4f; series %v", peak, tc.impPeak, got.GridImport)
			}
			if peak := maxFloat(got.GridExport); math.Abs(peak-tc.expPeak) > 1e-2 {
				t.Fatalf("export peak %.4f, want %.4f; series %v", peak, tc.expPeak, got.GridExport)
			}
		})
	}
}

// peakPriorityInput is an attenuate_demand_peaks request whose cost optimum is
// degenerate in the charging schedule (flat 0.3 price, no load) and whose only
// non-peak T3 term is c_priority > 0. c_priority prefers charging early, which
// front-loads the schedule and raises the import peak. Only a peak stage ahead of
// the preference stage can hold the horizon maximum at the even-spread minimum.
//
// T = 30, goal 24000 Wh at eta_c 0.95, so the grid must draw 24000/0.95 =
// 25263.157 Wh; spread evenly that is an import peak of 842.105 W.
func peakPriorityInput() Input {
	const T = 30
	grid := true
	cp := 1
	dt := make([]int, T)
	gt := make([]float64, T)
	ft := make([]float64, T)
	pn := make([]float64, T)
	pe := make([]float64, T)
	sgoal := make([]float64, T)
	for i := 0; i < T; i++ {
		dt[i] = 3600
		pn[i] = 0.3
	}
	sgoal[T-1] = 24000
	return Input{
		Strategy: Strategy{ChargingStrategy: "attenuate_demand_peaks"},
		Batteries: []Battery{{
			ChargeFromGrid: &grid, SCapacity: f64(50000), SMin: f64(0), SMax: f64(50000), SInitial: f64(0),
			CMin: f64(0), CMax: f64(8000), DMax: f64(0), PA: f64(0), CPriority: &cp, SGoal: sgoal,
		}},
		Series: Series{DT: dt, GT: gt, FT: ft, PN: pn, PE: pe},
		EtaC:   f64(0.95), EtaD: f64(0.95),
	}
}

// TestPeakStageLevelsBeforePriority is the genuine Task 10 regression: the peak
// objective (T2) must run before the preference objective (T3), so a c_priority
// that wants early charging cannot raise the horizon peak. Without the peak stage
// the preference stage fronts the charge and the import peak is 4210.53 W
// (25263.157/6) instead of the even-spread 842.105 W.
func TestPeakStageLevelsBeforePriority(t *testing.T) {
	got, err := Solve(peakPriorityInput(), settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" {
		t.Fatalf("status %s", got.Status)
	}
	if peak := maxFloat(got.GridImport); math.Abs(peak-842.1052631578948) > 1e-2 {
		t.Fatalf("import peak %.4f, want even-spread minimum 842.1053; series %v", peak, got.GridImport)
	}
	if spread := maxFloat(got.GridImport) - minFloat(got.GridImport); spread > 1 {
		t.Fatalf("grid_import spread %.4f, want flat; series %v", spread, got.GridImport)
	}
}

// TestPeakStagePinsActivePeak locks the mechanism: after the cascade runs for a
// peak strategy, each active peak variable carries a finite upper bound (the pin
// stage C smooths under). Before the pin was wired the bound stayed +Inf.
func TestPeakStagePinsActivePeak(t *testing.T) {
	in := peakPriorityInput()
	p, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	mo := buildModel(p)
	if _, err := mo.run(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, side := range mo.peakSides {
		if hi := mo.m.ColUpper[mo.peakVar(side)]; math.IsInf(hi, 1) {
			t.Fatalf("active peak %q is not pinned (ColUpper +Inf)", side)
		}
	}
}

// TestSolve009DischargeBeforeImport is the preference-stage known answer: case 009
// sets discharge_before_import, so the only discharge-capable battery must be
// drained to s_min before the first grid import, and the import must not start
// earlier than the golden schedule's step 20. Before Task 8 the cost-only cascade
// imports at step 18 with the battery still holding 859 Wh, so the shape check is
// what fails.
func TestSolve009DischargeBeforeImport(t *testing.T) {
	in := loadCase(t, "../../test_cases/009-discharge-before-import.json")
	p, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" {
		t.Fatalf("status %s", got.Status)
	}
	if got.ObjectiveValue == nil {
		t.Fatal("objective_value is nil")
	}
	if math.Abs(*got.ObjectiveValue-2.66254976413256) > 1e-3 {
		t.Fatalf("objective %v, want 2.66254976413256", *got.ObjectiveValue)
	}

	firstImp := firstAbove(got.GridImport)
	if firstImp < 0 {
		t.Fatal("schedule never imports from the grid")
	}
	const goldenFirstImport = 20
	if firstImp < goldenFirstImport {
		t.Fatalf("first grid import at step %d, earlier than golden step %d", firstImp, goldenFirstImport)
	}
	for i, b := range got.Batteries {
		if p.Batteries[i].DMax <= 0 {
			continue
		}
		d := firstAbove(b.DischargingPower)
		if d < 0 {
			t.Fatalf("battery %d never discharges", i)
		}
		if d >= firstImp {
			t.Fatalf("battery %d first discharges at %d, not before first import at %d", i, d, firstImp)
		}
		if soc := b.StateOfCharge[firstImp]; soc > p.Batteries[i].SMin+1 {
			t.Fatalf("battery %d SOC %.2f at first import step %d, want drained to s_min %.2f",
				i, soc, firstImp, p.Batteries[i].SMin)
		}
	}
}

// firstAbove returns the index of the first value above a small threshold, or -1.
func firstAbove(v []float64) int {
	for i, x := range v {
		if x > 1e-6 {
			return i
		}
	}
	return -1
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

// TestFinalizeStatusUsesDecidingStage locks the cascade's status rule: the answer
// reports the deciding stage's status, which is the last retained stage's status
// (spec 6.1). A retained proven preference stage decides Optimal; a retained
// truncated preference stage decides Feasible; an unretained stage leaves the
// previous (cost) status standing.
func TestFinalizeStatusUsesDecidingStage(t *testing.T) {
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

	// Pretend the cost stage stopped at a time limit with an incumbent.
	const costStatus = highs.ModelStatusTimeLimit

	cases := []struct {
		name     string
		retained *highs.Solution
		want     string
	}{
		{"retained proven preference stage", &highs.Solution{Status: highs.ModelStatusOptimal}, "Optimal"},
		{"retained truncated preference stage", &highs.Solution{Status: highs.ModelStatusTimeLimit}, "Feasible"},
		{"unretained stage keeps the cost status", nil, "Feasible"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := decidingStatus(costStatus, tc.retained)
			if got := mo.finalizeStatus(sol, st).Status; got != tc.want {
				t.Fatalf("status %q, want %q", got, tc.want)
			}
		})
	}

	// A retained proven preference stage over a proven cost stage stays Optimal.
	st := decidingStatus(highs.ModelStatusOptimal, &highs.Solution{Status: highs.ModelStatusOptimal})
	if got := mo.finalizeStatus(sol, st).Status; got != "Optimal" {
		t.Fatalf("status %q, want Optimal", got)
	}
}

// TestHasNonzero is the stage-C gate: buildModel appends zero-valued terms (for
// example c_priority == 0 for every battery/step), so the gate must look for a
// nonzero coefficient, not merely a non-empty row.
func TestHasNonzero(t *testing.T) {
	cases := []struct {
		name string
		row  []coef
		want bool
	}{
		{"one nonzero", []coef{{0, -1}}, true},
		{"nonzero after zero", []coef{{0, 0}, {1, 3}}, true},
		{"empty", nil, false},
		{"all zero", []coef{{0, 0}, {1, 0}, {2, 0}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasNonzero(tc.row); got != tc.want {
				t.Fatalf("hasNonzero(%v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}

// TestSolve022PureCost guards the gate fix: case 022 has no strategy and
// c_priority 0, so every prefRow term is zero and stage C must be skipped. Money
// must be unaffected, so the clean objective still matches the golden value.
func TestSolve022PureCost(t *testing.T) {
	in := loadCase(t, "../../test_cases/022-feed-in-cap-recovery.json")
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
	if math.Abs(*got.ObjectiveValue-200.38) > 1e-3 {
		t.Fatalf("objective %v, want 200.38", *got.ObjectiveValue)
	}
}

// continuityInput is one c_min > 0 battery whose SOC goals (s[1] >= 800, s[5] >=
// 1500) are jointly reachable in one contiguous window ([0,1,2] charges 500 Wh at
// 900 s >> 1500 Wh total). Flat prices make that window exactly as cheap as the
// fragmented schedule ([0,1,5], c_min forces 250 Wh granularity) the cost stage
// settles on, so stage D can trade the split for one session without spending
// money. active sets c_active, which means the device enters the horizon charging.
func continuityInput(active bool) Input {
	ac := active
	grid := true
	noGrid := false
	return Input{
		Batteries: []Battery{{
			ChargeFromGrid: &grid, DischargeToGrid: &noGrid,
			SCapacity: f64(5000), SMin: f64(0), SMax: f64(5000), SInitial: f64(0),
			CMin: f64(1000), CMax: f64(2000), DMax: f64(0), PA: f64(0),
			CActive: &ac, SGoal: []float64{0, 800, 0, 0, 0, 1500},
		}},
		Series: Series{
			DT: []int{900, 900, 900, 900, 900, 900},
			GT: []float64{0, 0, 0, 0, 0, 0}, FT: []float64{0, 0, 0, 0, 0, 0},
			PN: []float64{0.0003, 0.0003, 0.0003, 0.0003, 0.0003, 0.0003},
			PE: []float64{0, 0, 0, 0, 0, 0},
		},
		EtaC: f64(1), EtaD: f64(1),
	}
}

// chargeSessions counts 0 -> positive transitions in charging power. Step 0 is not
// a start when the device is active (c_active: it enters the horizon charging).
func chargeSessions(c []float64, active bool) int {
	n := 0
	prev := 0.0
	for t, v := range c {
		if v > 1e-6 && prev <= 1e-6 && !(t == 0 && active) {
			n++
		}
		prev = v
	}
	return n
}

// TestContinuityReducesChargeStarts is the stage-D known answer: the cost stage
// splits the charges across two sessions, and continuity, pinned to the same cost,
// collapses them into one. The clean objective must still be the cost optimum, so
// the stage cannot have spent money to do it.
func TestContinuityReducesChargeStarts(t *testing.T) {
	got, err := Solve(continuityInput(false), settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" {
		t.Fatalf("status %s, want Optimal", got.Status)
	}
	if got.ObjectiveValue == nil {
		t.Fatal("objective_value is nil")
	}
	// 1500 Wh at the flat 0.0003 price -> clean cost -0.45.
	if math.Abs(*got.ObjectiveValue-(-0.45)) > 1e-6 {
		t.Fatalf("objective %v, want -0.45: continuity must not spend money", *got.ObjectiveValue)
	}
	if n := chargeSessions(got.Batteries[0].ChargingPower, false); n != 1 {
		t.Fatalf("charge sessions = %d, want 1; charging=%v", n, got.Batteries[0].ChargingPower)
	}
}

// TestContinuityActiveDeviceHasNoStarts: with c_active the device enters the horizon
// charging, so a schedule that keeps it on from step 0 counts zero starts. The same
// cost-optimal collapse as above reaches it.
func TestContinuityActiveDeviceHasNoStarts(t *testing.T) {
	got, err := Solve(continuityInput(true), settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" {
		t.Fatalf("status %s, want Optimal", got.Status)
	}
	c := got.Batteries[0].ChargingPower
	if c[0] <= 1e-6 {
		t.Fatalf("active device does not charge from step 0; charging=%v", c)
	}
	if n := chargeSessions(c, true); n != 0 {
		t.Fatalf("counted starts = %d, want 0; charging=%v", n, c)
	}
}

// TestContinuitySpendsNoMoney pins the stage-D bound: the clean value of the full
// cascade equals the cost stage's own optimum on the same model. Continuity only
// rearranges charging under the bound, never trades money for fewer starts.
func TestContinuitySpendsNoMoney(t *testing.T) {
	in := continuityInput(false)
	p, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	mo := buildModel(p)
	csol, err := mo.stage(mo.costCoeffs(), 0, time.Time{}, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	costValue := mo.cleanValue(csol)

	got, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectiveValue == nil {
		t.Fatal("objective_value is nil")
	}
	if math.Abs(*got.ObjectiveValue-costValue) > 1e-6 {
		t.Fatalf("continuity objective %v != cost optimum %v", *got.ObjectiveValue, costValue)
	}
	if n := chargeSessions(got.Batteries[0].ChargingPower, false); n != 1 {
		t.Fatalf("charge sessions = %d, want 1", n)
	}
}

// preferenceRegressionInput is a request with a nonzero preference row
// (discharge_before_import weights grid import by 5e-6*(T-t)) AND a c_min > 0
// battery, so stage C runs and stage D is eligible. c_min == c_max makes each
// active step charge a fixed 500 Wh, so the early goal (s[1] >= 800) forces two
// early steps and the remaining step goes as late as the preference wants. The
// cost stage settles on that late schedule, so stage C confirms rather than
// improves, which is exactly the case the preference pin must still cover.
func preferenceRegressionInput() Input {
	grid, noGrid := true, false
	return Input{
		Strategy: Strategy{ChargingStrategy: "none", DischargingStrategy: "discharge_before_import"},
		Batteries: []Battery{{
			ChargeFromGrid: &grid, DischargeToGrid: &noGrid,
			SCapacity: f64(5000), SMin: f64(0), SMax: f64(5000), SInitial: f64(0),
			CMin: f64(2000), CMax: f64(2000), DMax: f64(0), PA: f64(0),
			SGoal: []float64{0, 800, 0, 0, 0, 1500},
		}},
		Series: Series{
			DT: []int{900, 900, 900, 900, 900, 900},
			GT: []float64{0, 0, 0, 0, 0, 0}, FT: []float64{0, 0, 0, 0, 0, 0},
			PN: []float64{0.003, 0.003, 0.003, 0.003, 0.003, 0.003},
			PE: []float64{0, 0, 0, 0, 0, 0},
		},
		EtaC: f64(1), EtaD: f64(1),
	}
}

// prefFromGridImport recomputes the discharge_before_import preference objective
// from the reported grid import: prefRow adds, once per battery,
// n[t] * (-minImportPrice * 5e-6 * (T-t)) for every step.
func prefFromGridImport(imp []float64, minPrice float64, nb int) float64 {
	T := len(imp)
	v := 0.0
	for b := 0; b < nb; b++ {
		for t := 0; t < T; t++ {
			v += imp[t] * (-minPrice * 5e-6 * float64(T-t))
		}
	}
	return v
}

// TestContinuityKeepsPreferenceValue is the regression for the preference pin:
// stage D must not lower the preference value stage C achieved. The pin is added
// after stage C whether or not it improved, so the merged-into-one-session
// alternative that continuity would otherwise pick cannot win. Without the pin the
// cost bound alone lets stage D move the last charge from t=5 to t=2 (equal cost,
// fewer starts) and the reported preference drops.
func TestContinuityKeepsPreferenceValue(t *testing.T) {
	in := preferenceRegressionInput()
	p, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	mo := buildModel(p)
	got, err := mo.run(settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if mo.prefAfterC == nil {
		t.Fatal("preference stage did not run; nothing was recorded")
	}
	// One battery, so the preference row is a single copy of the import weighting.
	final := prefFromGridImport(got.GridImport, minf64(p.Series.PN), len(p.Batteries))
	if final < *mo.prefAfterC-pinEps {
		t.Fatalf("stage D lowered preference: final %v < post-stage-C %v", final, *mo.prefAfterC)
	}
}

// objectiveAtLeast fails unless v meets the conformance floor v >= want - tol,
// with tol = max(1e-4, 1e-5*|want|) (spec 12).
func objectiveAtLeast(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatal("objective_value is nil")
	}
	tol := math.Max(1e-4, 1e-5*math.Abs(want))
	if *got < want-tol {
		t.Fatalf("objective %v, want >= %v (tol %v)", *got, want, tol)
	}
}

// TestSolve013GridExportLimitHit verifies the soft export limit (spec 5.2.3): a
// p_max_exp that the schedule wants to exceed must be penalized (an overshoot
// series) rather than made infeasible, and the violation flag set. Case 013 sets
// only p_max_exp = 15000 and the golden exports beyond it, so the answer stays
// Optimal with a positive grid_export_overshoot entry.
func TestSolve013GridExportLimitHit(t *testing.T) {
	in := loadCase(t, "../../test_cases/013-grid-export-limit-hit.json")
	got, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" && got.Status != "Feasible" {
		t.Fatalf("status %s, want Optimal/Feasible (soft limit)", got.Status)
	}
	if !got.LimitViolations.GridExportLimitHit {
		t.Fatal("limit_violations.grid_export_limit_hit = false, want true")
	}
	if T := len(in.Series.GT); len(got.GridExportOvershoot) != T {
		t.Fatalf("len(grid_export_overshoot) = %d, want T = %d", len(got.GridExportOvershoot), T)
	}
	if maxFloat(got.GridExportOvershoot) <= 0 {
		t.Fatalf("grid_export_overshoot %v has no positive entry", got.GridExportOvershoot)
	}
	objectiveAtLeast(t, got.ObjectiveValue, 17.527010877312602)
}

// TestSolve014GridImportLimitViolation is the import-side twin: p_max_imp = 3300
// is exceeded in the golden schedule, so the import overshoot is present and
// positive, the violation flag is set and the solve stays Optimal. No demand rate
// is configured, so excImp is reported only as overshoot, not folded into
// grid_import (spec 5.3).
func TestSolve014GridImportLimitViolation(t *testing.T) {
	in := loadCase(t, "../../test_cases/014-grid-import-limit-violation.json")
	got, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Optimal" && got.Status != "Feasible" {
		t.Fatalf("status %s, want Optimal/Feasible (soft limit)", got.Status)
	}
	if !got.LimitViolations.GridImportLimitExceeded {
		t.Fatal("limit_violations.grid_import_limit_exceeded = false, want true")
	}
	if T := len(in.Series.GT); len(got.GridImportOvershoot) != T {
		t.Fatalf("len(grid_import_overshoot) = %d, want T = %d", len(got.GridImportOvershoot), T)
	}
	if maxFloat(got.GridImportOvershoot) <= 0 {
		t.Fatalf("grid_import_overshoot %v has no positive entry", got.GridImportOvershoot)
	}
	objectiveAtLeast(t, got.ObjectiveValue, -7.094530917713852)
}

// minf64 is minFloat for a concrete slice, kept local to the test.
func minf64(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}
