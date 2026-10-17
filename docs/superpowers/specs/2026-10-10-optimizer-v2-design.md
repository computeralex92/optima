# optima design

A ground-up rewrite of the evcc charging optimizer (https://github.com/evcc-io/optimizer)
in Go, backed by HiGHS instead of PuLP/CBC. It is API compatible, so it can stand in
for the Python service next to evcc, and it targets better performance and better
end-results than the original.

Status: design, awaiting review before an implementation plan is written.

## 1. Context

The original is a Python 3.13 service (Flask + flask-restx) that builds a Mixed
Integer Linear Program with PuLP and solves it with CBC. One request runs up to five
solver stages under a single wall clock (`build`, `probe`, `cost`, `tie_break` LP +
MILP, `continuity`), with objective rescaling and a long list of CBC workarounds. The
Go code in the repository is only a CLI client plus a generated OpenAPI client.

This rewrite keeps the **API contract** and redesigns everything behind it. The
optimizer core, the solve strategy and the model construction are new.

## 2. Goals and non-goals

Goals:
- Drop-in for a personal evcc: same endpoints, same request/response JSON, same
  features, same status semantics.
- Better performance: fewer solves, a modern solver, native parallelism, optional
  warm starts.
- Better end-results: proven-optimal tie-breaks, a real continuity objective, and
  peak-profile smoothing the original documents as a blind spot.
- A single self-contained binary, runnable as a small container.
- Conformance harness that proves every golden case is matched or beaten.

Non-goals:
- Upstream adoption. The API is the compatibility boundary; internals are free.
- New request/response fields. Diagnostics stay in logs, not the payload.
- Changing existing strategy semantics or the strategy enum.
- Replacing the CLI client's feature set, only its language.

## 3. API contract

Two endpoints, identical to the original.

- `POST /optimize/charge-schedule`, body `OptimizationInput`, returns
  `OptimizationResult`.
- `GET /optimize/health`, returns `{"status":"healthy","message":"EV Charging MILP API is running"}`.

The vendored `openapi.yaml` is the source of truth.

### Request

`OptimizationInput`: optional `strategy`, optional `grid`, required `batteries`
(min 1) and `time_series`, optional `eta_c`/`eta_d`.

Battery fields: `charge_from_grid`, `discharge_to_grid`, `s_capacity`, `s_min`,
`s_max`, `s_initial`, `c_min`, `c_max`, `d_max`, `p_a`, optional `p_demand`,
`s_goal`, `c_priority`, `c_active`.

Time series: `dt` (int seconds), `gt`, `ft`, `p_N`, `p_E`, all equal length.

### Defaults (normalize step)

- `eta_c = eta_d = 0.95`
- `charge_from_grid = false`, `discharge_to_grid = false`
- `s_capacity = s_max`
- `c_priority = 0`, `c_active = false`
- `charging_strategy = discharging_strategy = none`

### Validation (400)

All time series equal length. `s_initial <= s_capacity`. `s_goal` and `p_demand`
length equal to T. Required battery fields present. Strategy enums valid. Numeric
ranges per the spec. Failure returns HTTP 400 with `{message, details}`, `details`
keyed by field path (for example `batteries.0.s_max`).

### Response contract details

- `status`: one of `Optimal, Feasible, Infeasible, Unbounded, Undefined, Not Solved`.
- `objective_value`: the **clean** value (see 5.3). Present for `Optimal`/`Feasible`,
  `null` otherwise. Nullable.
- On a non-solved status every array is empty and `objective_value` is `null`.
- `grid_import_overshoot` / `grid_export_overshoot` are `[]` unless the corresponding
  `p_max` is configured.
- `grid_import` includes the demand-rate overshoot when `prc_p_exc_imp` is active.
- `flow_direction[t]` is `y` (0 import, 1 export) for every step.
- All slices serialize as `[]`, never `null`.

## 4. Architecture and layout

```
optima/
├─ go.mod                      module github.com/computeralex92/optima
├─ cmd/optimizer/main.go       HTTP service
├─ cmd/client/main.go          CLI client
├─ internal/
│  ├─ api/                     types, normalize, validate, handlers, server
│  ├─ optimizer/               model, objective, scale, solve, result
│  └─ settings/                OPTIMIZER_* env
├─ test_cases/*.json           vendored golden cases
├─ internal/conformance/       golden-case harness
├─ openapi.yaml                vendored contract
├─ Dockerfile
└─ README.md
```

Dependency: `github.com/bartolsthoorn/gohighs/highs` (cgo, embedded static HiGHS).

The api package holds no solver logic; the optimizer package holds no HTTP. This
keeps the model testable in isolation and the transport thin.

## 5. Optimizer core

Notation: `h[t] = dt[t] / 3600` (hours), energy in Wh, power in W.

### 5.1 Variables

Per battery `i`, step `t`:

| variable | domain | meaning |
|---|---|---|
| `c[i][t]` | `[0, c_max*h[t]]` | charge energy |
| `d[i][t]` | `[0, d_max*h[t]]` | discharge energy |
| `s[i][t]` | `[0, s_capacity]` | state of charge |
| `n[t]`, `e[t]` | `>= 0` | grid import / export energy |
| `y[t]` | `{0,1}` | direction, 0 import, 1 export |
| `z_cd[i][t]` | `{0,1}` | charge/discharge lock |
| `z_c[i][t]` | `{0,1}` | charging active (`c_min > 0`) |
| `sMinPen, sMaxPen[i][t]` | `>= 0` | SOC overflow slacks |
| `goalPen[i][t]` | `>= 0` | SOC-goal shortfall |
| `demandPen[i][t]`, `zDem`, `zFull` | `>= 0`, `{0,1}` | charge-demand gating |
| `excImp[t]`, `excExp[t]`, `zImpLim`, `zExpLim` | `>= 0`, `{0,1}` | grid-limit exceedance and gating |
| `pMaxImpExc` | `>= 0` | demand-rate peak power |
| `peakImp`, `peakExp` | `>= 0` | horizon peak per side |
| `dev[t]` | `>= 0` | profile roughness (peak strategies) |
| `start[i][t]` | `{0,1}` | charge-session start (continuity) |

### 5.2 Constraints

1. Energy balance per `t`: `sum_i(d - c) + ft + importTot = exportTot + gt`, where
   `importTot = n + excImp` when a limit or demand-rate is set, `exportTot = e +
   excExp` when an export limit is set.
2. Direction big-M with tightened caps: `m_exp = ft + sum over discharge-to-grid of
   d_max*h[t]` when no import limit, else `M`; `m_imp = gt + sum over charge-from-grid
   of c_max*h[t]` when no export limit, else `M`. Then `e <= m_exp*y`,
   `n <= m_imp*(1-y)`.
3. Grid power caps (soft): `n <= p_max_imp*h[t]` with `excImp` gated by `zImpLim`;
   symmetric for export.
4. Demand-rate coupling: `excImp[t] <= pMaxImpExc*h[t]`.
5. SOC soft bounds: `sMaxPen >= s - s_max`, `sMinPen >= s_min - s`.
6. Dynamics: `s[i][0] = s_initial + eta_c*c[0] - (1/eta_d)*d[0]`;
   `s[i][t] = s[i][t-1] + eta_c*c[t] - (1/eta_d)*d[t]`.
7. Goals: `s[i][t] + goalPen[i][t] >= s_goal[t]` for `t >= 1`, `s_goal[t] > 0`.
8. Charge-demand gating with `zDem`, `zFull` (the #19-sensitive block, kept as in the
   original).
9. `c_min` gating: `c >= c_min*h[t]*z_c`, `c <= c_max*h[t]*z_c`.
10. Grid permission: `!charge_from_grid -> c <= c_max*h[t]*y`;
    `!discharge_to_grid -> d <= d_max*h[t]*(1-y)`.
11. Charge/discharge lock via `z_cd`: `d <= d_max*h[t]*z_cd`,
    `c <= c_max*h[t]*(1-z_cd)`.
12. Peak: `p_side[t] <= peakSide` for every `t`.
13. Roughness (peak strategies only): `dev[t] >= |p_side[t] - p_side[t-1]|`.
14. Continuity: `start[i][t] >= z_c[i][t] - (z_c[i][t-1] or c_active_i)`.

### 5.3 Objectives

- **Clean value** (reported `objective_value`): grid import cost (including `excImp`
  when a limit is set) + export revenue + `(s[T-1] - s_initial)*p_a` + demand-rate
  charge `-prc_p_exc_imp * pMaxImpExc`. No penalties, no preferences.
- **T1 cost**: clean economic terms plus the avoidance penalties (SOC, goal, demand,
  grid exceedance) and the demand-rate charge.
- **T2 peak**: minimize `peakImp` / `peakExp` on the active sides.
- **T3 preferences**: `charge_before_export`, `discharge_before_import`, `c_priority`,
  plus `sum dev` smoothing when a peak strategy is active.
- **T4 continuity**: minimize `sum start`.

Penalty coefficients derive from a positive floor, `max(max_import_price,
max_export_price, battery p_a, demand-rate per-Wh equivalent, 0.1e-3)`, so they stay
positive under zero or negative prices.

## 6. Solve orchestration

A lexicographic cascade. Each stage optimizes its objective, then pins the achieved
value with a bound row (tolerance `eps`) before the next stage runs. Stages that do
not apply are skipped. All stages share one wall clock derived from
`OPTIMIZER_TIME_LIMIT`.

| stage | objective | runs when | HiGHS stop |
|---|---|---|---|
| A `cost` | T1 | always | `mip_rel_gap` from gap_abs / gap_share, time slice |
| B `peak` | T2 | a peak strategy is set | gap 0, small slice |
| C `preference` | T3 | any T3 term or peak smoothing | gap 0 (proven-optimal tie break) |
| D `continuity` | T4 | a `c_min > 0` battery could have more than one session | gap 0, small slice |

Bound rows, with `eps` small:

- after A: `cost >= valueA - eps`
- after B: `peak <= valueB + eps`
- after C: `preference >= valueC - eps`

Continuity runs under all three bounds. This is what makes continuity and the tie
breaks "real" objectives: they can never trade away money or a higher-priority
preference, unlike the original's bounded-but-approximate stage.

The clean value is recomputed from the final solution for the payload.

### 6.1 Status mapping

- Proven optimal on the last retained stage, and solution integral and feasible ->
  `Optimal`.
- Time limit or iteration limit with a usable incumbent -> `Feasible` (payload is
  complete, same shape as `Optimal`).
- No incumbent -> `Infeasible` or `Not Solved`.
- Final safety pass: re-check balance rows, SOC bounds and integrality against a
  tolerance. A schedule that breaks the model becomes `Not Solved` rather than a
  plausible but wrong answer.

### 6.2 Solver options

Per stage: `output_flag=false`, `threads` from settings, `presolve=on`, and a stage
time limit. Stages A/B/C/D use `mip_rel_gap` as above (0 where noted). Objective
scaling is dropped; each stage's objective is homogeneous and HiGHS scales
internally. The preference-stage objective may be normalized to unit magnitude so its
gap tolerance is meaningful.

## 7. Improvements over the baseline

- **Peak-profile smoothing.** The original penalizes only the horizon maximum, so
  once a fixed load pins the peak a plateau and a jagged profile score alike. Stage B
  minimizes the peak; stage C then minimizes total absolute step-to-step change under
  the pinned peak, so the profile is flattened without raising the peak.
- **Proven-optimal tie-breaks.** Stage C runs at gap 0, so cost-neutral strategies get
  the actual best tie break rather than the original's LP floor (a pinned LP that is
  only sometimes beaten by a MILP).
