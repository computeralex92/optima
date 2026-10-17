package explain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/computeralex92/optima/internal/optimizer"
)

var priceSubunit = map[string]string{
	"EUR": "ct", "CHF": "ct", "GBP": "p", "USD": "¢", "CAD": "¢", "BRL": "¢", "AUD": "c", "NZD": "c", "ZAR": "c",
	"NOK": "øre", "DKK": "øre", "SEK": "öre", "PLN": "gr", "BGN": "st", "ILS": "ag", "TRY": "krş",
}

var strategyLabels = map[string]string{
	"charge_before_export":   "fill battery first",
	"attenuate_demand_peaks": "reduce grid import peaks",
	"attenuate_feedin_peaks": "reduce grid export peaks",
	"attenuate_grid_peaks":   "reduce grid peaks",
}

var demandLabels = map[string]string{
	"heating":    "heating",
	"unmodelled": "charging point without vehicle data",
}

// Digest renders the plan as the plain-text table upstream sends to the chat
// model. It is behavior, not configuration: keep the output stable.
func Digest(in Input) string {
	req := in.Req
	res := in.Res
	details := in.Details
	unit, price := priceFormat(in.Currency)
	ts := req.Series
	labels := stepLabels(ts.DT, details.Timestamp)

	titles := make([]string, 0, len(req.Batteries))
	devices := make([]string, 0, len(req.Batteries))
	capacities := make([]float64, 0, len(req.Batteries))
	for i, b := range req.Batteries {
		detail := batteryDetailAt(details.BatteryDetails, i)
		capacity := capacityWH(b, detail)
		capacities = append(capacities, capacity)
		title, line := batteryLine(b, detail, i, labels, capacity)
		titles = append(titles, title)
		devices = append(devices, line)
	}

	grid := req.Grid
	violations := res.LimitViolations
	notes := make([]string, 0, 4)
	if grid.PmaxImp != nil {
		notes = append(notes, "grid import limited to "+kwh(*grid.PmaxImp)+" kW")
	}
	if grid.PmaxExp != nil {
		notes = append(notes, "grid export limited to "+kwh(*grid.PmaxExp)+" kW")
	}
	if violations.GridImportLimitExceeded {
		notes = append(notes, "the plan needs more grid import than the limit allows")
	}
	if violations.GridExportLimitHit {
		notes = append(notes, "the plan reaches the grid export limit, surplus solar beyond it is lost")
	}

	demands := make([]string, 0, len(details.DemandDetails))
	for _, d := range details.DemandDetails {
		label, ok := demandLabels[d.Type]
		if !ok {
			continue
		}
		title := d.Title
		if title == "" {
			title = d.Type
		}
		demands = append(demands, fmt.Sprintf("%s %q", label, title))
	}

	strategy := strategyLabels[req.Strategy.ChargingStrategy]
	if strategy == "" {
		strategy = "no preference"
	}

	etaC, etaD := 0.95, 0.95
	if req.EtaC != nil {
		etaC = *req.EtaC
	}
	if req.EtaD != nil {
		etaD = *req.EtaD
	}

	lines := []string{
		fmt.Sprintf("Now: %s. The plan runs until %s. Status %s. Preference: %s.", labels[0], labels[len(labels)-1], res.Status, strategy),
		fmt.Sprintf("Prices in %s/kWh, energies in kWh. Charging and discharging efficiency %s and %s.", unit, pyFloat(etaC), pyFloat(etaD)),
	}
	for _, note := range notes {
		lines = append(lines, note+".")
	}
	if len(demands) > 0 {
		lines = append(lines, fmt.Sprintf("The household load includes %s.", strings.Join(demands, ", ")))
	}
	lines = append(lines, "Vehicles and home batteries:")
	lines = append(lines, devices...)
	lines = append(lines, "Plan per time slot, the vehicle and battery columns are charged/discharged/charge level in % at the end of the slot:")

	header := []string{"time", "minutes", "import price", "export price", "solar", "load", "grid import/export"}
	header = append(header, titles...)
	lines = append(lines, strings.Join(header, " | "))

	for i := range ts.DT {
		row := []string{
			labels[i],
			strconv.Itoa(ts.DT[i] / 60),
			price(at(ts.PN, i)),
			price(at(ts.PE, i)),
			kwh(at(ts.FT, i)),
			kwh(at(ts.GT, i)),
			fmt.Sprintf("%s/%s", kwh(at(res.GridImport, i)), kwh(at(res.GridExport, i))),
		}
		for j := 0; j < len(res.Batteries) && j < len(capacities); j++ {
			battery := res.Batteries[j]
			row = append(row, fmt.Sprintf("%s/%s/%.0f",
				kwh(at(battery.ChargingPower, i)),
				kwh(at(battery.DischargingPower, i)),
				math.Round(at(battery.StateOfCharge, i)/capacities[j]*100)))
		}
		lines = append(lines, strings.Join(row, " | "))
	}
	return strings.Join(lines, "\n")
}

