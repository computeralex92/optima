package optimizer

import (
	"math"

	"github.com/bartolsthoorn/gohighs/highs"
)

// bigM is the global big-M used where a per-step cap cannot tighten a direction
// constraint (spec 5.2.2). Matches the original's default.
const bigM = 1e6

// coef is one entry of a sparse objective row: column index and coefficient.
type coef struct {
	col int
	val float64
}

// coefRow accumulates a sparse constraint row, summing repeated columns so the
// gohighs duplicate-merge (last wins) cannot silently drop a term.
type coefRow struct {
	cols []int
	vals []float64
}

func (r *coefRow) add(col int, val float64) {
	if val == 0 {
		return
	}
	for i, c := range r.cols {
		if c == col {
			r.vals[i] += val
			return
		}
	}
	r.cols = append(r.cols, col)
	r.vals = append(r.vals, val)
}

func (r *coefRow) addScaled(other coefRow, s float64) {
	for i, c := range other.cols {
		r.add(c, other.vals[i]*s)
	}
}

// model holds the built HiGHS model plus column indices and reusable coefficient
// rows for the lexicographic stages.
type model struct {
	m    *highs.Model
	T    int
	prob Problem

	h, invH []float64 // hours per step and its reciprocal

	c, d, s [][]int // [battery][step]
	n, e    []int
	y       []int
	zCD     [][]int
	zC      [][]int // nil per battery when c_min == 0

	sMinPen, sMaxPen [][]int
	goalPen          [][]int // -1 per step when s_goal[t] <= 0
	demandPen        [][]int // -1 per step when p_demand is absent
	zDem, zFull      [][]int // -1 per step when p_demand is absent
	start            [][]int // -1 per step unless c_min > 0

	excImp, zImpLim []int // nil unless p_max_imp is set
	excExp, zExpLim []int // nil unless p_max_exp is set
	pMaxImpExc      int   // -1 unless the demand rate is active

	peakImp, peakExp int   // >= horizon peak power per side
	dev              []int // profile roughness

	peakSides   []string
	penaltyBase float64

	costRow, prefRow, peakRow, startRow []coef

	// prefAfterC is the preference value stage C reached, recorded unconditionally
	// when the preference stage runs. It is a seam for the in-package regression
	// that stage D cannot trade that value away; nil when no preference stage ran.
	prefAfterC *float64

	// Observability (spec 9), filled by run and surfaced through SolveLogged.
	stages      map[string]float64
	prefSummary string
	contSummary string
}