- **Continuity as a real objective.** Stage D minimizes charge starts under the cost,
  peak and preference bounds, with `c_active` meaning an already-running device can
  reach zero starts. Guaranteed not to spend money or preference value.
- **Warm starts.** Each stage is seeded from the prior incumbent, if the solver
  binding supports it (see risks). The original could not, due to a CBC bug.

## 8. Settings (environment)

Prefix `OPTIMIZER_`, matching the original names where they still apply:

- `OPTIMIZER_TIME_LIMIT` (float seconds, unset solves out)
- `OPTIMIZER_GAP_ABS` (currency, default: unset, which solves the cost stage to proven optimality; set it to trade optimality for speed. Upstream defaults to 0.01)
- `OPTIMIZER_GAP_SHARE` (default 0.0; only applied when `GAP_ABS` is set. Upstream defaults to 0.0025)
- `OPTIMIZER_PREFERENCE_BUDGET` (currency, default 0.0, keeps strategies cost neutral)
- `OPTIMIZER_NUM_THREADS`
- `OPTIMIZER_LOG_SUBJECT` (bool, default false)
- `OPTIMIZER_DUMP_SLOW_REQUESTS` (path, unset disables)

`OPTIMIZER_PROBE_SECONDS` is dropped: there is no probe stage.

## 9. Observability

