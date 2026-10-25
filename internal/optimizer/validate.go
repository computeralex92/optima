package optimizer

import (
	"fmt"
	"sort"
	"strings"
)

// ValidationError reports every failed check at once. Details is keyed by
// field path (e.g. "batteries.0.s_max") so the HTTP layer can render one
// problem list per response.
type ValidationError struct {
	Message string
	Details map[string]string
}

func (e *ValidationError) Error() string {
	if len(e.Details) == 0 {
		return e.Message
	}
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, e.Details[k]))
	}
	return e.Message + ": " + strings.Join(parts, "; ")
}

var chargingStrategies = map[string]bool{
	"none": true, "charge_before_export": true, "attenuate_demand_peaks": true,
	"attenuate_feedin_peaks": true, "attenuate_grid_peaks": true,
}

var dischargingStrategies = map[string]bool{
	"none": true, "discharge_before_import": true,
}

// Validate checks the request against the openapi.yaml contract and returns a
// *ValidationError carrying every failure, or nil. T is the horizon length
// inferred from len(Series.GT). An absent (nil) optional value is never an
// error; an all-zero s_goal is a valid goal.
func (in *Input) Validate() error {
	d := map[string]string{}
	add := func(key, msg string) {
		if prev, ok := d[key]; ok {
			d[key] = prev + "; " + msg
			return
		}
		d[key] = msg
	}

	// batteries (minItems 1) and time_series are required. Series is a value
	// type, so "present" means at least one of its arrays is non-nil.
	if len(in.Batteries) == 0 {
		add("batteries", "required field is missing")
	}
	if in.Series.DT == nil && in.Series.GT == nil && in.Series.FT == nil &&
		in.Series.PN == nil && in.Series.PE == nil {
		add("time_series", "required field is missing")
	}

	if in.Strategy.ChargingStrategy != "" && !chargingStrategies[in.Strategy.ChargingStrategy] {
		add("strategy.charging_strategy", "unknown charging strategy")
	}
	if in.Strategy.DischargingStrategy != "" && !dischargingStrategies[in.Strategy.DischargingStrategy] {
		add("strategy.discharging_strategy", "unknown discharging strategy")
	}

	if in.Grid.PmaxImp != nil && *in.Grid.PmaxImp < 0 {
		add("grid.p_max_imp", "must be >= 0")
	}
	if in.Grid.PmaxExp != nil && *in.Grid.PmaxExp < 0 {
		add("grid.p_max_exp", "must be >= 0")
	}
	if in.Grid.PrcPExcImp != nil && *in.Grid.PrcPExcImp < 0 {
		add("grid.prc_p_exc_imp", "must be >= 0")
	}

	if in.EtaC != nil && (*in.EtaC <= 0 || *in.EtaC > 1) {
		add("eta_c", "must be in (0, 1]")
	}
	if in.EtaD != nil && (*in.EtaD <= 0 || *in.EtaD > 1) {
		add("eta_d", "must be in (0, 1]")
	}

	// Horizon. GT defines T; every series must be non-empty (minItems 1) and
	// every other series must match GT's length.
	t := len(in.Series.GT)
	for _, s := range []struct {
		key string
		n   int
	}{
		{"time_series.dt", len(in.Series.DT)},
		{"time_series.gt", len(in.Series.GT)},
		{"time_series.ft", len(in.Series.FT)},
		{"time_series.p_N", len(in.Series.PN)},
		{"time_series.p_E", len(in.Series.PE)},
	} {
		if s.n == 0 {
			add(s.key, "[] is too short")
		}
	}
	seriesLen := []struct {
		key string
		n   int
	}{
		{"time_series.dt", len(in.Series.DT)},
		{"time_series.ft", len(in.Series.FT)},
		{"time_series.p_N", len(in.Series.PN)},
		{"time_series.p_E", len(in.Series.PE)},
	}
	for _, s := range seriesLen {
		if s.n != t {
			add(s.key, fmt.Sprintf("length %d does not match time_series.gt length %d", s.n, t))
		}
	}
	for i, v := range in.Series.DT {
		if v <= 0 {
			add(fmt.Sprintf("time_series.dt.%d", i), "must be > 0")
		}
	}
	for _, s := range []struct {
		key  string
		vals []float64
	}{
		{"time_series.gt", in.Series.GT}, {"time_series.ft", in.Series.FT},
	} {
		if hasNegative(s.vals) {
			add(s.key, "must be >= 0")
		}
	}

	for i, b := range in.Batteries {
		prefix := fmt.Sprintf("batteries.%d.", i)
		for _, req := range []struct {
			key string
			val *float64
		}{
			{"s_min", b.SMin}, {"s_max", b.SMax}, {"s_initial", b.SInitial},
			{"c_min", b.CMin}, {"c_max", b.CMax}, {"d_max", b.DMax}, {"p_a", b.PA},
		} {
			if req.val == nil {
				add(prefix+req.key, "required field is missing")
			}
		}
		// Prices/valuations (p_N, p_E, p_a) may legitimately be negative and
		// are intentionally excluded from non-negativity enforcement.
		for _, nn := range []struct {
			key string
			val *float64
		}{
			{"s_capacity", b.SCapacity}, {"s_min", b.SMin}, {"s_max", b.SMax},
			{"s_initial", b.SInitial}, {"c_min", b.CMin}, {"c_max", b.CMax},
			{"d_max", b.DMax},
		} {
			if nn.val != nil && *nn.val < 0 {
				add(prefix+nn.key, "must be >= 0")
			}
		}
		if b.CPriority != nil && (*b.CPriority < 0 || *b.CPriority > 2) {
			add(prefix+"c_priority", "must be in [0, 2]")
		}
		// s_initial <= s_capacity, with s_capacity defaulting to s_max.
		if b.SInitial != nil {
			capacity := b.SCapacity
			if capacity == nil {
				capacity = b.SMax
			}
			if capacity != nil && *b.SInitial > *capacity {
				add(prefix+"s_initial", "must be <= s_capacity")
			}
		}
		if b.PDemand != nil {
			if len(b.PDemand) != t {
				add(prefix+"p_demand", fmt.Sprintf("length %d does not match time_series.gt length %d", len(b.PDemand), t))
			}
			if hasNegative(b.PDemand) {
				add(prefix+"p_demand", "must be >= 0")
			}
		}
		if b.SGoal != nil {
			if len(b.SGoal) != t {
				add(prefix+"s_goal", fmt.Sprintf("length %d does not match time_series.gt length %d", len(b.SGoal), t))
			}
			if hasNegative(b.SGoal) {
				add(prefix+"s_goal", "must be >= 0")
			}
		}
	}

	if len(d) == 0 {
		return nil
	}
	return &ValidationError{
		Message: fmt.Sprintf("request validation failed (%d problem(s))", len(d)),
		Details: d,
	}
}

func hasNegative(vals []float64) bool {
	for _, v := range vals {
		if v < 0 {
			return true
		}
	}
	return false
}
