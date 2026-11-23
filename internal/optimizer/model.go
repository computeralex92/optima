package optimizer

import (
	"math"
	"sort"

	"github.com/bartolsthoorn/gohighs/highs"
)

// bigM is the global big-M used where a per-step cap cannot tighten a direction
// constraint (spec 5.2.2). Matches the original's default.
const bigM = 1e6

const (
	// hedgeTieDiscount prices the hedge just below its worth, so a schedule that
	// only ties the worst case keeps the plan the forecast prefers.
	hedgeTieDiscount = 1e-3
	// hedgeWindow is how far the worst case looks back. The schedule is replanned
	// long before a day has passed, so older forecast errors are behind it;
	// counting from the horizon start would grow the reserve day by day.
	hedgeWindow = 86400
)

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

	// Hedge against forecast errors (Bertsimas and Sim), nil unless active.
	uImp, uImpSum, uChg, uRes []int

	peakSides   []string
	penaltyBase float64

	costRow, prefRow, peakRow, startRow []coef

	// prefAfterC is the preference value stage C or the joint probe reached,
	// recorded unconditionally when either runs. It is a seam for the in-package
	// regression that stage D cannot trade that value away; nil when neither ran.
	prefAfterC *float64

	// costProven reports whether the cost stage proved the money it reports. A
	// tie break can prove its own objective under the cost bound, never that
	// bound itself, so Optimal requires both.
	costProven bool

	// Observability (spec 9), filled by run and surfaced through SolveLogged.
	stages      map[string]float64
	path        string
	prefSummary string
	contSummary string
}