- One structured JSON line per request, `{"solve": {...}}` with keys `client`,
  `elapsed`, `stages`, `path`, `preferences`, `continuity`, `status`, `steps`. The
  semantics reflect our cascade (`stages` maps stage name to seconds; `path` is
  `cascade`), keeping the key set stable for log tooling.
- Slow requests (time limit exhausted) and cost stages left with an open gap are
  appended as JSON Lines to `OPTIMIZER_DUMP_SLOW_REQUESTS`.
- `OPTIMIZER_LOG_SUBJECT`: when set, log the JWT `sub` from `Authorization: Bearer`
  after verifying with `JWT_TOKEN_SECRET` (HS256). Off by default, it names accounts.

## 10. CLI client

`go run ./cmd/client <case.json>` posts the case's `request` to a running service and
renders the schedule as tables (per-battery charge/discharge/SOC, grid import/export)
plus the objective value. Minimal, single dependency on the api types.

## 11. Build and container

- `Makefile`: `build`, `test`, `lint`, `run`, `docker`.
- `Dockerfile`: multi-stage. Builder is a `golang` image with a C toolchain (cgo),
  built for the target arch using gohighs's embedded static HiGHS; final stage is a
  minimal image with the single binary.
- Cross-compilation note: cgo plus static HiGHS means no plain `CGO_ENABLED=0`
  cross-build. Linux targets are built in Docker; GoReleaser is deferred until
  tagged binaries or multi-arch images are wanted.

