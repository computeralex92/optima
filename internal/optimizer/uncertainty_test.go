package optimizer

import (
	"math"
	"testing"

	"github.com/computeralex92/optima/internal/settings"
)

// uncertaintyInput is the upstream hedge fixture: a day in six hourly steps,
// two cheap night hours, two solar hours and two expensive evening hours. The
// forecast solar just covers the evening, so a shortfall shows up as evening
// import at the high price.
func uncertaintyInput(u *Uncertainty, chargeFromGrid bool) Input {
	dischargeToGrid := false
	return Input{
		Strategy: Strategy{ChargingStrategy: "charge_before_export", DischargingStrategy: "none"},
		Batteries: []Battery{{
			ChargeFromGrid:  &chargeFromGrid,
			DischargeToGrid: &dischargeToGrid,
			SCapacity:       f64(10000),
			SMin:            f64(0),
			SMax:            f64(10000),
			SInitial:        f64(2000),
			CMin:            f64(0),
			CMax:            f64(5000),
			DMax:            f64(5000),
			PA:              f64(1e-4),
		}},
		Series: Series{
			DT: []int{3600, 3600, 3600, 3600, 3600, 3600},
			GT: []float64{1000, 1000, 1000, 1000, 2500, 2500},
			FT: []float64{0, 0, 3000, 3000, 0, 0},
			PN: []float64{1e-4, 1e-4, 2e-4, 2e-4, 4e-4, 4e-4},
			PE: []float64{0.5e-4, 0.5e-4, 0.5e-4, 0.5e-4, 0.5e-4, 0.5e-4},
		},
		Uncertainty: u,
	}
}

func solveUncertainty(t *testing.T, in Input) Result {
	t.Helper()
	res, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "Optimal" && res.Status != "Feasible" {
		t.Fatalf("status %s", res.Status)
	}
	return res
}

func nightImport(res Result) float64 {
	return res.GridImport[0] + res.GridImport[1]
}

// TestHedgeChargesBeforeAnUncertainDay is the upstream behavior: with the
// forecast covering the evening, a hedged plan imports more at night to hold a
// reserve, and the reported clean objective is worse for it.
func TestHedgeChargesBeforeAnUncertainDay(t *testing.T) {
	nominal := solveUncertainty(t, uncertaintyInput(nil, true))
	if got := nightImport(nominal); got < 1999 || got > 2001 {
		t.Fatalf("nominal night import %v, want 2000", got)
	}

	hedged := solveUncertainty(t, uncertaintyInput(&Uncertainty{FtDev: 0.5, Budget: 2}, true))
	if got := nightImport(hedged); got <= 3000 {
		t.Fatalf("hedged night import %v, want > 3000", got)
	}
	if *hedged.ObjectiveValue >= *nominal.ObjectiveValue {
		t.Fatalf("hedged objective %v not below nominal %v", *hedged.ObjectiveValue, *nominal.ObjectiveValue)
	}
}

// TestHedgeIsCappedByItsBudget: a larger deviation budget buys more reserve.
func TestHedgeIsCappedByItsBudget(t *testing.T) {
	small := solveUncertainty(t, uncertaintyInput(&Uncertainty{FtDev: 0.5, Budget: 1}, true))
	large := solveUncertainty(t, uncertaintyInput(&Uncertainty{FtDev: 0.5, Budget: 2}, true))
	if nightImport(small) >= nightImport(large) {
		t.Fatalf("budget 1 night import %v not below budget 2 %v", nightImport(small), nightImport(large))
	}
}

// TestHedgeLeavesTheModelAloneWithoutAnythingToHedge: a zero budget or a
// forecast the step spills anyway adds neither variables nor rows.
func TestHedgeLeavesTheModelAloneWithoutAnythingToHedge(t *testing.T) {
	in := uncertaintyInput(nil, true)
	plain, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	base := buildModel(plain)

	cases := []struct {
		name string
		in   Input
	}{
		{"zero budget", uncertaintyInput(&Uncertainty{FtDev: 0.5, Budget: 0}, true)},
		{"no deviation", uncertaintyInput(&Uncertainty{FtDev: 0.5, Budget: 2}, true)},
	}
	// no solar to fall short and no demand deviation: nothing to hedge
	cases[1].in.Series.FT = []float64{0, 0, 0, 0, 0, 0}

	for _, tc := range cases {
		p, err := tc.in.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		mo := buildModel(p)
		if mo.m.NumVars() != base.m.NumVars() {
			t.Fatalf("%s: %d variables, want %d", tc.name, mo.m.NumVars(), base.m.NumVars())
		}
		if mo.m.NumConstraints() != base.m.NumConstraints() {
			t.Fatalf("%s: %d rows, want %d", tc.name, mo.m.NumConstraints(), base.m.NumConstraints())
		}
	}
}

