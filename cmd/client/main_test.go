package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alex/optimizer-v2/internal/optimizer"
)

// TestRender pins the pure formatting: one row per step per battery carrying
// charge/discharge/SOC plus the per-step grid import/export, then a footer with
// the objective and status.
func TestRender(t *testing.T) {
	obj := 42.5
	r := optimizer.Result{
		Status:         "Optimal",
		ObjectiveValue: &obj,
		Batteries: []optimizer.BatteryResult{
			{
				ChargingPower:    []float64{100, 0},
				DischargingPower: []float64{0, 50},
				StateOfCharge:    []float64{1000, 950},
			},
			{
				ChargingPower:    []float64{0, 200},
				DischargingPower: []float64{10, 0},
				StateOfCharge:    []float64{500, 700},
			},
		},
		GridImport: []float64{300, 400},
		GridExport: []float64{0, 120},
	}

	var buf bytes.Buffer
	render(&buf, r)
	lines := nonEmptyLines(buf.String())

	if len(lines) != 6 { // header + 2 steps * 2 batteries + footer
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), buf.String())
	}

	wantHeader := []string{"STEP", "BATTERY", "CHARGE", "DISCHARGE", "SOC", "GRID_IMPORT", "GRID_EXPORT"}
	if got := strings.Fields(lines[0]); !equalFields(got, wantHeader) {
		t.Fatalf("header = %v, want %v", got, wantHeader)
	}

	want := [][]string{
		{"0", "0", "100.00", "0.00", "1000.00", "300.00", "0.00"},
		{"0", "1", "0.00", "10.00", "500.00", "300.00", "0.00"},
		{"1", "0", "0.00", "50.00", "950.00", "400.00", "120.00"},
		{"1", "1", "200.00", "0.00", "700.00", "400.00", "120.00"},
	}
	for i, w := range want {
		if got := strings.Fields(lines[i+1]); !equalFields(got, w) {
			t.Fatalf("row %d = %v, want %v", i, got, w)
		}
	}

	footer := lines[5]
	if !strings.Contains(footer, "42.5000") || !strings.Contains(footer, "Optimal") {
		t.Fatalf("footer = %q, want objective and status", footer)
	}
}

// TestRenderNonSolved: a non-solved payload has a nil objective and no rows.
func TestRenderNonSolved(t *testing.T) {
	r := optimizer.NewResult()
	r.Status = "Infeasible"

	var buf bytes.Buffer
	render(&buf, r)
	lines := nonEmptyLines(buf.String())

	if len(lines) != 2 { // header + footer
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[1], "Infeasible") {
		t.Fatalf("footer = %q, want status", lines[1])
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func equalFields(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