## 12. Testing and conformance

- Vendor `test_cases/*.json` verbatim.
- Harness per case: POST the request, then assert
  - status equals the expected status,
  - `objective_value >= expected - tol` (equal or better; `rtol 1e-5`, `atol 1e-4`),
  - invariants: energy balance within `eps`, SOC within `[s_min, s_max]` (soft where
    the request forces it), `c_min` gating (charge is 0 or `>= c_min`, unless topping
    off near `s_max`),
  - strategy effect where applicable (peak strategies flatten, charge-before-export
    prefers export timing, continuity reduces starts).
- Unit tests: objective split is exhaustive; scaling/normalization; validation paths
  (400 bodies); status mapping; clean-value computation.
- A `-short` mode skips the slower golden cases.

## 13. Risks and mitigations

- **gohighs capabilities (step 0 spike).** Confirm (a) a time-limit exit returns a
  usable incumbent via `ColValues`, (b) the low-level `Solver` API supports a hot
  start, (c) the model can be re-solved with a changed objective and an added bound
  row without rebuilding. If (a) fails, use the low-level C API directly. If (b)
  fails, drop warm starts (speed nicety, not correctness) and note it.
- **Objective tolerance vs. tiny preferences.** Handled by the lexicographic split:
  preferences are never solved together with money.
- **Golden cases with cost-neutral plateaus.** Acceptance is objective-based, so
  different-but-equally-optimal schedules pass; strategy-effect checks guard the
  intent.
- **cgo/static-link deployment.** Same arch set as the original's Docker targets
  (linux amd64/arm64).

## 14. Milestones

0. Spike gohighs capabilities (13).
1. Skeleton: module, settings, api types/normalize/validate, health endpoint.
2. Model build + cost stage only; conformance on non-strategy cases.
3. Lexicographic cascade: peak, preferences, continuity.
4. Peak-profile smoothing.
5. CLI, Docker, observability.
6. Full conformance green, performance measurement against the baseline.
