package optimizer

import (
	"math"
	"time"

	"github.com/alex/optimizer-v2/internal/settings"
	"github.com/bartolsthoorn/gohighs/highs"
)

// pinEps is the slack allowed when a stage pins its achieved value before the
// next stage runs (spec 6). It is in objective units (currency for the cost
// stage). The golden-case objectives range over ~1e-3 .. 1e3, so 1e-6 forbids a
// later stage from trading away more than floating-point residue while still
// leaving the solver room to absorb it.
const pinEps = 1e-6

// safetyTol is the absolute tolerance of the final feasibility re-check, in the
// model's units (Wh for the energy balance). HiGHS holds rows to ~1e-9, so a
// breach beyond this is a real model violation, not solver noise.
const safetyTol = 1e-4

// Solve is the package entrypoint used by the API layer. The caller validates
// the raw Input; Solve only normalizes it (R5), builds the model and runs the
// cascade.
func Solve(in Input, s settings.Settings) (Result, error) {
	p, err := in.Normalize()
	if err != nil {
		return Result{}, err
	}
	return buildModel(p).run(s)
}

// run drives the lexicographic cascade on one built model. All stages share a
// single wall clock. Stage A (cost) always runs, stage C (preference) runs when
// the preference row has a nonzero coefficient, stage D (continuity) when a
// c_min > 0 battery exists; the peak stage is a no-op until Task 10 wires it.
func (mo *model) run(s settings.Settings) (Result, error) {
	var deadline time.Time
	if s.TimeLimit != nil {
		deadline = time.Now().Add(time.Duration(*s.TimeLimit * float64(time.Second)))
	}

	// Stage A: cost (T1, spec 5.3). Always runs.
	sol, err := mo.stage(mo.costCoeffs(), costGap(mo.prob, s), deadline, s)
	if err != nil {
		return Result{}, err
	}
	// gohighs' HasSolution() is true on TimeLimit/IterationLimit even with no MIP
	// incumbent, so a no-incumbent stop can look Feasible; the safety pass below is
	// the net. A full fix needs primal_solution_status, absent in this binding.
	if sol == nil || !sol.HasSolution() {
		st := highs.ModelStatusNotSet
		if sol != nil {
			st = sol.Status
		}
		return mo.assemble(nil, mapStatus(st, false)), nil
	}

	// The deciding status starts with the cost stage and is superseded whenever a
	// later stage retains its solution (spec 6.1: the last retained stage decides).
	status := sol.Status

	// Pin the achieved cost so later stages cannot trade it away.
	mo.pinLower(mo.costRow, objective(mo.costRow, sol))

	// Stage C: preference (T3, spec 5.3). Runs only when some T3 term is actually
	// active (a nonzero preference coefficient), under the cost bound, at gap 0
	// (proven-optimal tie break). The objective is normalized to unit magnitude so
	// its gap is meaningful (spec 6.2). It can never spend money: the cost pin
	// bounds it, and a worse or unsolved stop leaves the cost-stage solution in
	// place, and with it the cost stage's deciding status.
	if hasNonzero(mo.prefRow) {
		psol, err := mo.stage(mo.prefCoeffs(), 0, deadline, s)
		if err != nil {
			return Result{}, err
		}
		if psol != nil && psol.HasSolution() &&
			objective(mo.prefRow, psol) > objective(mo.prefRow, sol) {
			sol = psol
			status = decidingStatus(status, psol)
		}
		// Pin the achieved preference (spec 6/7) after stage C runs, whether or not
		// it improved: the incumbent's preference value is a floor the later stages
		// cannot trade away. Task 8 deliberately left it unpinned so the peak and
		// continuity stages of Tasks 9-10 could not spend preference value. The
		// recorded value is a seam for the regression in solve_test.go.
		pv := objective(mo.prefRow, sol)
		mo.prefAfterC = &pv
		mo.pinLower(mo.prefRow, pv)
	}

	// Stage B (peak) runs here once Task 10 populates peakRow; it retains through
	// the same deciding-status rule.

	// Stage D: continuity (T4, spec 5.3). Runs when a c_min > 0 battery exists
	// (startRow is nonzero), under the cost bound and, when the preference stage
	// retained, the preference bound. The objective is the integral count of charge
	// starts, so gap 0 asks for proven optimality and nothing is normalized away.
	if hasNonzero(mo.startRow) {
		dsol, err := mo.stage(mo.startCoeffs(), 0, deadline, s)
		if err != nil {
			return Result{}, err
		}
		// Retain only a schedule with strictly fewer sessions than the incumbent.
		// The earlier stages leave start[i][t] free, so its value there is not the
		// real transition count; count from the charging power instead.
		if dsol != nil && dsol.HasSolution() && mo.countStarts(dsol) < mo.countStarts(sol) {
			sol = dsol
			status = decidingStatus(status, dsol)
		}
	}

	// Final safety pass (spec 6.1): never report a schedule that breaks the model.
	return mo.finalizeStatus(sol, status), nil
}