// daysInput is the upstream multi-day fixture: three equal days in hourly
// steps, six cheap night hours, solar around noon and the rest at the high
// price.
func daysInput(u *Uncertainty) Input {
	ftCycle := []float64{0, 0, 0, 0, 0, 0, 0, 0, 1500, 3000, 4000, 4000, 3000, 1500, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	pnCycle := []float64{2e-4, 2e-4, 2e-4, 2e-4, 2e-4, 2e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4, 3e-4}
	chargeFromGrid, dischargeToGrid := true, false
	in := Input{
		Strategy: Strategy{ChargingStrategy: "charge_before_export", DischargingStrategy: "none"},
		Batteries: []Battery{{
			ChargeFromGrid:  &chargeFromGrid,
			DischargeToGrid: &dischargeToGrid,
			SCapacity:       f64(10000),
			SMin:            f64(1000),
			SMax:            f64(10000),
			SInitial:        f64(5000),
			CMin:            f64(0),
			CMax:            f64(4000),
			DMax:            f64(5000),
			PA:              f64(1.8e-4),
		}},
		Series:      Series{PN: []float64{}, PE: []float64{}},
		EtaC:        f64(0.9),
		EtaD:        f64(0.9),
		Uncertainty: u,
	}
	for day := 0; day < 3; day++ {
		in.Series.DT = append(in.Series.DT, make([]int, 24)...)
		for i := 0; i < 24; i++ {
			in.Series.DT[day*24+i] = 3600
			in.Series.GT = append(in.Series.GT, 500)
			in.Series.FT = append(in.Series.FT, ftCycle[i])
			in.Series.PN = append(in.Series.PN, pnCycle[i])
			in.Series.PE = append(in.Series.PE, 0.8e-4)
		}
	}
	return in
}

// TestHedgeDoesNotDischargeIntoCheapNightToChargeItBack locks u_chg: booking
// the worst case import at the night price must not need a discharge the same
// night undoes.
func TestHedgeDoesNotDischargeIntoCheapNightToChargeItBack(t *testing.T) {
	in := daysInput(&Uncertainty{FtDev: 0.6, GtDev: 0.1, Budget: 6})
	res, err := Solve(in, settings.Settings{})
	if err != nil || res.Status != "Optimal" {
		t.Fatalf("solve: %v %s", err, res.Status)
	}
	for _, night := range []int{0, 24, 48} {
		discharged, charged := 0.0, 0.0
		for i := 0; i < 6; i++ {
			discharged += res.Batteries[0].DischargingPower[night+i]
			charged += res.Batteries[0].ChargingPower[night+i]
		}
		if math.Min(discharged, charged) > 1 {
			t.Fatalf("night %d discharges %v and charges %v", night, discharged, charged)
		}
	}
}

// TestReserveDoesNotGrowWithTheDaysPlanned locks the one-day lookback: equal
// days hold an equal reserve.
func TestReserveDoesNotGrowWithTheDaysPlanned(t *testing.T) {
	in := daysInput(&Uncertainty{FtDev: 0.4, GtDev: 0.1, Budget: 6})
	res, err := Solve(in, settings.Settings{})
	if err != nil || res.Status != "Optimal" {
		t.Fatalf("solve: %v %s", err, res.Status)
	}
	lowest := func(from int) float64 {
		m := math.Inf(1)
		for _, v := range res.Batteries[0].StateOfCharge[from : from+24] {
			m = math.Min(m, v)
		}
		return m
	}
	if a, b := lowest(24), lowest(48); math.Abs(a-b) > 1 {
		t.Fatalf("lowest SOC day 2 %v vs day 3 %v", a, b)
	}
}

// TestHedgeSpendsReserveFirstOnFlatTariff pins the vendored case: stored energy
// is valued just below the import it replaces, which must not make holding it
// the cheaper recourse.
func TestHedgeSpendsReserveFirstOnFlatTariff(t *testing.T) {
	in := loadCase(t, "../../test_cases/029-hedge-spends-reserve-on-flat-tariff.json")
	res := solveUncertainty(t, in)
	sMin := floatOr(in.Batteries[0].SMin, 0)
	lowest := math.Inf(1)
	for _, v := range res.Batteries[0].StateOfCharge[:12] {
		lowest = math.Min(lowest, v)
	}
	if math.Abs(lowest-sMin) > 1 {
		t.Fatalf("lowest SOC %v, want s_min %v", lowest, sMin)
	}
}

// TestStoredCaseSolvesWithABudget checks the hedge on a real vendored request.
func TestStoredCaseSolvesWithABudget(t *testing.T) {
	in := loadCase(t, "../../test_cases/012-early-charging-not-perfect.json")
	in.Uncertainty = &Uncertainty{FtDev: 0.3, GtDev: 0.2, Budget: 8}
	solveUncertainty(t, in)
}

// TestFlatTariffBuysNoReserveFromGrid: a shortfall costs the same whenever it is
// imported, so a reserve bought from the grid cannot beat importing it when it
// happens.
func TestFlatTariffBuysNoReserveFromGrid(t *testing.T) {
	in := uncertaintyInput(&Uncertainty{FtDev: 0.3, GtDev: 0.2, Budget: 2}, true)
	for i := range in.Series.PN {
		in.Series.PN[i] = 3e-4
	}
	res := solveUncertainty(t, in)
	for i, imp := range res.GridImport {
		if imp > in.Series.GT[i]+1 {
			t.Fatalf("step %d imports %v above its demand %v", i, imp, in.Series.GT[i])
		}
	}
}
