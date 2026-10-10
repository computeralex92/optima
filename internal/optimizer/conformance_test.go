package optimizer

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/computeralex92/optima/internal/settings"
)

// tolerance constants, spec section 12.
const (
	balanceTol = 1e-2 // energy balance per step, Wh
	socEps     = 1e-2 // SOC soft-bound slack, Wh
	cminTol    = 1e-2 // c_min gating slack, Wh
	activeTol  = 1e-2 // "is this power nonzero" threshold, Wh
)

// goldenCase is a vendored test_cases JSON file: the raw request plus (usually)
// the reference response. Only status and objective_value are authoritative;
// schedules may differ because acceptance is objective-based (spec 12/13).
type goldenCase struct {
	Request          Input             `json:"request"`
	ExpectedResponse *expectedResponse `json:"expected_response"`
}

// expectedResponse is the subset of the golden payload the harness asserts on.
// ObjectiveValue is a pointer so an explicit null is distinguishable.
type expectedResponse struct {
	Status         string   `json:"status"`
	ObjectiveValue *float64 `json:"objective_value"`
}

// TestConformance is the golden-case harness (spec 12). For every vendored case
// with an expected_response it decodes the request, validates and normalizes it,
// solves with default settings, then asserts the response contract and the model
// invariants. Strict schedule equality is deliberately not asserted: a different
// but equally optimal schedule passes (spec 13).
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping golden-case conformance in -short mode")
	}

	paths, err := filepath.Glob("../../test_cases/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden cases found under ../../test_cases")
	}

	for _, path := range paths {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			c := loadGolden(t, path)
			if c.ExpectedResponse == nil {
				t.Skip("no expected_response")
			}
			runConformanceCase(t, c)
		})
	}
}

// runConformanceCase asserts one case's response against the contract.
func runConformanceCase(t *testing.T, c goldenCase) {
	t.Helper()
	exp := c.ExpectedResponse

	if err := c.Request.Validate(); err != nil {
		t.Fatalf("golden request failed validation: %v", err)
	}
	p, err := c.Request.Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	got, err := Solve(c.Request, settings.Settings{})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}

	if got.Status != exp.Status {
		t.Fatalf("status = %q, want %q", got.Status, exp.Status)
	}

	if exp.ObjectiveValue != nil {
		if got.ObjectiveValue == nil {
			t.Fatal("objective_value is nil, want a value")
		}
		t.Logf("objective got=%v expected=%v", *got.ObjectiveValue, *exp.ObjectiveValue)
		objectiveAtLeast(t, got.ObjectiveValue, *exp.ObjectiveValue)
	} else if got.ObjectiveValue != nil {
		t.Fatalf("objective_value = %v, want nil", *got.ObjectiveValue)
	}

	if got.Status != "Optimal" && got.Status != "Feasible" {
		return
	}

	T := len(p.Series.GT)
	if len(got.GridImport) != T {
		t.Fatalf("len(grid_import) = %d, want T = %d", len(got.GridImport), T)
	}
	if len(got.GridExport) != T {
		t.Fatalf("len(grid_export) = %d, want T = %d", len(got.GridExport), T)
	}
	if len(got.Batteries) != len(p.Batteries) {
		t.Fatalf("len(batteries) = %d, want %d", len(got.Batteries), len(p.Batteries))
	}

	if err := checkBalance(p, got); err != nil {
		t.Fatal(err)
	}
	if err := checkSOCBounds(p, got); err != nil {
		t.Fatal(err)
	}
	if err := checkCMinGating(p, c.Request.Series.DT, got); err != nil {
		t.Fatal(err)
	}
}

