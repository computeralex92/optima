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
// single wall clock. Stage A (cost) always runs; the peak, preference and
// continuity stages are no-ops until Tasks 8-10 fill their coefficient rows
// (R4), so only the cost stage runs here.
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
	if sol == nil || !sol.HasSolution() {
		st := highs.ModelStatusNotSet
		if sol != nil {
			st = sol.Status
		}
		return mo.assemble(nil, mapStatus(st, false)), nil
	}

	// Pin the achieved cost so later stages cannot trade it away.
	mo.pinLower(mo.costRow, objective(mo.costRow, sol))

	// Stages B (peak), C (preference) and D (continuity) run here once Tasks
	// 8-10 populate their rows; until then the cascade is cost-only.

	// Final safety pass (spec 6.1): never report a schedule that breaks the model.
	if !mo.safetyCheck(sol) {
		return mo.assemble(nil, "Not Solved"), nil
	}
	return mo.assemble(sol, mapStatus(sol.Status, true)), nil
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
		return "Optimal"
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
