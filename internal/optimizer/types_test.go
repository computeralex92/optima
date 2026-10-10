package optimizer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeDefaults(t *testing.T) {
	smax := 10000.0
	in := Input{Batteries: []Battery{{SMax: &smax}}, Series: Series{GT: []float64{1, 2}}}
	p, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if p.EtaC != 0.95 || p.EtaD != 0.95 {
		t.Fatalf("eta defaults: %+v", p)
	}
	if p.Batteries[0].SCapacity != 10000 {
		t.Fatalf("s_capacity should default to s_max, got %v", p.Batteries[0].SCapacity)
	}
	if p.Strategy.ChargingStrategy != "none" || p.Strategy.DischargingStrategy != "none" {
		t.Fatalf("strategy defaults: %+v", p.Strategy)
	}
}

func TestNormalizeDegenerateHorizon(t *testing.T) {
	smin, smax, si, cmin, cmax, dmax, pa := 0.0, 1000.0, 0.0, 0.0, 1000.0, 0.0, 0.1
	b := Battery{SMin: &smin, SMax: &smax, SInitial: &si, CMin: &cmin, CMax: &cmax, DMax: &dmax, PA: &pa}
	for _, n := range []int{0, 1} {
		in := Input{Batteries: []Battery{b}, Series: Series{DT: make([]int, n), GT: make([]float64, n), FT: make([]float64, n), PN: make([]float64, n), PE: make([]float64, n)}}
		if _, err := in.Normalize(); err != nil {
			t.Fatalf("T=%d: unexpected error %v", n, err)
		}
	}
}

func TestNormalizeResultSlicesAreNeverNull(t *testing.T) {
	b, err := json.Marshal(NewResult())
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, key := range []string{"batteries", "grid_import", "grid_export", "flow_direction", "grid_import_overshoot", "grid_export_overshoot"} {
		if !strings.Contains(got, `"`+key+`":[]`) {
			t.Fatalf("%s must marshal as []: %s", key, got)
		}
	}
}
