package optimizer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func f64(v float64) *float64 { return &v }

// validBattery returns a complete, valid battery config for a 2-step horizon.
func validBattery() Battery {
	return Battery{
		SMin:     f64(0),
		SMax:     f64(10000),
		SInitial: f64(0),
		CMin:     f64(0),
		CMax:     f64(5000),
		DMax:     f64(5000),
		PA:       f64(0.2),
	}
}

// twoStepSeries returns a complete, equal-length 2-step time series.
func twoStepSeries() Series {
	return Series{
		DT: []int{3600, 3600},
		GT: []float64{1, 1},
		FT: []float64{1, 1},
		PN: []float64{0.3, 0.3},
		PE: []float64{0.1, 0.1},
	}
}

func TestValidateSeriesLengthMismatch(t *testing.T) {
	in := Input{Batteries: []Battery{validBattery()}, Series: Series{
		DT: []int{3600, 3600}, GT: []float64{1, 1}, FT: []float64{1, 1},
		PN: []float64{0.3}, PE: []float64{0.1, 0.1},
	}}
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	if _, ok := ve.Details["time_series.p_N"]; !ok {
		t.Fatalf("want a time_series.p_N detail, got %v", ve.Details)
	}
}

func TestValidateMissingRequiredField(t *testing.T) {
	b := validBattery()
	b.SMax = nil
	in := Input{Batteries: []Battery{b}, Series: twoStepSeries()}
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Details["batteries.0.s_max"] == "" {
		t.Fatalf("want batteries.0.s_max detail, got %v", err)
	}
}

func TestValidateAllZeroGoalIsAllowed(t *testing.T) {
	b := validBattery()
	b.SGoal = []float64{0, 0}
	in := Input{Batteries: []Battery{b}, Series: twoStepSeries()}
	if err := in.Validate(); err != nil {
		t.Fatalf("all-zero s_goal must be valid, got %v", err)
	}
}

func TestValidateDemandLengthMismatch(t *testing.T) {
	b := validBattery()
	b.PDemand = []float64{1}
	in := Input{Batteries: []Battery{b}, Series: twoStepSeries()}
	var ve *ValidationError
	if !errors.As(in.Validate(), &ve) || ve.Details["batteries.0.p_demand"] == "" {
		t.Fatalf("want batteries.0.p_demand detail")
	}
}

// TestValidateCollectsEveryFailure pins the "collect every failure" contract:
// two independent defects must both appear in Details.
func TestValidateCollectsEveryFailure(t *testing.T) {
	b := validBattery()
	b.SMax = nil
	in := Input{Batteries: []Battery{b}, Series: twoStepSeries()}
	in.Series.PN = []float64{0.3} // wrong length
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	if len(ve.Details) < 2 {
		t.Fatalf("want >= 2 details, got %v", ve.Details)
	}
	if ve.Details["batteries.0.s_max"] == "" || ve.Details["time_series.p_N"] == "" {
		t.Fatalf("want both batteries.0.s_max and time_series.p_N, got %v", ve.Details)
	}
}

func TestValidateRequiresBatteriesAndSeries(t *testing.T) {
	err := (&Input{}).Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("empty input must be rejected, got %v", err)
	}
	if ve.Details["batteries"] == "" || ve.Details["time_series"] == "" {
		t.Fatalf("want batteries and time_series details, got %v", ve.Details)
	}
}

func TestValidateRejectsEmptySeries(t *testing.T) {
	in := Input{Batteries: []Battery{validBattery()}, Series: Series{
		DT: []int{3600}, GT: []float64{1}, FT: []float64{},
		PN: []float64{0.3}, PE: []float64{0.1},
	}}
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Details["time_series.ft"] == "" {
		t.Fatalf("want a time_series.ft detail, got %v", err)
	}
}

func TestValidateRejectsZeroEta(t *testing.T) {
	in := Input{Batteries: []Battery{validBattery()}, Series: twoStepSeries(), EtaD: f64(0)}
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Details["eta_d"] == "" {
		t.Fatalf("want an eta_d detail, got %v", err)
	}
}

// TestValidateGoldenRequestsAreValid guards the conformance goal: real golden
// requests contain negative prices/valuations and must pass validation.
func TestValidateGoldenRequestsAreValid(t *testing.T) {
	for _, name := range []string{
		"014-grid-import-limit-violation.json",
		"018-high-soc-initial.json",
		"023-c_min-limit-kept-with-p_demand-set.json",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "test_cases", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var wrapper struct {
			Request Input `json:"request"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := wrapper.Request.Validate(); err != nil {
			t.Fatalf("%s: golden request must be valid, got %v", name, err)
		}
	}
}
