# Upstream compatibility policy

`optima` is a drop-in replacement for the upstream
[`evcc-io/optimizer`](https://github.com/evcc-io/optimizer) service. This document
is the contract: what must match upstream, how upstream changes are absorbed, and
which deliberate divergences exist.

## The compatibility contract

Frozen 1:1 with upstream:

- Paths and methods: `POST /optimize/charge-schedule`, `GET /optimize/health`.
- Request/response JSON shape, field names and defaults.
- The `status` enum: `Optimal, Feasible, Infeasible, Unbounded, Undefined, Not Solved`.
- The error response shape.
- Network port `7050`.
- `OPTIMIZER_*` environment variable names.

**Conflict rule.** Upstream wins on shape and semantics; we win on which schedule is
returned among equal-or-better ones.

**Acceptance.** A response is conformant iff `status` matches upstream's,
`objective_value` is at-least upstream's, and the model invariants (energy balance,
SOC bounds, `c_min` gating) hold. Schedule arrays are deliberately not compared.
This is enforced by `internal/optimizer/conformance_test.go` (`TestConformance`) and
is the only acceptance gate for absorbing upstream changes.

## Absorbing upstream changes

`openapi.yaml` and `test_cases/` are vendored from upstream at the revision recorded
in `upstream.lock`. The `.github/workflows/upstream-drift.yml` job checks upstream
`main` weekly and opens one issue labelled `upstream-drift` when a file is added,
changed or removed.

Triage an opened drift issue:

1. On a branch, run `make vendor-upstream REV=<sha>` (the SHA is in the issue).
2. Run `go test ./...`.
3. Green and the change is additive (a new optional field, a new enum value, a new
   case) — review the diff, commit, close the issue.
4. A golden case fails — decide whether it is our regression (fix the solver) or an
   upstream semantic change (absorb it; if it collides with one of our divergences,
   record or retire that divergence here). Never loosen the acceptance rule to make
   a case pass.

## Divergence registry

Each row is an intentional deviation from upstream. All are permitted by the conflict
rule and pinned by a test.

| id | surface | what differs | why | pinned by |
|---|---|---|---|---|
| D1 | validation | `p_N`, `p_E`, `p_a` may be negative | day-ahead prices and export tariffs go negative; `gt` and `ft` still require `>= 0` | `internal/optimizer/validate_test.go` |
| D2 | validation | `dt` must be `> 0` | a zero/negative step has no meaning in `h[t] = dt/3600` | `internal/optimizer/validate_test.go` |
| D3 | validation | empty series rejected; every `time_series` array must match `gt`'s length | an empty horizon cannot be solved | `TestValidateRejectsEmptySeries` |
| D4 | validation | `eta_c`/`eta_d` must be in `(0, 1]` | a zero or above-one efficiency is not physical | `TestValidateRejectsZeroEta` |
| D5 | schedule | peak-profile smoothing: a flatter profile at equal-or-better objective | upstream pins only the horizon maximum, so a plateau and a jagged profile score alike | `TestSolve024PeakFlat`, `TestPeakStagePinsActivePeak` |
| D6 | schedule | proven-optimal tie-breaks (the preference stage runs at zero MIP gap) | upstream stops at an approximate LP floor | `TestSolve024CostObjective`, `TestContinuityKeepsPreferenceValue` |
| D7 | schedule | continuity is a real objective under hard cost, peak and preference bounds | never trades money or preference value for a nicer schedule | `TestContinuityReducesChargeStarts`, `TestContinuitySpendsNoMoney`, `TestContinuityKeepsPreferenceValue` |

Every schedule divergence (D5–D7) is additionally bound by `TestConformance`'s
match-or-beat acceptance: the schedule we return may differ from upstream's, but its
objective value may never be worse.

## Our additions

Anything we add must be optional with an upstream-safe default and must never change
the shape of an existing response. There are no additions yet. When one is added, give
it a row here.