// hasNonzero reports whether a sparse objective row carries at least one nonzero
// coefficient. buildModel appends zero-valued terms (for example the c_priority
// terms when c_priority == 0), so a non-empty row does not by itself mean a
// stage applies.
func hasNonzero(row []coef) bool {
	for _, c := range row {
		if c.val != 0 {
			return true
		}
	}
	return false
}

// startCoeffs returns the T4 continuity objective as a dense coefficient vector of
// length NumVars: minimize the number of charge starts. startRow holds -1 on every
// start column (the model is a maximize model), so maximizing it minimizes the
// count. The coefficients stay integral; no normalization is applied.
func (mo *model) startCoeffs() []float64 {
	costs := make([]float64, mo.m.NumVars())
	for _, c := range mo.startRow {
		costs[c.col] += c.val
	}
	return costs
}

// countStarts counts charge sessions in a solution across the c_min > 0 batteries:
// each step whose charging power rises from zero. Step 0 is not a start when the
// device is active (c_active means it enters the horizon already charging, spec 7).
// The earlier stages leave start[i][t] free, so the real transition count is read
// from the charging power rather than from that variable.
func (mo *model) countStarts(sol *highs.Solution) int {
	n := 0
	for i, b := range mo.prob.Batteries {
		if mo.zC[i] == nil {
			continue
		}
		prev := 0.0
		for t := 0; t < mo.T; t++ {
			v := sol.Value(mo.c[i][t])
			if v > 1e-6 && prev <= 1e-6 && !(t == 0 && b.CActive) {
				n++
			}
			prev = v
		}
	}
	return n
}

// decidingStatus returns the model status that decides the reported answer. Spec
// 6.1 keys Optimal on the last retained stage: a later stage that retains its
// solution supersedes the previous deciding status (retained != nil), while a
// stage that does not retain anything leaves it standing. Tasks 9-10 route their
// stages through this same rule.
func decidingStatus(prev highs.ModelStatus, retained *highs.Solution) highs.ModelStatus {
	if retained != nil {
		return retained.Status
	}
	return prev
}

// prefCoeffs returns the T3 preference objective as a dense coefficient vector of
// length NumVars, normalized to unit magnitude (spec 6.2) so the stage's gap
// tolerance is meaningful. A zero row is returned unchanged.
func (mo *model) prefCoeffs() []float64 {
	costs := make([]float64, mo.m.NumVars())
	for _, c := range mo.prefRow {
		costs[c.col] += c.val
	}
	peak := 0.0
	for _, v := range costs {
		if a := math.Abs(v); a > peak {
			peak = a
		}
	}
	if peak == 0 {
		return costs
	}
	for i := range costs {
		costs[i] /= peak
	}
	return costs
}

// finalize applies the final status decision to the chosen solution: the safety
// pass downgrades a schedule that breaks the model to Not Solved, otherwise the
// solution's HiGHS status is mapped.
func (mo *model) finalize(sol *highs.Solution) Result {
	return mo.finalizeStatus(sol, sol.Status)
}

// finalizeStatus is finalize with an explicit deciding status. The cascade passes
// the last retained stage's status (spec 6.1), which is not necessarily the
// retained solution's own status when a later stage declined to retain.
func (mo *model) finalizeStatus(sol *highs.Solution, st highs.ModelStatus) Result {
	if !mo.safetyCheck(sol) {
		return mo.assemble(nil, "Not Solved")
	}
	return mo.assemble(sol, mapStatus(st, true))
}

// stage sets one stage's objective and solves it within the shared deadline.
// absGap is the absolute MIP gap in objective units (currency for cost); a zero
// gap asks for proven optimality and no relative early stop is allowed.
func (mo *model) stage(costs []float64, absGap float64, deadline time.Time, s settings.Settings) (*highs.Solution, error) {
	mo.m.Maximize = true
	mo.m.ColCosts = costs

	opts := []highs.SolveOption{
		highs.WithOutput(false),
		highs.WithPresolve("on"),
		// The gap is absolute and in currency, so disable the relative stop;
		// otherwise HiGHS's default mip_rel_gap could end the stage early.
		highs.WithMIPRelGap(0),
		highs.WithMIPAbsGap(absGap),
	}
	if s.NumThreads != nil {
		opts = append(opts, highs.WithThreads(*s.NumThreads))
	}
	if !deadline.IsZero() {
		// The per-stage slice is all the remaining wall clock; a request without
		// a time limit stays uncapped.
		remaining := time.Until(deadline).Seconds()
		if remaining < 0 {
			remaining = 0
		}
		opts = append(opts, highs.WithTimeLimit(remaining))
	}
	return mo.m.Solve(opts...)
}