// buildModel adds every variable and structural constraint from spec 5.2. Stage
// bound rows (6) are added later by the cascade. The stage coefficient rows are
// built here as side products so later tasks only expand them.
func buildModel(p Problem) *model {
	T := len(p.Series.GT)
	mo := &model{
		m:          &highs.Model{Maximize: true},
		T:          T,
		prob:       p,
		costProven: true,
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

	// worst-case recourse to the forecast error; see hedged and worstDeviation
	if mo.hedged() {
		mo.uImp = make([]int, T)
		mo.uImpSum = make([]int, T)
		mo.uChg = make([]int, T)
		mo.uRes = make([]int, T)
		for t := 0; t < T; t++ {
			mo.uImp[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
			mo.uImpSum[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
			mo.uChg[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
			mo.uRes[t] = mo.addVar(0, highs.Inf(), highs.Continuous)
		}
	}

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

	// 15. hedge against forecast errors, see hedged and worstDeviation
	if mo.uImp != nil {
		worst := mo.worstDeviation()
		var reserve []int
		for i, b := range p.Batteries {
			if b.DMax > 0 {
				reserve = append(reserve, i)
			}
		}
		for t := 0; t < T; t++ {
			// extra import accumulates: running <= running[t-1] + u_imp + u_chg
			var r1 coefRow
			r1.add(mo.uImpSum[t], 1)
			if t > 0 {
				r1.add(mo.uImpSum[t-1], -1)
			}
			r1.add(mo.uImp[t], -1)
			r1.add(mo.uChg[t], -1)
			mo.addLe(r1, 0)

			// extra import replaces a discharge the shortfall made impossible or
			// covers the shortfall as it arrives; it cannot be booked ahead cheaper
			var r2 coefRow
			r2.add(mo.uImp[t], 1)
			for _, i := range reserve {
				r2.add(mo.d[i][t], -1)
			}
			arriving := worst[t]
			if t > 0 {
				arriving -= worst[t-1]
			}
			mo.addLe(r2, math.Max(arriving, 0))

			// a cheap step may refill the shortfall with unused charge power
			var r3 coefRow
			r3.add(mo.uChg[t], 1)
			refill := 0.0
			for _, i := range reserve {
				if !p.Batteries[i].ChargeFromGrid {
					continue
				}
				roundTrip := p.EtaC * p.EtaD
				r3.add(mo.c[i][t], roundTrip)
				refill += roundTrip * p.Batteries[i].CMax * mo.h[t]
			}
			mo.addLe(r3, refill)

			// stored Wh reach the load at the discharge efficiency
			var r4 coefRow
			r4.add(mo.uImpSum[t], 1)
			supplied := worst[t]
			for _, i := range reserve {
				r4.add(mo.s[i][t], p.EtaD)
				r4.add(mo.sMinPen[i][t], p.EtaD)
				supplied += p.EtaD * p.Batteries[i].SMin
			}
			mo.addGe(r4, supplied)

			mo.addSingle(mo.uImpSum[t], 1, worst[t])

			// the reserve the worst case consumes instead of importing, what
			// step t adds to it
			var r5 coefRow
			r5.add(mo.uRes[t], 1)
			r5.add(mo.uImpSum[t], 1)
			usedBefore := worst[t]
			if t > 0 {
				r5.add(mo.uImpSum[t-1], -1)
				usedBefore -= worst[t-1]
			}
			mo.addGe(r5, usedBefore)
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

		// Without discharge the room only shrinks, so a rest below c_min fills
		// in one slot; the charger stops once the battery is full, that slot may
		// charge less than c_min. A larger rest splits into slots of c_min to
		// c_max each, so the constant bound suffices.
		restToFill := math.Inf(1)
		if b.DMax <= 0 {
			restToFill = math.Max(b.SMax-b.SInitial, 0) / p.EtaC
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
					cm := math.Min(b.CMin*mo.h[t], restToFill)
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
				cm := math.Min(b.CMin*mo.h[t], restToFill)
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

	// 16. circuit charge-power caps: the summed charge of the listed batteries.
	// Discharging is not counted, and each entry is enforced independently.
	for _, c := range p.Circuits {
		for t := 0; t < T; t++ {
			var r coefRow
			for _, i := range c.Batteries {
				r.add(mo.c[i][t], 1)
			}
			mo.addLe(r, c.PMax*mo.h[t])
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

	// cost of the budgeted forecast error: the extra import it forces at the
	// import price of the step it is drawn in, and the reserve it consumes at
	// what the plan values that energy at. A negative price would reward drawing
	// more, so it is floored at zero; a load Wh refilled through the batteries
	// costs the round trip.
	if mo.uImp != nil {
		for t := 0; t < T; t++ {
			price := math.Max(p.Series.PN[t], 0) * (1 - hedgeTieDiscount)
			mo.costRow = append(mo.costRow,
				coef{mo.uImp[t], -price},
				coef{mo.uChg[t], -price / (p.EtaC * p.EtaD)},
			)
		}
		stored := 0.0
		for _, b := range p.Batteries {
			if b.DMax > 0 {
				stored = math.Max(stored, b.PA)
			}
		}
		// No less than the cheapest import, so on a flat tariff holding the
		// reserve ties with importing and the preferences decide.
		reservePrice := math.Max(math.Max(maxFloat(p.Series.PE), stored/p.EtaD), math.Max(minFloat(p.Series.PN), 0)) * (1 - hedgeTieDiscount)
		for _, col := range mo.uRes {
			mo.costRow = append(mo.costRow, coef{col, -reservePrice})
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

// hedged reports whether the model carries the hedge: a budget and a step whose
// forecast can deviate.
func (mo *model) hedged() bool {
	u := mo.prob.Uncertainty
	if u.Budget <= 0 || (u.FtDev <= 0 && u.GtDev <= 0) {
		return false
	}
	for t := 0; t < mo.T; t++ {
		if mo.deviation(t) > 0 {
			return true
		}
	}
	return false
}

// deviation is the adverse forecast deviation of step t in Wh, less solar and
// more demand at once, beyond what the step spills anyway. Surplus above what all
// batteries can take in the step is exported or curtailed whatever the schedule,
// so a shortfall up to it only reduces that and never reaches the batteries.
func (mo *model) deviation(t int) float64 {
	u := mo.prob.Uncertainty
	ts := mo.prob.Series
	deviation := u.FtDev*ts.FT[t] + u.GtDev*ts.GT[t]
	spill := ts.FT[t] - ts.GT[t]
	for _, b := range mo.prob.Batteries {
		spill -= b.CMax * mo.h[t]
	}
	return math.Max(deviation-math.Max(spill, 0), 0)
}

// worstDeviation is the budgeted worst case per step: the largest `budget`
// deviations of the day up to it, Wh. A fractional budget counts its last step
// in part, as in Bertsimas and Sim.
func (mo *model) worstDeviation() []float64 {
	T := mo.T
	budget := math.Min(mo.prob.Uncertainty.Budget, float64(T))
	size := int(math.Ceil(budget))
	part := budget - math.Floor(budget)

	end := make([]float64, T)
	dev := make([]float64, T)
	cum := 0.0
	for t := 0; t < T; t++ {
		cum += float64(mo.prob.Series.DT[t])
		end[t] = cum
		dev[t] = mo.deviation(t)
	}

	worst := make([]float64, T)
	for t := 0; t < T; t++ {
		first := 0
		for first <= t && end[first] <= end[t]-hedgeWindow {
			first++
		}
		window := append([]float64(nil), dev[first:t+1]...)
		sort.Sort(sort.Reverse(sort.Float64Slice(window)))
		sum := 0.0
		for i := 0; i < len(window) && i < size; i++ {
			sum += window[i]
		}
		if part > 0 && size > 0 && len(window) >= size {
			sum -= (1 - part) * window[size-1]
		}
		worst[t] = sum
	}
	return worst
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