// checkBalance reconstructs the physical energy balance from the payload (spec
// 5.2.1): d - c + ft + import - export = gt, so the residual
// ft - gt + sum(d-c) + physicalImport - physicalExport must be ~0 per step.
//
// The payload splits the grid side: a bare p_max_imp reports its exceedance only
// as grid_import_overshoot, while an active demand rate folds excImp into
// grid_import; grid_export never folds in grid_export_overshoot. Reconstruct the
// physical import/export accordingly so no overshoot is dropped or double-counted.
func checkBalance(p Problem, got Result) error {
	demandActive := p.Grid.PmaxImp != nil && p.Grid.PrcPExcImp != nil
	for step := range p.Series.GT {
		residual := p.Series.FT[step] - p.Series.GT[step]
		for i := range got.Batteries {
			residual += got.Batteries[i].DischargingPower[step] - got.Batteries[i].ChargingPower[step]
		}
		imp := got.GridImport[step]
		if !demandActive && step < len(got.GridImportOvershoot) {
			imp += got.GridImportOvershoot[step]
		}
		exp := got.GridExport[step]
		if step < len(got.GridExportOvershoot) {
			exp += got.GridExportOvershoot[step]
		}
		if math.Abs(residual+imp-exp) > balanceTol {
			return fmt.Errorf("t=%d energy balance residual = %.6g, want |.| <= %g", step, residual+imp-exp, balanceTol)
		}
	}
	return nil
}

// checkSOCBounds asserts SOC stays within [s_min - eps, s_max + eps] unless the
// request forces otherwise (soft bounds, spec 5.2.5): a battery whose s_initial
// already lies below s_min (or above s_max) cannot meet the bound at step 0 and
// is allowed to approach it from outside.
func checkSOCBounds(p Problem, got Result) error {
	for i, b := range p.Batteries {
		allowLow := b.SInitial < b.SMin
		allowHigh := b.SInitial > b.SMax
		for step, s := range got.Batteries[i].StateOfCharge {
			if !allowLow && s < b.SMin-socEps {
				return fmt.Errorf("battery %d t=%d SOC %.6g below s_min %.6g", i, step, s, b.SMin)
			}
			if !allowHigh && s > b.SMax+socEps {
				return fmt.Errorf("battery %d t=%d SOC %.6g above s_max %.6g", i, step, s, b.SMax)
			}
		}
	}
	return nil
}

// checkCMinGating asserts the #19 invariant (spec 5.2.9): charging is either 0
// or at least c_min*h[t]. The one exception is topping off: when the room left to
// s_max is itself <= c_min*h[t], a smaller final charge is allowed.
func checkCMinGating(p Problem, dt []int, got Result) error {
	for i, b := range p.Batteries {
		if b.CMin <= 0 {
			continue
		}
		for step, c := range got.Batteries[i].ChargingPower {
			if c <= activeTol {
				continue
			}
			min := b.CMin * float64(dt[step]) / 3600
			if c >= min-cminTol {
				continue
			}
			room := b.SMax - got.Batteries[i].StateOfCharge[step]
			if room <= min+cminTol {
				continue
			}
			return fmt.Errorf("battery %d t=%d charge %.6g below c_min*h[t] %.6g with room %.6g to s_max",
				i, step, c, min, room)
		}
	}
	return nil
}

// TestConformance028LongHorizon is Review Focus 4: the 90 KB, 245-step case must
// solve and its balance must hold within 1e-2 at every step. The table harness
// already covers it; this pin keeps the long-horizon drift reachable on its own.
func TestConformance028LongHorizon(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-horizon case in -short mode")
	}
	c := loadGolden(t, "../../test_cases/028-attenuate-grid-peaks-long-horizon.json")
	p, err := c.Request.Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got, err := Solve(c.Request, settings.Settings{})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if got.Status != "Optimal" && got.Status != "Feasible" {
		t.Fatalf("status = %q, want a solved status", got.Status)
	}
	if len(got.GridImport) != len(p.Series.GT) {
		t.Fatalf("len(grid_import) = %d, want T = %d", len(got.GridImport), len(p.Series.GT))
	}
	if err := checkBalance(p, got); err != nil {
		t.Fatal(err)
	}
}

