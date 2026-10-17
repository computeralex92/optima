package explain

import (
	"strings"
	"testing"

	"github.com/computeralex92/optima/internal/optimizer"
)

func f64(v float64) *float64 { return &v }
func boolp(v bool) *bool     { return &v }

func testInput() Input {
	return Input{
		Req: optimizer.Input{
			Batteries: []optimizer.Battery{{
				ChargeFromGrid: boolp(true),
				SMin:           f64(0), SMax: f64(10000), SInitial: f64(5000),
				CMin: f64(0), CMax: f64(5000), DMax: f64(5000), PA: f64(0.0002),
			}},
			Series: optimizer.Series{
				DT: []int{900, 900, 900, 900, 3600},
				GT: []float64{250, 250, 250, 250, 1000},
				FT: []float64{0, 0, 0, 0, 2000},
				PN: []float64{0.0003, 0.0003, 0.0001, 0.0001, 0.0002},
				PE: []float64{0.0001, 0.0001, 0.0001, 0.0001, 0.0001},
			},
		},
		Res: optimizer.Result{
			Status: "Optimal",
			Batteries: []optimizer.BatteryResult{{
				ChargingPower:    []float64{0, 0, 1250, 1250, 0},
				DischargingPower: []float64{250, 250, 0, 0, 0},
				StateOfCharge:    []float64{4750, 4500, 5750, 7000, 7000},
			}},
			GridImport: []float64{0, 0, 1500, 1500, 0},
			GridExport: []float64{0, 0, 0, 0, 1000},
		},
		Details: Details{
			Timestamp: []string{
				"2026-10-15T10:00:00+02:00", "2026-10-15T10:15:00+02:00",
				"2026-10-15T10:30:00+02:00", "2026-10-15T10:45:00+02:00",
				"2026-10-15T11:00:00+02:00",
			},
			BatteryDetails: []BatteryDetail{{Name: "bat", Title: "Home battery", Type: "battery", Capacity: 10}},
		},
		Currency: "EUR",
		Language: "de",
	}
}

func TestDigestRendersPlan(t *testing.T) {
	text := Digest(testInput())
	want := []string{
		"Now: today 10:00. The plan runs until today 12:00. Status Optimal. Preference: no preference.",
		"Prices in ct/kWh, energies in kWh. Charging and discharging efficiency 0.95 and 0.95.",
		`home battery "Home battery": capacity 10.0 kWh, now 50% (5.0 kWh), kept between 0% and 100%, charges up to 5.0 kW, discharges up to 5.0 kW, may charge from the grid, does not discharge into the grid`,
		"today 10:30 | 15 | 10.0 | 10.0 | 0.0 | 0.2 | 1.5/0.0 | 1.2/0.0/57",
		"today 11:00 | 60 | 20.0 | 10.0 | 2.0 | 1.0 | 0.0/1.0 | 0.0/0.0/70",
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("digest missing %q", w)
		}
	}
}

func TestDigestRendersDischargePreference(t *testing.T) {
	in := testInput()
	in.Req.Strategy = optimizer.Strategy{
		ChargingStrategy:    "charge_before_export",
		DischargingStrategy: "discharge_before_import",
	}
	text := Digest(in)
	if want := "Preference: fill battery first, discharge before grid import."; !strings.Contains(text, want) {
		t.Fatalf("digest missing %q: %s", want, text)
	}
}

