package optimizer

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/computeralex92/optima/internal/settings"
)

// circuitInput is the upstream circuit fixture: two grid-charged batteries with
// a profitable final storage value, so both charge at full power unless a
// circuit caps their sum.
func circuitInput(circuits []Circuit) Input {
	chargeFromGrid := true
	battery := func() Battery {
		return Battery{
			ChargeFromGrid: &chargeFromGrid,
			SMin:           f64(0),
			SMax:           f64(20000),
			SInitial:       f64(0),
			CMin:           f64(0),
			CMax:           f64(11000),
			DMax:           f64(0),
			PA:             f64(0.0003),
		}
	}
	return Input{
		Batteries: []Battery{battery(), battery()},
		Series: Series{
			DT: []int{3600, 3600, 3600},
			GT: []float64{0, 0, 0},
			FT: []float64{0, 0, 0},
			PN: []float64{0.0001, 0.0002, 0.0002},
			PE: []float64{0, 0, 0},
		},
		Circuits: circuits,
	}
}

func chargeSums(res Result) []float64 {
	out := make([]float64, len(res.GridImport))
	for t := range out {
		for _, b := range res.Batteries {
			out[t] += b.ChargingPower[t]
		}
	}
	return out
}

func TestCircuitLimitsChargeSum(t *testing.T) {
	control, err := Solve(circuitInput(nil), settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if maxFloat(chargeSums(control)) <= 11000 {
		t.Fatalf("control does not exceed the limit: %v", chargeSums(control))
	}

	limited, err := Solve(circuitInput([]Circuit{{PMax: 11000, Batteries: []int{0, 1}}}), settings.Settings{})
	if err != nil || limited.Status != "Optimal" {
		t.Fatalf("limited solve: %v %s", err, limited.Status)
	}
	for _, total := range chargeSums(limited) {
		if total > 11001 {
			t.Fatalf("charge sum %v exceeds 11000", total)
		}
	}
}

// TestNonBindingCircuitKeepsSchedule: a cap at the sum of the individual maxima
// changes nothing.
func TestNonBindingCircuitKeepsSchedule(t *testing.T) {
	control, err := Solve(circuitInput(nil), settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := Solve(circuitInput([]Circuit{{PMax: 22000, Batteries: []int{0, 1}}}), settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(control, loose) {
		t.Fatalf("non-binding circuit changed the schedule")
	}
}

func TestValidateRejectsBadCircuits(t *testing.T) {
	in := circuitInput([]Circuit{{PMax: 11000, Batteries: []int{0, 2}}})
	if err := in.Validate(); err == nil {
		t.Fatal("out-of-range battery index must be rejected")
	}

	in = circuitInput([]Circuit{{PMax: 0, Batteries: []int{0}}})
	if err := in.Validate(); err == nil {
		t.Fatal("zero p_max must be rejected")
	}

	in = circuitInput([]Circuit{{PMax: 11000, Batteries: []int{0, 0}}})
	if err := in.Validate(); err == nil {
		t.Fatal("duplicate battery index must be rejected")
	}
}

// TestCircuitGoldenRequestIsValid guards the vendored contract examples.
func TestCircuitGoldenRequestIsValid(t *testing.T) {
	raw, err := json.Marshal(circuitInput([]Circuit{{PMax: 11000, Batteries: []int{0, 1}}}))
	if err != nil {
		t.Fatal(err)
	}
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("circuit request must be valid: %v", err)
	}
}