// TestConformanceChecks catch a broken invariant directly, so a future edit that
// makes a checker too permissive (or the #19 top-off allowance too broad) trips a
// small test rather than silently passing the golden suite.
func TestConformanceChecks(t *testing.T) {
	oneBat := func(b Bat) Problem {
		return Problem{
			Series:    Series{DT: []int{3600}, GT: []float64{0}, FT: []float64{0}, PN: []float64{0}, PE: []float64{0}},
			Batteries: []Bat{b},
		}
	}
	bat := func(smin, smax, sinitial, cmin float64) Bat {
		return Bat{SMin: smin, SMax: smax, SInitial: sinitial, CMin: cmin}
	}
	result := func(c, d, soc, imp, exp float64) Result {
		return Result{
			Batteries:  []BatteryResult{{ChargingPower: []float64{c}, DischargingPower: []float64{d}, StateOfCharge: []float64{soc}}},
			GridImport: []float64{imp}, GridExport: []float64{exp},
		}
	}

	t.Run("balance", func(t *testing.T) {
		p := oneBat(bat(0, 1000, 0, 0))
		if err := checkBalance(p, result(0, 0, 0, 0, 0)); err != nil {
			t.Fatalf("balanced payload rejected: %v", err)
		}
		if err := checkBalance(p, result(0, 0, 0, 5, 0)); err == nil {
			t.Fatal("unbalanced payload (import without sink) accepted")
		}
	})

	t.Run("soc bounds", func(t *testing.T) {
		p := oneBat(bat(10, 20, 10, 0))
		if err := checkSOCBounds(p, result(0, 0, 15, 0, 0)); err != nil {
			t.Fatalf("in-bounds SOC rejected: %v", err)
		}
		if err := checkSOCBounds(p, result(0, 0, 5, 0, 0)); err == nil {
			t.Fatal("SOC below s_min accepted with s_initial in bounds")
		}
		if err := checkSOCBounds(p, result(0, 0, 25, 0, 0)); err == nil {
			t.Fatal("SOC above s_max accepted with s_initial in bounds")
		}

		// Forced by the request: s_initial below s_min / above s_max.
		low := oneBat(bat(10, 20, 0, 0))
		if err := checkSOCBounds(low, result(0, 0, 5, 0, 0)); err != nil {
			t.Fatalf("SOC below s_min rejected despite s_initial < s_min: %v", err)
		}
		high := oneBat(bat(10, 20, 30, 0))
		if err := checkSOCBounds(high, result(0, 0, 30, 0, 0)); err != nil {
			t.Fatalf("SOC above s_max rejected despite s_initial > s_max: %v", err)
		}
	})

	t.Run("c_min gating", func(t *testing.T) {
		dt := []int{3600}
		p := oneBat(bat(0, 1000, 0, 100)) // c_min*h = 100
		if err := checkCMinGating(p, dt, result(0, 0, 0, 0, 0)); err != nil {
			t.Fatalf("zero charge rejected: %v", err)
		}
		if err := checkCMinGating(p, dt, result(150, 0, 0, 0, 0)); err != nil {
			t.Fatalf("charge above c_min rejected: %v", err)
		}
		if err := checkCMinGating(p, dt, result(50, 0, 0, 0, 0)); err == nil {
			t.Fatal("sub-min charge with room to s_max accepted")
		}
		// Topping off: room to s_max <= c_min*h allows a smaller charge.
		if err := checkCMinGating(p, dt, result(50, 0, 1000, 0, 0)); err != nil {
			t.Fatalf("topping-off charge rejected: %v", err)
		}
	})
}

// loadGolden reads and decodes one vendored case file.
func loadGolden(t *testing.T, path string) goldenCase {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read case %s: %v", path, err)
	}
	var c goldenCase
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("decode case %s: %v", path, err)
	}
	return c
}
