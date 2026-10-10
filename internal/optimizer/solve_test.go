package optimizer

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/alex/optimizer-v2/internal/settings"
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