func stepLabels(dt []int, timestamps []string) []string {
	if len(timestamps) == len(dt) && len(dt) > 0 {
		times := make([]time.Time, 0, len(timestamps)+1)
		ok := true
		for _, stamp := range timestamps {
			t, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				ok = false
				break
			}
			times = append(times, t)
		}
		if ok {
			last := times[len(times)-1].Add(time.Duration(dt[len(dt)-1]) * time.Second)
			times = append(times, last)
			first := times[0]
			labels := make([]string, len(times))
			for i, t := range times {
				labels[i] = dayLabel(t, first) + " " + t.Format("15:04")
			}
			return labels
		}
	}

	labels := make([]string, 0, len(dt)+1)
	elapsed := 0
	for i := 0; i <= len(dt); i++ {
		labels = append(labels, fmt.Sprintf("+%d:%02d", elapsed/3600, elapsed%3600/60))
		if i < len(dt) {
			elapsed += dt[i]
		}
	}
	return labels
}

func dayLabel(t, first time.Time) string {
	switch t.Format("2006-01-02") {
	case first.Format("2006-01-02"):
		return "today"
	case first.AddDate(0, 0, 1).Format("2006-01-02"):
		return "tomorrow"
	default:
		return t.Weekday().String()
	}
}

func kwh(wh float64) string {
	return fmt.Sprintf("%.1f", wh/1000)
}

func priceFormat(currency string) (string, func(float64) string) {
	if currency == "" {
		currency = "EUR"
	}
	if unit, ok := priceSubunit[currency]; ok {
		return unit, func(perWh float64) string { return fmt.Sprintf("%.1f", perWh*1e5) }
	}
	return currency, func(perWh float64) string { return fmt.Sprintf("%.3f", perWh*1e3) }
}

func pyFloat(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

func ranges(series []float64, labels []string) []string {
	out := make([]string, 0)
	start := -1
	for i := 0; i <= len(series); i++ {
		v := 0.0
		if i < len(series) {
			v = series[i]
		}
		if v > 0 && start == -1 {
			start = i
		} else if v <= 0 && start != -1 {
			out = append(out, fmt.Sprintf("%s to %s", labels[start], labels[i]))
			start = -1
		}
	}
	return out
}

func capacityWH(b optimizer.Battery, detail BatteryDetail) float64 {
	if b.SCapacity != nil && *b.SCapacity != 0 {
		return *b.SCapacity
	}
	if detail.Capacity != 0 {
		return detail.Capacity * 1000
	}
	if b.SMax != nil && *b.SMax != 0 {
		return *b.SMax
	}
	return 1
}

func batteryLine(b optimizer.Battery, detail BatteryDetail, i int, labels []string, capacity float64) (string, string) {
	kind := "home battery"
	if detail.Type == "vehicle" {
		kind = "vehicle"
	}
	title := detail.Title
	if title == "" {
		title = detail.Name
	}
	if title == "" {
		title = fmt.Sprintf("%s %d", kind, i+1)
	}

	pct := func(wh float64) string {
		return fmt.Sprintf("%.0f%%", math.Round(wh/capacity*100))
	}
	sInitial := floatOr(b.SInitial, 0)
	facts := []string{
		fmt.Sprintf("capacity %.1f kWh", capacity/1000),
		fmt.Sprintf("now %s (%.1f kWh)", pct(sInitial), sInitial/1000),
		fmt.Sprintf("kept between %s and %s", pct(floatOr(b.SMin, 0)), pct(floatOr(b.SMax, 0))),
	}
	charge := fmt.Sprintf("charges up to %.1f kW", floatOr(b.CMax, 0)/1000)
	if floatOr(b.CMin, 0) > 0 {
		charge += fmt.Sprintf(", at least %.1f kW or not at all", floatOr(b.CMin, 0)/1000)
	}
	facts = append(facts, charge)
	if floatOr(b.DMax, 0) > 0 {
		facts = append(facts, fmt.Sprintf("discharges up to %.1f kW", floatOr(b.DMax, 0)/1000))
	} else {
		facts = append(facts, "never discharges")
	}
	if boolOr(b.ChargeFromGrid, false) {
		facts = append(facts, "may charge from the grid")
	} else {
		facts = append(facts, "no grid charging, only surplus solar")
	}
	if floatOr(b.DMax, 0) > 0 {
		if boolOr(b.DischargeToGrid, false) {
			facts = append(facts, "may discharge into the grid")
		} else {
			facts = append(facts, "does not discharge into the grid")
		}
	}
	if boolOr(b.CActive, false) {
		facts = append(facts, "charging right now")
	}
	for t, goal := range b.SGoal {
		if goal > 0 && t+1 < len(labels) {
			facts = append(facts, fmt.Sprintf("charging goal %s by %s", pct(goal), labels[t+1]))
		}
	}
	for _, run := range ranges(b.PDemand, labels) {
		facts = append(facts, "must charge "+run)
	}
	return title, fmt.Sprintf("%s %q: %s", kind, title, strings.Join(facts, ", "))
}

func batteryDetailAt(details []BatteryDetail, i int) BatteryDetail {
	if i >= 0 && i < len(details) {
		return details[i]
	}
	return BatteryDetail{}
}

func at(series []float64, i int) float64 {
	if i >= 0 && i < len(series) {
		return series[i]
	}
	return 0
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