func TestStepLabelsWithoutTimestamps(t *testing.T) {
	got := stepLabels([]int{900, 900, 900, 900, 3600}, nil)
	want := []string{"+0:00", "+0:15", "+0:30", "+0:45", "+1:00", "+2:00"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func TestStepLabelsWithTimestamps(t *testing.T) {
	stamps := []string{
		"2026-10-15T23:45:00.123456789+02:00",
		"2026-10-16T00:00:00+02:00",
		"2026-10-17T10:00:00+02:00",
	}
	got := stepLabels([]int{900, 900, 3600}, stamps)
	want := []string{"today 23:45", "tomorrow 00:00", "Saturday 10:00", "Saturday 11:00"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func TestStepLabelsInvalidTimestampFallsBack(t *testing.T) {
	stamps := []string{
		"not-a-time",
		"2026-10-16T00:00:00+02:00",
		"2026-10-17T10:00:00+02:00",
	}
	got := stepLabels([]int{900, 900, 3600}, stamps)
	if len(got) == 0 || got[0] != "+0:00" {
		t.Fatalf("labels = %v, want cumulative fallback", got)
	}
}

func TestStepLabelsTimestampLengthMismatch(t *testing.T) {
	stamps := []string{
		"2026-10-15T10:00:00+02:00",
		"2026-10-15T10:15:00+02:00",
	}
	got := stepLabels([]int{900, 900, 900, 900, 3600}, stamps)
	if len(got) != 6 || got[0] != "+0:00" || got[len(got)-1] != "+2:00" {
		t.Fatalf("labels = %v, want cumulative fallback", got)
	}
}

func TestDigestTimestampLengthMismatch(t *testing.T) {
	in := testInput()
	in.Details.Timestamp = in.Details.Timestamp[:2]
	text := Digest(in)
	if !strings.Contains(text, "+0:30 | 15 | 10.0 | 10.0 | 0.0 | 0.2 | 1.5/0.0 | 1.2/0.0/57") {
		t.Fatalf("digest missing fallback row: %s", text)
	}
}

func TestRangesIgnoresDemandBeyondHorizon(t *testing.T) {
	in := testInput()
	in.Req.Batteries[0].PDemand = []float64{0, 0, 0, 0, 0, 1}
	_ = Digest(in)
}

func TestDigestOmitsZeroLimitNote(t *testing.T) {
	in := testInput()
	in.Req.Grid.PmaxImp = f64(0)
	in.Req.Grid.PmaxExp = f64(0)
	text := Digest(in)
	if strings.Contains(text, "grid import limited to") || strings.Contains(text, "grid export limited to") {
		t.Fatalf("zero limits must not render a note: %s", text)
	}
}

func TestDigestGoalsDemandsAndLimits(t *testing.T) {
	in := testInput()
	b := &in.Req.Batteries[0]
	b.DMax = f64(0)
	b.CMin = f64(1400)
	b.CActive = boolp(true)
	b.SMax = f64(8000)
	b.SGoal = []float64{0, 0, 0, 8000, 0}
	b.PDemand = []float64{350, 350, 0, 0, 1000}
	in.Details.BatteryDetails[0] = BatteryDetail{Title: "Carport (i3s)", Type: "vehicle", Capacity: 10}
	in.Details.DemandDetails = []DemandDetail{{Type: "home"}, {Type: "heating", Title: "Heat pump"}}
	in.Req.Grid.PmaxImp = f64(11000)
	in.Res.LimitViolations.GridImportLimitExceeded = true
	in.Currency = "GBP"

	text := Digest(in)
	want := []string{
		`vehicle "Carport (i3s)": capacity 10.0 kWh, now 50% (5.0 kWh), kept between 0% and 80%, charges up to 5.0 kW, at least 1.4 kW or not at all, never discharges, may charge from the grid, charging right now, charging goal 80% by today 11:00, must charge today 10:00 to today 10:30, must charge today 11:00 to today 12:00`,
		"grid import limited to 11.0 kW.",
		"the plan needs more grid import than the limit allows.",
		`The household load includes heating "Heat pump".`,
		"Prices in p/kWh",
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("digest missing %q", w)
		}
	}
}

func TestDigestUnknownCurrency(t *testing.T) {
	in := testInput()
	in.Currency = "HUF"
	if !strings.Contains(Digest(in), "today 10:00 | 15 | 0.300 | 0.100") {
		t.Fatalf("digest missing HUF price row: %s", Digest(in))
	}
}

func TestDigestCapacityFallback(t *testing.T) {
	in := testInput()
	in.Details.BatteryDetails = nil
	if !strings.Contains(Digest(in), `home battery "home battery 1": capacity 10.0 kWh`) {
		t.Fatalf("digest missing fallback title: %s", Digest(in))
	}
}

func TestSystemPrompt(t *testing.T) {
	for _, want := range []string{
		"You explain an energy plan",
		"Never use dashes as punctuation",
		"A real price difference always wins",
		"import in the cheapest quarter-hours",
		"cannot discharge into the grid",
		"A minimum charge power",
	} {
		if !strings.Contains(SystemPrompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
