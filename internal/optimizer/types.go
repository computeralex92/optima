package optimizer

// Wire types. JSON tags match the vendored openapi.yaml contract exactly.
// Optional or absent-distinguishable scalars are pointers so a missing value
// differs from an explicit zero; required battery fields are pointers so
// validation can tell a missing field from a zero one.

// Strategy mirrors OptimizerStrategy.
type Strategy struct {
	ChargingStrategy    string `json:"charging_strategy"`
	DischargingStrategy string `json:"discharging_strategy"`
}

// Grid mirrors GridConfig. A nil field means the limit/price is absent.
type Grid struct {
	PmaxImp    *float64 `json:"p_max_imp"`
	PmaxExp    *float64 `json:"p_max_exp"`
	PrcPExcImp *float64 `json:"prc_p_exc_imp"`
}

// Battery mirrors BatteryConfig.
type Battery struct {
	ChargeFromGrid  *bool     `json:"charge_from_grid"`
	DischargeToGrid *bool     `json:"discharge_to_grid"`
	SCapacity       *float64  `json:"s_capacity"`
	SMin            *float64  `json:"s_min"`
	SMax            *float64  `json:"s_max"`
	SInitial        *float64  `json:"s_initial"`
	CMin            *float64  `json:"c_min"`
	CMax            *float64  `json:"c_max"`
	CActive         *bool     `json:"c_active"`
	DMax            *float64  `json:"d_max"`
	PA              *float64  `json:"p_a"`
	PDemand         []float64 `json:"p_demand"`
	SGoal           []float64 `json:"s_goal"`
	CPriority       *int      `json:"c_priority"`
}

// Series mirrors TimeSeries.
type Series struct {
	DT []int     `json:"dt"`
	GT []float64 `json:"gt"`
	FT []float64 `json:"ft"`
	PN []float64 `json:"p_N"`
	PE []float64 `json:"p_E"`
}

// Input mirrors OptimizationInput.
type Input struct {
	Strategy  Strategy  `json:"strategy"`
	Grid      Grid      `json:"grid"`
	Batteries []Battery `json:"batteries"`
	Series    Series    `json:"time_series"`
	EtaC      *float64  `json:"eta_c"`
	EtaD      *float64  `json:"eta_d"`
}

// BatteryResult mirrors BatteryResult.
type BatteryResult struct {
	ChargingPower    []float64 `json:"charging_power"`
	DischargingPower []float64 `json:"discharging_power"`
	StateOfCharge    []float64 `json:"state_of_charge"`
}

// LimitViolations mirrors LimitViolationResult.
type LimitViolations struct {
	GridImportLimitExceeded bool `json:"grid_import_limit_exceeded"`
	GridExportLimitHit      bool `json:"grid_export_limit_hit"`
}

// Result mirrors OptimizationResult. ObjectiveValue is nullable.
type Result struct {
	Status              string          `json:"status"`
	ObjectiveValue      *float64        `json:"objective_value"`
	LimitViolations     LimitViolations `json:"limit_violations"`
	Batteries           []BatteryResult `json:"batteries"`
	GridImport          []float64       `json:"grid_import"`
	GridExport          []float64       `json:"grid_export"`
	FlowDirection       []int           `json:"flow_direction"`
	GridImportOvershoot []float64       `json:"grid_import_overshoot"`
	GridExportOvershoot []float64       `json:"grid_export_overshoot"`
}

// NewResult returns a Result whose slices are all non-nil, so the payload
// marshals them as [] rather than null (spec section 3). Assembly (Task 6)
// fills the values.
func NewResult() Result {
	return Result{
		Batteries:           []BatteryResult{},
		GridImport:          []float64{},
		GridExport:          []float64{},
		FlowDirection:       []int{},
		GridImportOvershoot: []float64{},
		GridExportOvershoot: []float64{},
	}
}

// Problem is the normalized, concrete view the model builder consumes.
type Problem struct {
	Strategy   Strategy
	Grid       Grid // concrete; PmaxImp/PmaxExp/PrcPExcImp stay *float64 (nil = absent)
	Batteries  []Bat
	Series     Series
	EtaC, EtaD float64
}

// Bat is one normalized battery. PDemand/SGoal are nil when absent.
type Bat struct {
	ChargeFromGrid, DischargeToGrid bool
	SCapacity, SMin, SMax, SInitial float64
	CMin, CMax, DMax, PA            float64
	PDemand, SGoal                  []float64 // nil when absent, length T when present
	CPriority                       int
	CActive                         bool
}

// Normalize applies the spec section 3 defaults and returns the concrete
// problem. T is inferred from len(Series.GT). It returns an error only when a
// Bat cannot be constructed; semantic validation belongs to Validate.
func (in *Input) Normalize() (Problem, error) {
	p := Problem{
		Strategy: in.Strategy,
		Grid:     in.Grid,
		Series:   in.Series,
		EtaC:     0.95,
		EtaD:     0.95,
	}
	if in.EtaC != nil {
		p.EtaC = *in.EtaC
	}
	if in.EtaD != nil {
		p.EtaD = *in.EtaD
	}
	if p.Strategy.ChargingStrategy == "" {
		p.Strategy.ChargingStrategy = "none"
	}
	if p.Strategy.DischargingStrategy == "" {
		p.Strategy.DischargingStrategy = "none"
	}

	p.Batteries = make([]Bat, len(in.Batteries))
	for i, b := range in.Batteries {
		smax := floatOr(b.SMax, 0)
		scap := smax
		if b.SCapacity != nil {
			scap = *b.SCapacity
		}
		p.Batteries[i] = Bat{
			ChargeFromGrid:  boolOr(b.ChargeFromGrid, false),
			DischargeToGrid: boolOr(b.DischargeToGrid, false),
			SCapacity:       scap,
			SMin:            floatOr(b.SMin, 0),
			SMax:            smax,
			SInitial:        floatOr(b.SInitial, 0),
			CMin:            floatOr(b.CMin, 0),
			CMax:            floatOr(b.CMax, 0),
			DMax:            floatOr(b.DMax, 0),
			PA:              floatOr(b.PA, 0),
			PDemand:         b.PDemand,
			SGoal:           b.SGoal,
			CPriority:       intOr(b.CPriority, 0),
			CActive:         boolOr(b.CActive, false),
		}
	}
	return p, nil
}

func floatOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}
