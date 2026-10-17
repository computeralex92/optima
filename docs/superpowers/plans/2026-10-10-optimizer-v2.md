# optima Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go, API-compatible, HiGHS-backed replacement for the evcc charging optimizer that matches or beats the Python service on every golden case.

**Architecture:** A thin `net/http` service (`internal/api`) over a solver core (`internal/optimizer`) that builds one MILP with the HiGHS high-level `Model` and solves it as an ordered lexicographic cascade (cost, peak, preference, continuity), each stage bounded by the earlier stages' values. Shared request/response types live in `internal/optimizer` with `snake_case` JSON tags.

**Tech Stack:** Go 1.24+, `github.com/bartolsthoorn/gohighs` (cgo, embedded static HiGHS), `net/http`, standard `testing`.

**Spec:** `docs/superpowers/specs/2026-10-10-optimizer-v2-design.md` (this plan argues from it; read both).

## Global Constraints

- Module path `github.com/computeralex92/optima`.
- cgo is required (gohighs links static HiGHS). `CGO_ENABLED=1`.
- No new third-party dependencies beyond gohighs.
- All JSON uses `snake_case` keys, exactly as `/optimize/charge-schedule` in the spec section 3.
- Response slices serialize as `[]`, never `null`; `objective_value` is nullable.
- Follow the spec's units: energy Wh, power W, `dt` seconds, `h[t] = dt[t]/3600`.
- No em dashes in code, comments, commits or docs.
- Commit subject style: Conventional Commits (`feat:`, `fix:`, `test:`, `chore:`).
- Four solver stages, in order: `cost`, `peak`, `preference`, `continuity`. Each skipped when it does not apply.
- Warm starts are NOT available (gohighs exposes no `setSolution`). Do not attempt them.

## Review Focus

Input classes the spec implies but no task's first tests exercise. Each is pinned by a test added to the owning task, in that task's own step style.

1. Degenerate horizons, `T == 0` or `T == 1` (spec silent): must not panic and must return a well-formed result.
2. `s_initial` above `s_capacity` or outside `[s_min, s_max]` (spec 5.2.5 soft bounds): must force discharge/charge and stay feasible, not error.
3. `p_max_imp == 0` or `p_max_exp == 0` versus the limit being absent: absent must leave the overshoot series `[]`, present-and-zero must gate correctly.
4. Very long horizons (case 028, case 021) and numerical drift: balance rows must hold within tolerance after solving.
5. `p_demand` / `s_goal` length mismatches and an all-zero `s_goal`: rejected with a 400 and a field-path detail, not a panic or a silently ignored goal.

---

### Task 1: Module bootstrap and HiGHS integration smoke test

**Files:**
- Create: `go.mod`, `go.sum`
- Create: `internal/optimizer/highs_smoke_test.go`
- Create: `Makefile`
- Create: `openapi.yaml`, `test_cases/*.json` (vendored)
- Create: `.gitignore`

**Interfaces:**
- Produces: a compiling module at the chosen path, with gohighs importable as `github.com/bartolsthoorn/gohighs/highs`.

- [ ] **Step 1: Initialise the module and dependency**

```bash
cd /path/to/optima
go mod init github.com/computeralex92/optima
go get github.com/bartolsthoorn/gohighs/highs
```

- [ ] **Step 2: Vendor the contract and golden cases**

```bash
tmp=$(mktemp -d)
git clone --depth 1 https://github.com/evcc-io/optimizer "$tmp"
cp "$tmp/openapi.yaml" .
cp -r "$tmp/test_cases" .
rm -rf "$tmp"
```

- [ ] **Step 3: Write the failing smoke test**

```go
// internal/optimizer/highs_smoke_test.go
package optimizer

import (
	"testing"

	"github.com/bartolsthoorn/gohighs/highs"
)

func TestHighsSolvesTrivialMILP(t *testing.T) {
	m := highs.Model{
		Maximize: true,
		ColCosts: []float64{1, 1},
		ColLower: []float64{0, 0},
		ColUpper: []float64{10, 10},
		VarTypes: []highs.VariableType{highs.Integer, highs.Integer},
	}
	m.AddLeRow([]float64{1, 1}, 3)
	sol, err := m.Solve(highs.WithOutput(false))
	if err != nil {
		t.Fatal(err)
	}
	if !sol.IsOptimal() {
		t.Fatalf("status = %s", sol.Status)
	}
	if got := sol.ColValues[0] + sol.ColValues[1]; got != 3 {
		t.Fatalf("x+y = %v, want 3", got)
	}
}
```

