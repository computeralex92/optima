# Upstream compatibility policy — design

**Date:** 2026-10-11
**Status:** approved (design), pending implementation
**Author:** Alex + agent

## 1. Problem

`optima` is a Go drop-in replacement for the upstream `evcc-io/optimizer`
service. The compatibility surface — `openapi.yaml` and `test_cases/*.json` — was
vendored from upstream **once** (commit `5940aa8`), from an unrecorded upstream
revision. Today:

- There is no record of which upstream commit the vendored contract corresponds to.
- There is no repeatable way to refresh it.
- Nothing notices when upstream moves. Upstream is actively moving: its `main` was
  committed on 2026-10-11 and has already gained the `attenuate_*` peak strategies
  and their golden cases (which overlap our README's "Improvements").
- The compatibility rule we actually enforce (match-or-beat objective, not exact
  schedules) exists only implicitly in `internal/optimizer/conformance_test.go`,
  and nothing governs *our own* additions versus upstream's contract.

Verification at design time: all 21 vendored blob SHAs (`openapi.yaml` +
`test_cases/*.json`) are identical to upstream `main` @
`95ca7b098f849c9cc3937aeae0bfd53e830f01f6`, so the policy has a clean starting pin.

## 2. Goal / non-goals

**Goal.** A standing, mechanized policy for (a) detecting and absorbing upstream
contract/feature changes and (b) governing our own divergences, with the existing
conformance harness as the acceptance gate.

**Non-goals.**
- No automatic merge of upstream changes; absorption is always a reviewed human commit.
- No running upstream's Python/PuLP test suite in CI (that is approach B, deferred).
- No change to the wire contract, the solver, or existing tests. This adds process
  and three small artifacts only.

## 3. Compatibility contract (the frozen surface)

Frozen 1:1 with upstream:

- Paths and methods: `POST /optimize/charge-schedule`, `GET /optimize/health`.
- Request/response JSON shape, field names and defaults.
- The `status` enum: `Optimal, Feasible, Infeasible, Unbounded, Undefined, Not Solved`.
- The error response shape.
- Network port `7050`.
- `OPTIMIZER_*` environment variable names.

**Conflict rule.** Upstream wins on *shape and semantics*. We win on *which*
schedule is returned among equal-or-better ones.

**Acceptance definition.** A response is conformant iff `status` matches
upstream's, `objective_value` is at-least upstream's, and the model invariants
(energy balance, SOC bounds, `c_min` gating) hold. Schedule arrays are deliberately
not compared. This is already implemented by `TestConformance`; the policy names it
as the contract rather than changing it.

## 4. Artifacts

### 4.1 `upstream.lock`
Three `KEY=value` lines, Makefile-includable:

```
REPO=https://github.com/evcc-io/optimizer
REV=95ca7b098f849c9cc3937aeae0bfd53e830f01f6
DATE=2026-10-11
```

Records exactly what we vendored. `REV` is the upstream commit the vendored
`openapi.yaml` + `test_cases/` correspond to.

### 4.2 `make vendor-upstream`
A Makefile target that:

1. Shallow-clones `$REPO` at `$REV` (overridable: `make vendor-upstream REV=<sha>`).
2. Copies `openapi.yaml` and `test_cases/` into the repo.
3. Rewrites `upstream.lock` with the resolved commit SHA and today's date.
4. Leaves the changes unstaged for a human to review and commit.

Deterministic and non-interactive; it never commits.

### 4.3 `.github/workflows/upstream-drift.yml`
- Triggers: weekly `schedule` + `workflow_dispatch`.
- Permissions: `contents: read`, `issues: write`.
- Steps: check out this repo; shallow-clone upstream `main`; compare vendored
  `openapi.yaml` and `test_cases/` against upstream's; if anything differs
  (added / changed / removed), open or update a single issue labelled
  `upstream-drift` containing the upstream HEAD SHA, the differing files, and the
  `make vendor-upstream REV=<sha>` command to absorb them. If there is no drift,
  do nothing (and close a stale `upstream-drift` issue if one is open).
- Actions pinned by commit SHA, matching the existing `ci.yml` style.
- No auto-PR, no Python.

### 4.4 `docs/compatibility.md` (the policy document)
The human-facing policy, containing:

1. **The contract** — section 3 above.
2. **Triage procedure** for an opened drift issue:
   - `make vendor-upstream REV=<sha>` on a branch.
   - Run `go test ./...`.
   - Green + additive change ⇒ review and merge.
   - A new or changed golden case fails ⇒ classify as *our regression* (fix the
     solver) or *upstream semantic change* (absorb, or record a divergence).
3. **Divergence registry** — one row per intentional deviation: id, surface, what
   differs, why, how it is pinned by a test, upstream-compat impact. Seeded with:
   - The four validator divergences already in the README (negative prices
     accepted, `dt > 0`, empty series rejected, `eta ∈ (0, 1]`).
   - Our solver improvements (peak-profile smoothing, proven-optimal tie-breaks,
     continuity objective), each explicitly permitted by the conflict rule and
     pinned by the conformance harness's match-or-beat acceptance.
4. **Additions rule** — anything we add must be optional with an upstream-safe
   default and must never change the shape of an existing response; additions are
   recorded in their own registry table.

### 4.5 README
One line linking to `docs/compatibility.md`.

## 5. Acceptance / gate

`go test ./...` — in particular `TestConformance` over the vendored golden cases —
is the single acceptance gate. Any PR that absorbs upstream drift must pass it.

## 6. Testing

- `vendor-upstream`: verified by running it (at the pinned `REV` and at
  `REV=main`) and confirming the copy is byte-identical to upstream and the lock
  updates; idempotent when re-run at the same `REV`.
- `upstream-drift.yml`: verified with `workflow_dispatch` against a branch where a
  vendored file has been altered, confirming one issue is opened; and against an
  unmodified branch, confirming no issue and a stale issue is closed.
- `docs/compatibility.md`: no automated test; reviewed by a human.

## 7. Out of scope (future)

- Approach B (git remote mirror, upstream Python tests in CI, auto re-vendor PRs).
- Renovate as the drift detector. The hosted Mend app cannot re-vendor
  (`postUpgradeTasks`/`allowedPostUpgradeCommands` are self-hosted only), so it can
  bump `upstream.lock`'s `REV` but not refresh `openapi.yaml`/`test_cases` — it would
  need an extra consistency gate to be correct, i.e. more moving parts than the
  drift workflow. Upgrade path if PR UX is preferred over an issue: run Renovate
  self-hosted with `postUpgradeTasks.commands = ["make vendor-upstream REV={{{newDigest}}}"]`.
- Any change to solver behavior, the wire contract, or existing tests.
