package optimizer

import "github.com/bartolsthoorn/gohighs/highs"

// dense expands a sparse coefficient row into a dense vector of length NumVars,
// summing repeated columns. All stage objectives are built this way.
func (mo *model) dense(row []coef) []float64 {
	costs := make([]float64, mo.m.NumVars())
	for _, c := range row {
		costs[c.col] += c.val
	}
	return costs
}

// costCoeffs returns the T1 cost objective as a dense coefficient vector of length
// NumVars (spec 5.3). The sparse costRow built by buildModel is the single source
// (ruling R1); this expands it, summing repeated columns.
func (mo *model) costCoeffs() []float64 {
	return mo.dense(mo.costRow)
}

// cleanValue recomputes the reported objective from a solution (spec 5.3): grid
// import cost (including excImp when an import limit is set) + export revenue +
// (s[i][T-1] - s_initial)*p_a + the demand-rate charge. No penalties, no
// preferences.
func (mo *model) cleanValue(sol *highs.Solution) float64 {
	v := 0.0
	for t := 0; t < mo.T; t++ {
		if mo.excImp != nil {
			// import limit set: energy cost covers the regular import and the
			// beyond-limit portion alike.
			v -= (sol.Value(mo.n[t]) + sol.Value(mo.excImp[t])) * mo.prob.Series.PN[t]
		} else {
			v -= sol.Value(mo.n[t]) * mo.prob.Series.PN[t]
		}
		v += sol.Value(mo.e[t]) * mo.prob.Series.PE[t]
	}
	if mo.T > 0 {
		for i, b := range mo.prob.Batteries {
			// reference is s_initial, not s[0]: s[0] already carries step 0's charge.
			v += (sol.Value(mo.s[i][mo.T-1]) - b.SInitial) * b.PA
		}
	}
	if mo.pMaxImpExc >= 0 {
		v -= *mo.prob.Grid.PrcPExcImp * sol.Value(mo.pMaxImpExc)
	}
	return v
}