- [ ] **Step 4: Run it to verify it passes (cgo toolchain present)**

Run: `go test ./internal/optimizer/ -run TestHighsSolvesTrivialMILP -v`
Expected: PASS. If this fails to link, stop and report: the cgo/HiGHS toolchain is the prerequisite for everything else.

- [ ] **Step 5: Add Makefile and .gitignore**

`Makefile` targets: `build` (`go build ./...`), `test` (`go test ./...`), `run` (`go run ./cmd/optimizer`), `lint` (`go vet ./...`). `.gitignore`: `/bin/`, `*.log`, `slow-requests.jsonl`.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "chore: bootstrap module, vendor contract and golden cases"
```

---

### Task 2: Settings package

**Files:**
- Create: `internal/settings/settings.go`
- Test: `internal/settings/settings_test.go`

**Interfaces:**
- Produces:
```go
package settings

type Settings struct {
	TimeLimit        *float64 // nil = solve out
	GapAbs           *float64 // default 0.01
	GapShare         float64  // default 0.0025
	PreferenceBudget float64  // default 0.0
	NumThreads       *int     // nil = HiGHS default
	LogSubject       bool     // default false
	DumpSlowRequests string   // "" = disabled
}

func Load() Settings // reads OPTIMIZER_* from the environment, applies defaults
```

- [ ] **Step 1: Write the failing test**

```go
func TestLoadDefaults(t *testing.T) {
	t.Setenv("OPTIMIZER_GAP_ABS", "")
	// clear the rest in the test as needed
	s := Load()
	if s.GapAbs == nil || *s.GapAbs != 0.01 {
		t.Fatalf("GapAbs = %v", s.GapAbs)
	}
	if s.GapShare != 0.0025 || s.PreferenceBudget != 0 {
		t.Fatalf("defaults wrong: %+v", s)
	}
	if s.TimeLimit != nil || s.NumThreads != nil {
		t.Fatalf("optional defaults should be nil: %+v", s)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("OPTIMIZER_TIME_LIMIT", "10")
	t.Setenv("OPTIMIZER_NUM_THREADS", "4")
	t.Setenv("OPTIMIZER_LOG_SUBJECT", "true")
	s := Load()
	if s.TimeLimit == nil || *s.TimeLimit != 10 {
		t.Fatalf("TimeLimit = %v", s.TimeLimit)
	}
	if s.NumThreads == nil || *s.NumThreads != 4 {
		t.Fatalf("NumThreads = %v", s.NumThreads)
	}
	if !s.LogSubject {
		t.Fatalf("LogSubject = false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/settings/ -v`
Expected: FAIL (undefined: Load).

- [ ] **Step 3: Implement `settings.go`**

Parse each `OPTIMIZER_*` var: unset or unparseable leaves the default; unset optional fields stay nil. `Load()` returns the struct. `OPTIMIZER_PROBE_SECONDS` is intentionally not read (spec section 8).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/settings/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: add OPTIMIZER_* settings loader"
```

---

### Task 3: Domain types and normalization

**Files:**
- Create: `internal/optimizer/types.go`
- Test: `internal/optimizer/types_test.go`

**Interfaces:**
- Produces wire types `Input`, `Strategy`, `Grid`, `Battery`, `Series`, `Result`, `BatteryResult`, `LimitViolations` (all with the spec's JSON tags; optional fields are pointers so absent is distinguishable), and the normalized concrete types below.

```go
type Problem struct {
	Strategy  Strategy
	Grid      Grid // concrete; PmaxImp/PmaxExp/PrcPExcImp stay *float64 (nil = absent)
	Batteries []Bat
	Series    Series
	EtaC, EtaD float64
}

type Bat struct {
	ChargeFromGrid, DischargeToGrid bool
	SCapacity, SMin, SMax, SInitial float64
	CMin, CMax, DMax, PA             float64
	PDemand, SGoal                   []float64 // nil when absent, length T when present
	CPriority                        int
	CActive                          bool
}

// Normalize applies spec section 3 defaults. T is inferred from len(Series.GT).
func (in *Input) Normalize() (Problem, error)
```

Normalization rules (spec 3): `EtaC=EtaD=0.95`; `ChargeFromGrid=DischargeToGrid=false`; `SCapacity=SMax` when absent; `CPriority=0`; `CActive=false`; strategies default `none`. `Normalize` returns an error only for conditions that make a `Bat` unconstructible; semantic validation belongs to Task 4.

- [ ] **Step 1: Write the failing test**

```go
func TestNormalizeDefaults(t *testing.T) {
	smax := 10000.0
	in := Input{Batteries: []Battery{{SMax: &smax}}, Series: Series{GT: []float64{1, 2}}}
	p, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if p.EtaC != 0.95 || p.EtaD != 0.95 {
		t.Fatalf("eta defaults: %+v", p)
	}
	if p.Batteries[0].SCapacity != 10000 {
		t.Fatalf("s_capacity should default to s_max, got %v", p.Batteries[0].SCapacity)
	}
	if p.Strategy.ChargingStrategy != "none" || p.Strategy.DischargingStrategy != "none" {
		t.Fatalf("strategy defaults: %+v", p.Strategy)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/optimizer/ -run TestNormalizeDefaults -v`
Expected: FAIL.

- [ ] **Step 3: Implement types and `Normalize`**

Declare the wire structs with the exact JSON tags from `openapi.yaml`. Implement `Normalize` per the rules above; `T = len(in.Series.GT)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/optimizer/ -run TestNormalizeDefaults -v`
Expected: PASS.

- [ ] **Step 5: Add the degenerate-horizon test (Review Focus 1)**

```go
func TestNormalizeDegenerateHorizon(t *testing.T) {
	smin, smax, si, cmin, cmax, dmax, pa := 0.0, 1000.0, 0.0, 0.0, 1000.0, 0.0, 0.1
	b := Battery{SMin: &smin, SMax: &smax, SInitial: &si, CMin: &cmin, CMax: &cmax, DMax: &dmax, PA: &pa}
	for _, n := range []int{0, 1} {
		in := Input{Batteries: []Battery{b}, Series: Series{DT: make([]int, n), GT: make([]float64, n), FT: make([]float64, n), PN: make([]float64, n), PE: make([]float64, n)}}
		if _, err := in.Normalize(); err != nil {
			t.Fatalf("T=%d: unexpected error %v", n, err)
		}
	}
}
```

Run: `go test ./internal/optimizer/ -run TestNormalizeDegenerateHorizon -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat: add domain types and normalization"
```

---

### Task 4: Request validation

**Files:**
- Create: `internal/optimizer/validate.go`
- Test: `internal/optimizer/validate_test.go`

**Interfaces:**
- Produces:
```go
type ValidationError struct {
	Message string
	Details map[string]string
}
func (e *ValidationError) Error() string

// Validate returns a *ValidationError or nil.
func (in *Input) Validate() error
```

Validation (spec 3): required battery fields present (`s_min`, `s_max`, `s_initial`, `c_min`, `c_max`, `d_max`, `p_a`); equal series lengths; `s_initial <= s_capacity`; `s_goal`/`p_demand` length == T; strategy enum membership (`none`, `charge_before_export`, `attenuate_demand_peaks`, `attenuate_feedin_peaks`, `attenuate_grid_peaks` / `none`, `discharge_before_import`); non-negative ranges. Detail keys use field paths: `batteries.0.s_max`, `time_series.ft`.

- [ ] **Step 1: Write the failing test**

```go
func TestValidateSeriesLengthMismatch(t *testing.T) {
	in := Input{Batteries: []Battery{validBattery()}, Series: Series{
		DT: []int{3600, 3600}, GT: []float64{1, 1}, FT: []float64{1, 1},
		PN: []float64{0.3}, PE: []float64{0.1, 0.1},
	}}
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	if _, ok := ve.Details["time_series.p_N"]; !ok {
		t.Fatalf("want a time_series.p_N detail, got %v", ve.Details)
	}
}

func TestValidateMissingRequiredField(t *testing.T) {
	b := validBattery()
	b.SMax = nil
	in := Input{Batteries: []Battery{b}, Series: twoStepSeries()}
	err := in.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Details["batteries.0.s_max"] == "" {
		t.Fatalf("want batteries.0.s_max detail, got %v", err)
	}
}
```

(`validBattery()` and `twoStepSeries()` are small test helpers returning a complete 2-step request.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/optimizer/ -run TestValidate -v`
Expected: FAIL.

- [ ] **Step 3: Implement `Validate`**

Collect every failure into `Details` (do not stop at the first) so one response lists them all; `Message` is a short summary.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/optimizer/ -run TestValidate -v`
Expected: PASS.

- [ ] **Step 5: Add the all-zero `s_goal` and mismatched-`p_demand` tests (Review Focus 5)**

```go
func TestValidateAllZeroGoalIsAllowed(t *testing.T) {
	b := validBattery()
	b.SGoal = []float64{0, 0}
	in := Input{Batteries: []Battery{b}, Series: twoStepSeries()}
	if err := in.Validate(); err != nil {
		t.Fatalf("all-zero s_goal must be valid, got %v", err)
	}
}

func TestValidateDemandLengthMismatch(t *testing.T) {
	b := validBattery()
	b.PDemand = []float64{1}
	in := Input{Batteries: []Battery{b}, Series: twoStepSeries()}
	var ve *ValidationError
	if !errors.As(in.Validate(), &ve) || ve.Details["batteries.0.p_demand"] == "" {
		t.Fatalf("want batteries.0.p_demand detail")
	}
}
```

Run: `go test ./internal/optimizer/ -run TestValidate -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat: validate charge-schedule requests"
```

---

### Task 5: Model construction (variables and structural constraints)

**Files:**
- Create: `internal/optimizer/model.go`
- Test: `internal/optimizer/model_test.go`

**Interfaces:**
- Consumes: `Problem`, `Bat`, `Series` (Task 3).
- Produces:
```go
// model holds the built HiGHS model plus column indices and reusable coefficient
// rows for the lexicographic stages.
type model struct {
	m    *highs.Model
	T    int
	prob Problem

	c, d, s [][]int // [battery][step]
	n, e    []int
	y       []int
	zCD     [][]int
	zC      [][]int // nil per battery when c_min == 0
	// slacks, gating, peaks, starts ...
	costRow, prefRow, peakRow, startRow []coef // coefficient vectors over all columns
}

type coef struct{ col int; val float64 }

func buildModel(p Problem) *model
```

`buildModel` adds every variable and every structural constraint from spec section 5.2. Stage bound rows (6) are added later by the cascade, not here.

- [ ] **Step 1: Write the failing test (balance holds after a solve)**

```go
func TestModelBalanceHolds(t *testing.T) {
	p, _ := twoStepProblem() // one battery, PV on, house load, no strategy
	mo := buildModel(p)
	mo.m.ColCosts = zeroCosts(mo) // any objective; feasibility is what we check
	mo.m.Maximize = true
	sol, err := mo.m.Solve(highs.WithOutput(false))
	if err != nil || !sol.HasSolution() {
		t.Fatalf("solve failed: %v %v", err, sol)
	}
	// RowValues[balanceRow[t]] must equal gt[t] - ft[t] + net, i.e. the row is satisfied.
	// Evaluate independently from ColValues instead: sum_i(d-c) + ft + (n+excImp) - (e+excExp) - gt == 0
	for t := 0; t < mo.T; t++ {
		bal := p.Series.FT[t] - p.Series.GT[t]
		for i := range p.Batteries {
			bal += value(mo, sol, mo.d[i][t]) - value(mo, sol, mo.c[i][t])
		}
		bal += gridImport(mo, sol, t) - gridExport(mo, sol, t)
		if math.Abs(bal) > 1e-3 {
			t.Fatalf("t=%d balance off by %v", t, bal)
		}
	}
}
```

(`value`, `gridImport`, `gridExport`, `zeroCosts`, `twoStepProblem` are test helpers.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/optimizer/ -run TestModelBalanceHolds -v`
Expected: FAIL (undefined: buildModel).

- [ ] **Step 3: Implement `buildModel`**

Follow spec section 5.1 (variables) and 5.2 (constraints 1-14). Build coefficient vectors `costRow`, `prefRow`, `peakRow`, `startRow` as side products of the same loop so the cascade can reuse them. Keep `h[t] = float64(dt[t]) / 3600`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/optimizer/ -run TestModelBalanceHolds -v`
Expected: PASS.

- [ ] **Step 5: Add the high-SOC test (Review Focus 2)**

```go
func TestModelForcesDischargeFromOverfullBattery(t *testing.T) {
	// s_initial above s_max: the sMaxPen slack is penalized in costRow, so a cost
	// solve must discharge toward s_max. Assert SOC is closer to s_max than s_initial.
	// (Solved through Solve() in Task 8; here drive the cost stage directly.)
}
```

Implement it as an assertion that solving the model with `costRow` sets `s[0][T-1] <= s_initial`. Run it, expect PASS.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat: build the MILP model from a request"
```

---

### Task 6: Cost objective, clean value and result assembly

**Files:**
- Create: `internal/optimizer/objective.go`
- Create: `internal/optimizer/result.go`
- Test: `internal/optimizer/result_test.go`

**Interfaces:**
- Consumes: `model` (Task 5).
- Produces:
```go
// costCoeffs returns T1 objective coefficients (spec 5.3).
func (mo *model) costCoeffs() []float64

// cleanValue recomputes the reported objective from a solution (spec 5.3).
func (mo *model) cleanValue(sol *highs.Solution) float64

// assemble builds the OptimizationResult payload; status is mapped by Task 7.
func (mo *model) assemble(sol *highs.Solution, status string) Result
```

- [ ] **Step 1: Write the failing test (clean value on a no-grid-limit case)**

Use the analytic case: house `gt=0`, PV `ft=0`, one battery, price `p_N=0.3` flat, goal must be met, `p_a=0`. The clean value is `-import * price`. With an independent expected value computed by hand for a 1-battery, 2-step toy, assert `cleanValue` matches within `1e-6`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/optimizer/ -run TestCleanValue -v`
Expected: FAIL.

- [ ] **Step 3: Implement `costCoeffs`, `cleanValue`, `assemble`**

`cleanValue` per spec 5.3: grid import cost (plus `excImp` when a limit is set) + export revenue + `(s[T-1] - s_initial)*p_a` + demand-rate charge. `assemble` fills every field, uses empty slices for a non-solved status, and leaves `grid_import_overshoot`/`grid_export_overshoot` as `[]` unless the matching `p_max` is set (spec 3).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/optimizer/ -run TestCleanValue -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: cost objective, clean value and result assembly"
```

---

### Task 7: Lexicographic cascade and status mapping

**Files:**
- Create: `internal/optimizer/solve.go`
- Test: `internal/optimizer/solve_test.go`

**Interfaces:**
- Consumes: `model`, `costCoeffs`, `assemble` (Tasks 5-6), `settings.Settings` (Task 2).
- Produces:
```go
// Solve is the package entrypoint used by the API layer.
func Solve(in Input, s settings.Settings) (Result, error) // runs Normalize+Validate internally? No: caller validates.

// run stages, in order, on one model.
func (mo *model) run(s settings.Settings) (Result, error)

func mapStatus(st highs.ModelStatus, hasSolution bool) string
```

- [ ] **Step 1: Write the failing test (known-answer cost case 024, cost stage only)**

```go
func TestSolve024CostObjective(t *testing.T) {
	in := loadCase(t, "../../test_cases/024-attenuate-demand-peaks.json")
	p, err := in.Normalize()
	if err != nil { t.Fatal(err) }
	// expected objective_value from the golden file
	got, err := Solve(in, settings.Settings{})
	if err != nil { t.Fatal(err) }
	if got.Status != "Optimal" && got.Status != "Feasible" { t.Fatalf("status %s", got.Status) }
	if math.Abs(*got.ObjectiveValue - (-1263.1579199999999)) > 1e-3 {
		t.Fatalf("objective %v", *got.ObjectiveValue)
	}
	_ = p
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/optimizer/ -run TestSolve024CostObjective -v`
Expected: FAIL (undefined: Solve).

- [ ] **Step 3: Implement the cascade**

For each stage that applies, set `mo.m.Maximize`, `mo.m.ColCosts`, solve with `highs.WithOutput(false)`, `highs.WithPresolve("on")`, `highs.WithThreads` when set, `highs.WithTimeLimit` from a shared deadline and per-stage slice, and `highs.WithMIPRelGap` per stage (0 where the spec says proven). After a stage, pin its value: append a bound row (`AddSparseRow`) for cost/preference, set `ColUpper` for a peak. Keep the best solution seen. Implement `mapStatus` per spec 6.1:
- `Optimal` -> `Optimal`
- `TimeLimit`,`IterationLimit`,`ObjectiveBound`,`ObjectiveTarget` -> `Feasible` if `hasSolution` else `Not Solved`
- `Infeasible`,`UnboundedOrInfeasible` -> `Infeasible`
- `Unbounded` -> `Unbounded`
- anything else -> `Not Solved`

Then run the final safety pass (spec 6.1): recompute balance rows, SOC bounds and integrality within tolerance; downgrade to `Not Solved` on failure.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/optimizer/ -run TestSolve024 -v`
Expected: PASS.

- [ ] **Step 5: Add the zero-limit versus absent-limit test (Review Focus 3)**

```go
func TestOvershootSeriesPresence(t *testing.T) {
	// absent p_max_exp: grid_export_overshoot must be [] (len 0)
	// present p_max_exp = 0: grid_export_overshoot must be length T
}
```

Run it, expect PASS.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat: lexicographic solve cascade and status mapping"
```

---

### Task 8: Preference stage (charge-before-export, discharge-before-import, priority)

**Files:**
- Modify: `internal/optimizer/solve.go`
- Test: `internal/optimizer/solve_test.go`

**Interfaces:**
- Consumes: `prefRow` (Task 5), cascade (Task 7).
- Produces: stage `preference` runs when `prefRow` is non-empty, at `mip_rel_gap` 0, under the cost bound.

- [ ] **Step 1: Write the failing test (case 009, discharge-before-import)**

Assert the solved schedule discharges the battery before importing: for the golden case, the first grid-import step has SOC at `s_min` (or equal-or-better objective and a check that import starts no earlier than the golden schedule). Reference the expected `discharge_before_import` shape from `023`/`009` golden values.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/optimizer/ -run TestSolve009 -v`
Expected: FAIL.

- [ ] **Step 3: Implement the `preference` objective**

Fill `prefRow` per spec 5.3 (T3): `charge_before_export` penalizes export by `min_import_price * 2e-5 * (T - t)`; `discharge_before_import` penalizes import by `min_import_price * 5e-6 * (T - t)`; `c_priority` adds `min_import_price * 5e-5 * (T - t) * priority` on `c` and `d`. Normalize `prefRow` to unit magnitude before solving so its gap tolerance is meaningful. Solve at gap 0.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/optimizer/ -run TestSolve009 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: preference stage for cost-neutral strategies"
```

---

### Task 9: Continuity stage (fewest charge starts)

**Files:**
- Modify: `internal/optimizer/model.go`, `internal/optimizer/solve.go`
- Test: `internal/optimizer/solve_test.go`

**Interfaces:**
- Consumes: `startRow`, `zC` (Task 5).
- Produces: stage `continuity` runs when a battery with `c_min > 0` exists, minimizing `sum start` under the cost, peak and preference bounds.

- [ ] **Step 1: Write the failing test**

Build a case with one `c_min > 0` battery and a goal reachable in one contiguous window; assert the solved schedule has exactly one charge session (count transitions from `charging == 0` to `charging > 0`). Then the same with `c_active = true`: assert zero counted starts for a device that charges from step 0.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/optimizer/ -run TestContinuity -v`
Expected: FAIL.

- [ ] **Step 3: Implement the continuity stage**

Add `start[i][t] >= z_c[i][t] - (z_c[i][t-1] or c_active_i)` constraints (spec 5.2.14) and minimize their sum with `Maximize=false` and `ColCosts = startRow`. Run under the cost/peak/preference bound rows, gap 0, small time slice.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/optimizer/ -run TestContinuity -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: continuity stage minimizes charge starts"
```

---

### Task 10: Peak stage and peak-profile smoothing

**Files:**
- Modify: `internal/optimizer/model.go`, `internal/optimizer/solve.go`
- Test: `internal/optimizer/solve_test.go`

**Interfaces:**
- Consumes: `peakRow`, `dev[t]` (Task 5).
- Produces: stage `peak` (minimize `peakImp`/`peakExp`) then smoothing inside the `preference` stage, for the active sides from the strategy.

- [ ] **Step 1: Write the failing test (case 024 smoothing)**

For case 024 (`attenuate_demand_peaks`, flat PV-less profile), assert the solved `grid_import` series is flat: `max(import) - min(import) <= 1` (within tolerance), which is the property the old objective could only reach by luck.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/optimizer/ -run TestSolve024 -v`
Expected: FAIL on flatness before smoothing is implemented.

- [ ] **Step 3: Implement peak + smoothing**

Stage `peak`: `Maximize=false`, `ColCosts = peakRow` (1.0 on the active peak vars), gap 0, under the cost bound. Then pin each active peak via `ColUpper`. In the `preference` stage add `sum dev` with `dev[t] >= |p_side[t] - p_side[t-1]|` (spec 5.2.13) so the profile flattens below the pinned peak.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/optimizer/ -run 'TestSolve02[456]' -v`
Expected: PASS (constant 024, feedin 025, both 026).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: peak levelling with profile smoothing"
```

---

### Task 11: Grid limits, demand rate and violation reporting

**Files:**
- Modify: `internal/optimizer/model.go`, `internal/optimizer/result.go`
- Test: `internal/optimizer/solve_test.go`

**Interfaces:**
- Consumes: `excImp`, `excExp`, `zImpLim`, `zExpLim`, `pMaxImpExc` (Task 5).
- Produces: correct `limit_violations` flags and overshoot series (spec 3).

- [ ] **Step 1: Write the failing tests (cases 013, 014)**

Case 013 (`grid-export-limit-hit`): assert `limit_violations.grid_export_limit_hit == true` and `len(grid_export_overshoot) == T` with a positive entry. Case 014 (`grid-import-limit-violation`): `grid_import_limit_exceeded == true`. Both must still return status `Optimal`/`Feasible` (soft constraints, spec 5.2.3).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/optimizer/ -run TestSolve01[34] -v`
Expected: FAIL until limits and reporting are correct.

- [ ] **Step 3: Fix the limit constraints and reporting**

Ensure `excImp`/`excExp` enter the balance (`importTot`/`exportTot`) and the clean import series exactly when the corresponding limit is set (spec 5.2.1, 5.3), that the exceedance gating uses `zImpLim`/`zExpLim`, and that `assemble` sets the violation flags from whether any `exc*` is positive.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/optimizer/ -run TestSolve01[34] -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "fix: grid limit softness and violation reporting"
```

---

### Task 12: HTTP service (charge-schedule and health)

**Files:**
- Create: `internal/api/server.go`, `internal/api/handlers.go`
- Create: `cmd/optimizer/main.go`
- Test: `internal/api/handlers_test.go`

**Interfaces:**
- Consumes: `optimizer.Solve`, `optimizer.Input`, `optimizer.ValidationError`, `settings.Load` (Tasks 2-7).
- Produces:
```go
func NewServer(s settings.Settings) http.Handler
```

- [ ] **Step 1: Write the failing tests**

```go
func TestHealth(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/optimize/health", nil))
	if rec.Code != 200 { t.Fatalf("code %d", rec.Code) }
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "healthy" { t.Fatalf("body %v", body) }
}

func TestChargeScheduleRejectsBadInput(t *testing.T) {
	// POST a request whose p_N has the wrong length; expect 400 and a JSON body
	// with a "message" key (never an empty body).
}

func TestChargeScheduleSolves(t *testing.T) {
	// POST test_cases/024 request; expect 200, status "Optimal", objective_value non-null.
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement the server and handlers**

`POST /optimize/charge-schedule`: decode into `optimizer.Input`; on a JSON error return 400 `{message}`; call `Validate` and, on `*ValidationError`, return 400 `{message, details}`; call `Normalize` then `Solve`; marshal the result 200. `GET /optimize/health` returns the exact body from spec 3. `cmd/optimizer/main.go` loads settings, builds the server, and listens on `:7050`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: HTTP service for charge-schedule and health"
```

---

### Task 13: Golden-case conformance harness

**Files:**
- Create: `internal/optimizer/conformance_test.go`

**Interfaces:**
- Consumes: `Solve`, `Input`, `ValidationError` (Tasks 3-7).

- [ ] **Step 1: Write the harness**

Glob `../../test_cases/*.json`. For each with an `expected_response`, decode `request` into `Input`, `Validate`, `Normalize`, `Solve`, then assert:
- `result.Status == expected.status`,
- `result.ObjectiveValue >= expected.objective_value - tol` with `tol = max(1e-4, 1e-5*|expected|)` (equal or better, spec 12),
- for `Optimal`/`Feasible`: `len(result.GridImport) == T`, `len(result.Batteries) == len(batteries)`,
- energy balance reconstructed from the payload holds within `1e-2` per step,
- SOC within `[s_min - eps, s_max + eps]` unless the request forces otherwise,
- `c_min` gating: each `charging_power` is `0` or `>= c_min*h[t]` unless `room <= c_min*h[t]` (the #19 invariant).

- [ ] **Step 2: Run it and record failures**

Run: `go test ./internal/optimizer/ -run TestConformance -v`
Expected: some cases fail; capture the list.

- [ ] **Step 3: Fix the failing cases**

Work through failures one at a time, adding a focused unit test for each root cause before fixing, so a regression re-trips a small test rather than the whole suite.

- [ ] **Step 4: Run it to verify all cases pass**

Run: `go test ./internal/optimizer/ -run TestConformance -v`
Expected: PASS for all 20 cases.

- [ ] **Step 5: Add the long-horizon drift test (Review Focus 4)**

Assert case 028 (90 KB, long horizon) solves and its balance holds within `1e-2` per step. Run it, expect PASS.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "test: golden-case conformance harness"
```

---

### Task 14: Observability

**Files:**
- Create: `internal/optimizer/log.go`
- Modify: `internal/optimizer/solve.go` (collect stage timings), `internal/api/handlers.go` (emit the line, dump slow requests, JWT subject)
- Test: `internal/api/observability_test.go`, `internal/optimizer/log_test.go`

**Interfaces:**
- Consumes: `settings.Settings` (Task 2).
- Produces: one `{"solve": {...}}` JSON line per request with keys `client`, `elapsed`, `stages`, `path`, `preferences`, `continuity`, `status`, `steps`; JSONL append to `OPTIMIZER_DUMP_SLOW_REQUESTS` on time-limit exhaustion; optional `sub` logging.

- [ ] **Step 1: Write the failing tests**

```go
func TestSolveLogLineKeys(t *testing.T) {
	// capture stdout, POST a request with User-Agent "evcc/0.311.1",
	// assert exactly one line starting {"solve" with the key set above.
}

func TestSlowRequestsDumped(t *testing.T) {
	// settings.DumpSlowRequests to a temp file, TimeLimit 0, two POSTs -> two JSONL lines.
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ -run 'TestSolveLog|TestSlowRequests' -v`
Expected: FAIL.

- [ ] **Step 3: Implement logging, dumping and JWT subject**

Thread stage timings out of `run` into the result's log metadata. Emit the line in the handler (so it has the request's `User-Agent`). Dump to the file named by settings when the solve exhausted its limit. When `LogSubject` is set and `Authorization: Bearer` is present, verify HS256 with `JWT_TOKEN_SECRET` and log `sub`, else log nothing (never the token).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -run 'TestSolveLog|TestSlowRequests' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: per-request solve log, slow dump, JWT subject"
```

---

### Task 15: CLI client

**Files:**
- Create: `cmd/client/main.go`
- Test: `cmd/client/main_test.go`

**Interfaces:**
- Consumes: `optimizer.Input`, `optimizer.Result`.

- [ ] **Step 1: Write the failing test**

Test the pure formatting function, not the network: given an `optimizer.Result`, `render(w io.Writer, r Result)` writes one row per step per battery with charge/discharge/SOC and the grid import/export, plus an objective line.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/client/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement the client**

Read a case file (`jq .request`-style JSON with a top-level `request` key, or a bare request), POST to `$OPTIMIZER_URL` or `http://localhost:7050`, print via `render`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/client/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: CLI client renders a solved schedule"
```

---

### Task 16: Container and finish

**Files:**
- Create: `Dockerfile`
- Modify: `Makefile`, `README.md`
- Test: `internal/optimizer/conformance_test.go` (final run)

**Interfaces:** none new.

- [ ] **Step 1: Write the Dockerfile**

Multi-stage. Builder: a Go image with a C++ toolchain (`CGO_ENABLED=1`), `go build ./cmd/optimizer`. Final: a minimal runtime image (HiGHS needs libstdc++/libc, so not `scratch`), expose `7050`. Build for the host arch by default; note in the README that Linux images build with Docker.

- [ ] **Step 2: Verify the binary and container build**

Run: `go build ./... && docker build -t optima .`
Expected: both succeed.

- [ ] **Step 3: Run the full suite**

Run: `go test ./...`
Expected: all tests pass, all 20 conformance cases green.

- [ ] **Step 4: Makefile `docker` and README**

Add `docker` target. README: what it is, how to run, how it maps to the upstream API, the two build caveats (cgo toolchain locally, Docker for Linux), and the improvements over the baseline (spec 7).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "chore: container, make targets and README"
```

---

## Self-Review Checklist (author's, before handoff)

- Spec coverage: section 3 -> Tasks 3,4,12; 5 -> Tasks 5,6,8,9,10,11; 6 -> Tasks 7,9,10; 7 -> Tasks 8,9,10; 8 -> Task 2; 9 -> Task 14; 10 -> Task 15; 11 -> Tasks 1,16; 12 -> Task 13; 13 (risks) -> resolved in Global Constraints (warm start dropped) and Task 1 (binding smoke test).
- Types named consistently: `Problem`, `Bat`, `model`, `coef`, `Solve`, `mapStatus`, `cleanValue`, `assemble`, `costCoeffs`, `NewServer`.
- No task transcribes a model body: all constraint bodies cite spec section 5.2 and are written once, in Task 5.
- Review Focus items 1-5 each land as a test: 1 -> Task 3.5, 2 -> Task 5.5, 3 -> Task 7.5, 4 -> Task 13.5, 5 -> Task 4.5.
