package optimizer

import (
	"errors"
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
