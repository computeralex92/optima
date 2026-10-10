package optimizer

import "github.com/bartolsthoorn/gohighs/highs"

// assemble builds the OptimizationResult payload from a solution (spec section 3).
// status is mapped by Task 7. On a non-solved status the payload carries empty
// (non-nil) arrays and a nil objective; on Optimal/Feasible every field is filled
// and the objective is the clean value (spec 5.3).
func (mo *model) assemble(sol *highs.Solution, status string) Result {
	res := NewResult()
	res.Status = status
	if status != "Optimal" && status != "Feasible" {
		return res
	}

	v := mo.cleanValue(sol)
	res.ObjectiveValue = &v

	// A demand rate makes excImp the beyond-threshold import, so it is part of the
	// real grid import; a bare import limit keeps excImp out of grid_import and
	// reports it only as overshoot.
	demandActive := mo.pMaxImpExc >= 0
	for t := 0; t < mo.T; t++ {
		imp := sol.Value(mo.n[t])
		if demandActive {
			imp += sol.Value(mo.excImp[t])
		}
		res.GridImport = append(res.GridImport, imp)
		res.GridExport = append(res.GridExport, sol.Value(mo.e[t]))
		res.FlowDirection = append(res.FlowDirection, int(sol.Value(mo.y[t])))
	}

	if mo.excImp != nil {
		for t := 0; t < mo.T; t++ {
			x := sol.Value(mo.excImp[t])
			res.GridImportOvershoot = append(res.GridImportOvershoot, x)
			if x > 0 {
				res.LimitViolations.GridImportLimitExceeded = true
			}
		}
	}
	if mo.excExp != nil {
		for t := 0; t < mo.T; t++ {
			x := sol.Value(mo.excExp[t])
			res.GridExportOvershoot = append(res.GridExportOvershoot, x)
			if x > 0 {
				res.LimitViolations.GridExportLimitHit = true
			}
		}
	}

	for i := range mo.prob.Batteries {
		res.Batteries = append(res.Batteries, BatteryResult{
			ChargingPower:    colValues(sol, mo.c[i]),
			DischargingPower: colValues(sol, mo.d[i]),
			StateOfCharge:    colValues(sol, mo.s[i]),
		})
	}
	return res
}

// colValues reads one battery's per-step column values.
func colValues(sol *highs.Solution, cols []int) []float64 {
	out := make([]float64, len(cols))
	for i, c := range cols {
		out[i] = sol.Value(c)
	}
	return out
}