// costGap is the absolute MIP gap for the cost stage, in currency (spec 6):
//
//	gap = max(GapAbs, GapShare * need * mean(p_N))
//
// where need is the charge required to reach every battery's s_goal, in Wh at
// the charge efficiency. A nil GapAbs asks for proven optimality (gap 0).
func costGap(p Problem, s settings.Settings) float64 {
	if s.GapAbs == nil {
		return 0
	}
	need := 0.0
	for _, b := range p.Batteries {
		if len(b.SGoal) == 0 {
			continue
		}
		goal := maxFloat(b.SGoal)
		if d := goal - b.SInitial; d > 0 {
			need += d / p.EtaC
		}
	}
	meanPN := 0.0
	if n := len(p.Series.PN); n > 0 {
		sum := 0.0
		for _, v := range p.Series.PN {
			sum += v
		}
		meanPN = sum / float64(n)
	}
	if g := s.GapShare * need * meanPN; g > *s.GapAbs {
		return g
	}
	return *s.GapAbs
}

// objective evaluates a sparse objective row against a solution.
func objective(row []coef, sol *highs.Solution) float64 {
	v := 0.0
	for _, c := range row {
		v += c.val * sol.Value(c.col)
	}
	return v
}

// pinLower appends the bound row sum(row*x) >= value-pinEps, freezing a
// maximized stage's achieved value for the stages that follow. Repeated columns
// are merged so the gohighs duplicate-merge (last wins) cannot drop a term.
func (mo *model) pinLower(row []coef, value float64) {
	var r coefRow
	for _, c := range row {
		r.add(c.col, c.val)
	}
	mo.m.AddSparseRow(value-pinEps, r.cols, r.vals, highs.Inf())
}

// mapStatus maps a HiGHS model status to the response status enum (spec 6.1).
func mapStatus(st highs.ModelStatus, hasSolution bool) string {
	switch st {
	case highs.ModelStatusOptimal:
		if hasSolution {
			return "Optimal"
		}
		return "Not Solved"
	case highs.ModelStatusTimeLimit, highs.ModelStatusIterationLimit,
		highs.ModelStatusObjectiveBound, highs.ModelStatusObjectiveTarget:
		if hasSolution {
			return "Feasible"
		}
		return "Not Solved"
	case highs.ModelStatusInfeasible, highs.ModelStatusUnboundedOrInfeasible:
		return "Infeasible"
	case highs.ModelStatusUnbounded:
		return "Unbounded"
	default:
		return "Not Solved"
	}
}

// safetyCheck re-verifies the chosen solution against the model before it is
// reported (spec 6.1): energy balance, variable bounds (which carry the SOC
// bounds [0, s_capacity]) and integrality, each within safetyTol. A breach
// downgrades the result to Not Solved rather than returning a plausible but
// wrong schedule.
func (mo *model) safetyCheck(sol *highs.Solution) bool {
	nv := mo.m.NumVars()
	if sol == nil || len(sol.ColValues) < nv {
		return false
	}

	for j, vt := range mo.m.VarTypes {
		if vt != highs.Integer {
			continue
		}
		if math.Abs(sol.ColValues[j]-math.Round(sol.ColValues[j])) > safetyTol {
			return false
		}
	}

	for j := 0; j < nv; j++ {
		lo, hi := highs.NegInf(), highs.Inf()
		if j < len(mo.m.ColLower) {
			lo = mo.m.ColLower[j]
		}
		if j < len(mo.m.ColUpper) {
			hi = mo.m.ColUpper[j]
		}
		if v := sol.ColValues[j]; v < lo-safetyTol || v > hi+safetyTol {
			return false
		}
	}

	for t := 0; t < mo.T; t++ {
		bal := mo.prob.Series.FT[t] - mo.prob.Series.GT[t]
		for i := range mo.prob.Batteries {
			bal += sol.Value(mo.d[i][t]) - sol.Value(mo.c[i][t])
		}
		imp := sol.Value(mo.n[t])
		if mo.excImp != nil {
			imp += sol.Value(mo.excImp[t])
		}
		exp := sol.Value(mo.e[t])
		if mo.excExp != nil {
			exp += sol.Value(mo.excExp[t])
		}
		if math.Abs(bal+imp-exp) > safetyTol {
			return false
		}
	}
	return true
}