// buildModel adds every variable and structural constraint from spec 5.2. Stage
// bound rows (6) are added later by the cascade. The stage coefficient rows are
// built here as side products so later tasks only expand them.
func buildModel(p Problem) *model {
	T := len(p.Series.GT)
	mo := &model{
		m:    &highs.Model{Maximize: true},
		T:    T,
		prob: p,
	}
	mo.h = make([]float64, T)
	mo.invH = make([]float64, T)
	for t := 0; t < T; t++ {
		mo.h[t] = float64(p.Series.DT[t]) / 3600
		mo.invH[t] = 1 / mo.h[t]
	}

	demandActive := p.Grid.PmaxImp != nil && p.Grid.PrcPExcImp != nil

	// Penalty base: the largest positive currency/Wh rate any economic term can
	// command, floored at 0.1e-3 (spec 5.3), so penalties stay positive under
	// zero or negative prices.
	minDT := math.Inf(1)
	for _, d := range p.Series.DT {
		if float64(d) < minDT {
			minDT = float64(d)
		}
	}
	base := []float64{maxFloat(p.Series.PN), maxFloat(p.Series.PE), 0.1e-3}
	for _, b := range p.Batteries {
		base = append(base, b.PA)
	}
	if demandActive {
		// currency/W converted over the shortest step, see the original.
		base = append(base, *p.Grid.PrcPExcImp*3600/minDT)
	}
	mo.penaltyBase = maxFloat(base)
	minImportPrice := minFloat(p.Series.PN)

	prcSocExcPen := mo.penaltyBase * 1e3
	prcGoalPen := mo.penaltyBase * 1e2
	prcGridImpPen := mo.penaltyBase * 1e2
	prcGridExpPen := mo.penaltyBase * 1e2
	prcPPeak := mo.penaltyBase * 1e-3
	// ramp weight for the step-to-step roughness dev[t], per the reference.
	prcPRamp := mo.penaltyBase * 1e-5

	mo.peakSides = activePeakSides(p.Strategy)

	// --- variables (spec 5.1) ---

	nb := len(p.Batteries)
	mo.c = make([][]int, nb)
	mo.d = make([][]int, nb)
	mo.s = make([][]int, nb)
	mo.zCD = make([][]int, nb)
	mo.zC = make([][]int, nb)
	mo.sMinPen = make([][]int, nb)
	mo.sMaxPen = make([][]int, nb)
	mo.goalPen = make([][]int, nb)
	mo.demandPen = make([][]int, nb)
	mo.zDem = make([][]int, nb)
	mo.zFull = make([][]int, nb)
	mo.start = make([][]int, nb)

	for i, b := range p.Batteries {
		mo.c[i] = make([]int, T)
		mo.d[i] = make([]int, T)
		mo.s[i] = make([]int, T)
		mo.zCD[i] = make([]int, T)
		mo.sMinPen[i] = make([]int, T)
		mo.sMaxPen[i] = make([]int, T)
		mo.goalPen[i] = make([]int, T)
		mo.demandPen[i] = make([]int, T)
		mo.zDem[i] = make([]int, T)
		mo.zFull[i] = make([]int, T)
		mo.start[i] = make([]int, T)
		for t := 0; t < T; t++ {
			mo.c[i][t] = mo.addVar(0, b.CMax*mo.h[t], highs.Continuous)
			mo.d[i][t] = mo.addVar(0, b.DMax*mo.h[t], highs.Continuous)
			mo.s[i][t] = mo.addVar(0, b.SCapacity, highs.Continuous)
			mo.zCD[i][t] = mo.addVar(0, 1, highs.Integer)
			mo.sMinPen[i][t] = mo.addVar(0, highs.Inf(), highs.Continuous)
			mo.sMaxPen[i][t] = mo.addVar(0, highs.Inf(), highs.Continuous)

			mo.goalPen[i][t] = -1
			if b.SGoal != nil && b.SGoal[t] > 0 {
				mo.goalPen[i][t] = mo.addVar(0, highs.Inf(), highs.Continuous)
			}

			mo.demandPen[i][t] = -1
			mo.zDem[i][t] = -1
			mo.zFull[i][t] = -1
			if b.PDemand != nil {
				mo.demandPen[i][t] = mo.addVar(0, highs.Inf(), highs.Continuous)
				mo.zDem[i][t] = mo.addVar(0, 1, highs.Integer)
				mo.zFull[i][t] = mo.addVar(0, 1, highs.Integer)
			}

			mo.start[i][t] = -1
			if b.CMin > 0 {
				mo.start[i][t] = mo.addVar(0, 1, highs.Integer)
			}
		}
		if b.CMin > 0 {
			mo.zC[i] = make([]int, T)
			for t := 0; t < T; t++ {
				mo.zC[i][t] = mo.addVar(0, 1, highs.Integer)
			}
		}
	}

	mo.n = make([]int, T)
	mo.e = make([]int, T)
	mo.y = make([]int, T)
	mo.dev = make([]int, T)
	for t := 0; t < T; t++ {
		mo.n[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
		mo.e[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
		mo.y[t] = mo.addVar(0, 1, highs.Integer)
		mo.dev[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
	}
	if p.Grid.PmaxImp != nil {
		mo.excImp = make([]int, T)
		mo.zImpLim = make([]int, T)
		for t := 0; t < T; t++ {
			mo.excImp[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
			mo.zImpLim[t] = mo.addVar(0, 1, highs.Integer)
		}
	}
	if p.Grid.PmaxExp != nil {
		mo.excExp = make([]int, T)
		mo.zExpLim = make([]int, T)
		for t := 0; t < T; t++ {
			mo.excExp[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
			mo.zExpLim[t] = mo.addVar(0, 1, highs.Integer)
		}
	}
	mo.pMaxImpExc = -1
	if demandActive {
		mo.pMaxImpExc = mo.addVar(0, highs.Inf(), highs.Continuous)
	}
	mo.peakImp = mo.addVar(0, highs.Inf(), highs.Continuous)
	mo.peakExp = mo.addVar(0, highs.Inf(), highs.Continuous)

	// --- constraints (spec 5.2) ---

	// 1. energy balance per step
	for t := 0; t < T; t++ {
		var r coefRow
		for i := range p.Batteries {
			r.add(mo.c[i][t], -1)
			r.add(mo.d[i][t], 1)
		}
		r.add(mo.n[t], 1)
		if mo.excImp != nil {
			r.add(mo.excImp[t], 1)
		}
		r.add(mo.e[t], -1)
		if mo.excExp != nil {
			r.add(mo.excExp[t], -1)
		}
		mo.addEq(r, p.Series.GT[t]-p.Series.FT[t])
	}

	// 2. direction big-M with tightened caps
	capDExp := 0.0
	capCImp := 0.0
	for _, b := range p.Batteries {
		if b.DischargeToGrid {
			capDExp += b.DMax
		}
		if b.ChargeFromGrid {
			capCImp += b.CMax
		}
	}
	for t := 0; t < T; t++ {
		mExp := p.Series.FT[t] + capDExp*mo.h[t]
		if p.Grid.PmaxImp != nil {
			mExp = bigM
		}
		mImp := p.Series.GT[t] + capCImp*mo.h[t]
		if p.Grid.PmaxExp != nil {
			mImp = bigM
		}
		var r1 coefRow
		r1.add(mo.e[t], 1)
		r1.add(mo.y[t], -mExp)
		mo.addLe(r1, 0)

		var r2 coefRow
		r2.add(mo.n[t], 1)
		r2.add(mo.y[t], mImp)
		mo.addLe(r2, mImp)
	}

	// 3. soft grid power caps, exceedance gated by the limit binary
	if p.Grid.PmaxImp != nil {
		pm := *p.Grid.PmaxImp
		for t := 0; t < T; t++ {
			lim := pm * mo.h[t]
			mo.addSingle(mo.n[t], 1, lim) // n <= lim
			var r1 coefRow
			r1.add(mo.n[t], 1)
			r1.add(mo.zImpLim[t], lim)
			mo.addGe(r1, lim) // n + lim*z >= lim
			var r2 coefRow
			r2.add(mo.excImp[t], 1)
			r2.add(mo.zImpLim[t], bigM)
			mo.addLe(r2, bigM) // exc + M*z <= M
		}
	}
	if p.Grid.PmaxExp != nil {
		pm := *p.Grid.PmaxExp
		for t := 0; t < T; t++ {
			lim := pm * mo.h[t]
			mo.addSingle(mo.e[t], 1, lim)
			var r1 coefRow
			r1.add(mo.e[t], 1)
			r1.add(mo.zExpLim[t], lim)
			mo.addGe(r1, lim)
			var r2 coefRow
			r2.add(mo.excExp[t], 1)
			r2.add(mo.zExpLim[t], bigM)
			mo.addLe(r2, bigM)
		}
	}

	// 4. demand-rate coupling
	if demandActive {
		for t := 0; t < T; t++ {
			var r coefRow
			r.add(mo.excImp[t], 1)
			r.add(mo.pMaxImpExc, -mo.h[t])
			mo.addLe(r, 0)
		}
	}

	// 12. horizon peak per side (the peak binary is minimized by stage B)
	for _, side := range []string{"imp", "exp"} {
		for t := 0; t < T; t++ {
			r := mo.gridPower(side, t)
			r.add(mo.peakVar(side), -1)
			mo.addLe(r, 0)
		}
	}

	// 13. roughness, only for the sides the active peak strategy levels
	for _, side := range mo.peakSides {
		for t := 1; t < T; t++ {
			r1 := mo.gridPower(side, t)
			r1.addScaled(mo.gridPower(side, t-1), -1)
			r1.add(mo.dev[t], 1)
			mo.addGe(r1, 0) // dev >= p[t] - p[t-1]

			r2 := mo.gridPower(side, t-1)
			r2.addScaled(mo.gridPower(side, t), -1)
			r2.add(mo.dev[t], 1)
			mo.addGe(r2, 0) // dev >= p[t-1] - p[t]
		}
	}

	// 5-11, 14. per battery
	for i, b := range p.Batteries {
		// 5. SOC soft bounds
		for t := 0; t < T; t++ {
			var r1 coefRow
			r1.add(mo.sMaxPen[i][t], 1)
			r1.add(mo.s[i][t], -1)
			mo.addGe(r1, -b.SMax)

			var r2 coefRow
			r2.add(mo.sMinPen[i][t], 1)
			r2.add(mo.s[i][t], 1)
			mo.addGe(r2, b.SMin)
		}

		// 6. dynamics
		if T > 0 {
			var r coefRow
			r.add(mo.s[i][0], 1)
			r.add(mo.c[i][0], -p.EtaC)
			r.add(mo.d[i][0], 1/p.EtaD)
			mo.addEq(r, b.SInitial)
		}
		for t := 1; t < T; t++ {
			var r coefRow
			r.add(mo.s[i][t], 1)
			r.add(mo.s[i][t-1], -1)
			r.add(mo.c[i][t], -p.EtaC)
			r.add(mo.d[i][t], 1/p.EtaD)
			mo.addEq(r, 0)
		}

		// 7. charge goals
		if b.SGoal != nil {
			for t := 1; t < T; t++ {
				if b.SGoal[t] > 0 {
					var r coefRow
					r.add(mo.s[i][t], 1)
					r.add(mo.goalPen[i][t], 1)
					mo.addGe(r, b.SGoal[t])
				}
			}
		}

		// 8. charge-demand gating (#19); 9. c_min gating
		if b.PDemand != nil {
			for t := 0; t < T; t++ {
				if b.PDemand[t] > 0 {
					pDemand := math.Min(b.CMax*mo.h[t], b.PDemand[t])

					var r1 coefRow
					r1.add(mo.s[i][t], -1)
					r1.add(mo.zDem[i][t], b.SMax)
					mo.addLe(r1, b.CMin*mo.h[t])

					var r2 coefRow
					r2.add(mo.s[i][t], -1)
					r2.add(mo.zFull[i][t], b.SMax)
					mo.addLe(r2, 0)

					var r3 coefRow
					r3.add(mo.c[i][t], 1)
					r3.add(mo.demandPen[i][t], 1)
					r3.add(mo.zFull[i][t], pDemand)
					mo.addGe(r3, pDemand)
				} else {
					// keep the c_min gating below effective
					mo.addSingle(mo.zDem[i][t], 1, 0)
				}
				if b.CMin > 0 {
					cm := b.CMin * mo.h[t]
					var r1 coefRow
					r1.add(mo.c[i][t], 1)
					r1.add(mo.zDem[i][t], cm)
					r1.add(mo.zC[i][t], -cm)
					mo.addGe(r1, 0)
					var r2 coefRow
					r2.add(mo.c[i][t], 1)
					r2.add(mo.zC[i][t], -b.CMax*mo.h[t])
					mo.addLe(r2, 0)
				}
			}
		} else if b.CMin > 0 {
			for t := 0; t < T; t++ {
				cm := b.CMin * mo.h[t]
				var r1 coefRow
				r1.add(mo.c[i][t], 1)
				r1.add(mo.zC[i][t], -cm)
				mo.addGe(r1, 0)
				var r2 coefRow
				r2.add(mo.c[i][t], 1)
				r2.add(mo.zC[i][t], -b.CMax*mo.h[t])
				mo.addLe(r2, 0)
			}
		}

		// 10. grid permission
		if !b.ChargeFromGrid {
			for t := 0; t < T; t++ {
				var r coefRow
				r.add(mo.c[i][t], 1)
				r.add(mo.y[t], -b.CMax*mo.h[t])
				mo.addLe(r, 0)
			}
		}
		if !b.DischargeToGrid {
			for t := 0; t < T; t++ {
				var r coefRow
				r.add(mo.d[i][t], 1)
				r.add(mo.y[t], b.DMax*mo.h[t])
				mo.addLe(r, b.DMax*mo.h[t])
			}
		}

		// 11. charge/discharge lock
		for t := 0; t < T; t++ {
			var r1 coefRow
			r1.add(mo.d[i][t], 1)
			r1.add(mo.zCD[i][t], -b.DMax*mo.h[t])
			mo.addLe(r1, 0)

			var r2 coefRow
			r2.add(mo.c[i][t], 1)
			r2.add(mo.zCD[i][t], b.CMax*mo.h[t])
			mo.addLe(r2, b.CMax*mo.h[t])
		}

		// 14. continuity: start >= z_c[t] - (z_c[t-1] or c_active)
		if mo.zC[i] != nil {
			active := 0.0
			if b.CActive {
				active = 1
			}
			for t := 0; t < T; t++ {
				var r coefRow
				r.add(mo.start[i][t], 1)
				r.add(mo.zC[i][t], -1)
				rhs := 0.0
				if t == 0 {
					rhs = -active
				} else {
					r.add(mo.zC[i][t-1], 1)
				}
				mo.addGe(r, rhs)
			}
		}
	}

	// --- stage coefficient rows (side products) ---

	for t := 0; t < T; t++ {
		mo.costRow = append(mo.costRow, coef{mo.n[t], -p.Series.PN[t]})
		if p.Grid.PmaxImp != nil {
			mo.costRow = append(mo.costRow, coef{mo.excImp[t], -p.Series.PN[t]})
		}
		mo.costRow = append(mo.costRow, coef{mo.e[t], p.Series.PE[t]})
	}
	for i, b := range p.Batteries {
		if T > 0 {
			mo.costRow = append(mo.costRow, coef{mo.s[i][T-1], b.PA})
		}
		for t := 0; t < T; t++ {
			mo.costRow = append(mo.costRow,
				coef{mo.sMaxPen[i][t], -prcSocExcPen},
				coef{mo.sMinPen[i][t], -prcSocExcPen},
			)
			if mo.goalPen[i][t] >= 0 {
				mo.costRow = append(mo.costRow, coef{mo.goalPen[i][t], -prcGoalPen})
			}
			if b.PDemand != nil {
				w := 1 + float64(T-t)/float64(T)
				mo.costRow = append(mo.costRow, coef{mo.demandPen[i][t], -prcGoalPen * w})
			}
		}
	}
	if demandActive {
		mo.costRow = append(mo.costRow, coef{mo.pMaxImpExc, -*p.Grid.PrcPExcImp})
	}
	if p.Grid.PmaxImp != nil && !demandActive {
		for t := 0; t < T; t++ {
			mo.costRow = append(mo.costRow, coef{mo.excImp[t], -prcGridImpPen})
		}
	}
	if p.Grid.PmaxExp != nil {
		for t := 0; t < T; t++ {
			mo.costRow = append(mo.costRow, coef{mo.excExp[t], -prcGridExpPen * (1 - float64(t)*1e-5)})
		}
	}

	if p.Strategy.ChargingStrategy == "charge_before_export" {
		for range p.Batteries {
			for t := 0; t < T; t++ {
				mo.prefRow = append(mo.prefRow, coef{mo.e[t], -minImportPrice * 2e-5 * float64(T-t)})
			}
		}
	}
	for _, side := range mo.peakSides {
		mo.peakRow = append(mo.peakRow, coef{mo.peakVar(side), -1})
		mo.prefRow = append(mo.prefRow, coef{mo.peakVar(side), -prcPPeak})
	}
	if p.Strategy.DischargingStrategy == "discharge_before_import" {
		for range p.Batteries {
			for t := 0; t < T; t++ {
				mo.prefRow = append(mo.prefRow, coef{mo.n[t], -minImportPrice * 5e-6 * float64(T-t)})
			}
		}
	}
	for i, b := range p.Batteries {
		for t := 0; t < T; t++ {
			v := minImportPrice * 5e-5 * float64(T-t) * float64(b.CPriority)
			mo.prefRow = append(mo.prefRow, coef{mo.c[i][t], v}, coef{mo.d[i][t], v})
		}
	}
	if len(mo.peakSides) > 0 {
		// profile smoothing: minimize the step-to-step change under the pinned peak
		for t := 0; t < T; t++ {
			mo.prefRow = append(mo.prefRow, coef{mo.dev[t], -prcPRamp})
		}
	}
	for i := range p.Batteries {
		if mo.zC[i] == nil {
			continue
		}
		for t := 0; t < T; t++ {
			mo.startRow = append(mo.startRow, coef{mo.start[i][t], -1})
		}
	}

	return mo
}

func (mo *model) addVar(lower, upper float64, vt highs.VariableType) int {
	col := len(mo.m.ColLower)
	mo.m.ColLower = append(mo.m.ColLower, lower)
	mo.m.ColUpper = append(mo.m.ColUpper, upper)
	mo.m.VarTypes = append(mo.m.VarTypes, vt)
	return col
}

func (mo *model) addLe(r coefRow, ub float64) { mo.m.AddSparseRow(highs.NegInf(), r.cols, r.vals, ub) }
func (mo *model) addGe(r coefRow, lb float64) { mo.m.AddSparseRow(lb, r.cols, r.vals, highs.Inf()) }
func (mo *model) addEq(r coefRow, b float64)  { mo.m.AddSparseRow(b, r.cols, r.vals, b) }

// addSingle adds a one-term row a*x <= b.
func (mo *model) addSingle(col int, a, b float64) {
	mo.m.AddSparseRow(highs.NegInf(), []int{col}, []float64{a}, b)
}

// gridPower is the grid power (W) of one side at step t as a sparse row.
func (mo *model) gridPower(side string, t int) coefRow {
	var r coefRow
	inv := mo.invH[t]
	if side == "imp" {
		r.add(mo.n[t], inv)
		if mo.excImp != nil {
			r.add(mo.excImp[t], inv)
		}
	} else {
		r.add(mo.e[t], inv)
		if mo.excExp != nil {
			r.add(mo.excExp[t], inv)
		}
	}
	return r
}

func (mo *model) peakVar(side string) int {
	if side == "imp" {
		return mo.peakImp
	}
	return mo.peakExp
}

func activePeakSides(s Strategy) []string {
	switch s.ChargingStrategy {
	case "attenuate_demand_peaks":
		return []string{"imp"}
	case "attenuate_feedin_peaks":
		return []string{"exp"}
	case "attenuate_grid_peaks":
		return []string{"imp", "exp"}
	}
	return nil
}

func maxFloat(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

func minFloat(v []float64) float64 {
	m := math.Inf(1)
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}
