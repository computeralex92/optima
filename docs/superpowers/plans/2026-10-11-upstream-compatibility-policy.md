# Upstream Compatibility Policy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pin the vendored upstream contract, detect upstream drift automatically, and write the policy that governs upstream absorption and our own divergences.

**Architecture:** `upstream.lock` records the pinned upstream revision; a `vendor-upstream` Make target refreshes the vendored `openapi.yaml` + `test_cases/`; a weekly GitHub Action runs `scripts/upstream-drift.sh` and opens one issue on drift; `docs/compatibility.md` holds the contract, the triage procedure and the divergence registry. No Go code changes.

**Tech Stack:** GNU make, Bash, GitHub Actions, `gh`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-11-upstream-compatibility-policy-design.md` (read both).

## Global Constraints

- The project is **optima**; module path is `github.com/computeralex92/optima`.
- Frozen upstream surface: paths `POST /optimize/charge-schedule`, `GET /optimize/health`; `status` enum `Optimal, Feasible, Infeasible, Unbounded, Undefined, Not Solved`; port `7050`; `OPTIMIZER_*` env names.
- Pinned upstream at design time: `REPO=https://github.com/evcc-io/optimizer`, `REV=95ca7b098f849c9cc3937aeae0bfd53e830f01f6`, `DATE=2026-10-11`.
- No Python in CI. GitHub Actions pinned by commit SHA, matching `.github/workflows/ci.yml`.
- Acceptance gate: `go test ./...` (in particular `TestConformance`).

## Review Focus

1. `make vendor-upstream` with an unreachable repo or a nonexistent `REV` — must fail loudly and change nothing (no half-copied tree).
2. An upstream **removed** test case — after re-vendor, the deleted local file must not linger.
3. Drift report when upstream **adds**, **changes** or **removes** a file — all three must be listed; additions are the common case but not the only one.
4. Drift script when the clone fails (offline/rate-limited) — must exit non-zero, never print an empty (in-sync) report.
5. `upstream.lock` missing or malformed — the vendor target must error clearly, not silently no-op.

---

### Task 1: Pin the upstream revision and add `make vendor-upstream`

**Files:**
- Create: `upstream.lock`
- Modify: `Makefile`

**Interfaces:**
- Produces: `upstream.lock` — three Make-includable lines (`REPO=`, `REV=`, `DATE=`); `make vendor-upstream [REV=<ref>]` — re-copies `openapi.yaml` and `test_cases/`, rewrites `upstream.lock`, leaves the diff unstaged.

- [ ] **Step 1: Create `upstream.lock`**

Exact content:

```
REPO=https://github.com/evcc-io/optimizer
REV=95ca7b098f849c9cc3937aeae0bfd53e830f01f6
DATE=2026-10-11
```

- [ ] **Step 2: Add the include and the target to `Makefile`**

Add `upstream.lock` include at the top (after `CONTAINER_ENGINE ?= podman`), add `vendor-upstream` to `.PHONY`, and append:

```make
include upstream.lock

vendor-upstream: ## Re-vendor openapi.yaml + test_cases from upstream at REV
	@set -e; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	git init -q "$$tmp"; \
	git -C "$$tmp" remote add origin "$(REPO)"; \
	git -C "$$tmp" fetch -q --depth 1 origin "$(REV)"; \
	git -C "$$tmp" checkout -q FETCH_HEAD; \
	cp "$$tmp/openapi.yaml" openapi.yaml; \
	rm -rf test_cases; \
	cp -r "$$tmp/test_cases" test_cases; \
	rev=$$(git -C "$$tmp" rev-parse HEAD); \
	printf 'REPO=%s\nREV=%s\nDATE=%s\n' "$(REPO)" "$$rev" "$$(date +%F)" > upstream.lock; \
	echo "vendored $$rev; review with: git diff --stat"
```

(`set -e` makes a failed `fetch` abort before any file is copied; `rm -rf test_cases` before the copy is what stops a removed upstream case from lingering.)

- [ ] **Step 3: Verify idempotency at the pinned revision**

Run: `make vendor-upstream && git status --porcelain -- openapi.yaml test_cases`
Expected: no output (the vendored files are byte-identical to upstream at the pin).

- [ ] **Step 4: Verify it fails loudly on a bad revision (Review Focus 1)**

Run: `make vendor-upstream REV=0000000000000000000000000000000000000000; echo "exit=$?"`
Expected: `exit=` a non-zero value; `git status --porcelain` shows no change to `openapi.yaml`/`test_cases`.

- [ ] **Step 5: Verify a missing lock errors (Review Focus 5)**

Run: `mv upstream.lock /tmp/upstream.lock.bak && make vendor-upstream; echo "exit=$?"; mv /tmp/upstream.lock.bak upstream.lock`
Expected: make fails with "upstream.lock: No such file"; non-zero exit.

- [ ] **Step 6: Commit**

```bash
git add upstream.lock Makefile
git commit -m "chore: pin the upstream revision and add make vendor-upstream"
```

---

### Task 2: Drift detection — script and weekly workflow

**Files:**
- Create: `scripts/upstream-drift.sh` (executable)
- Create: `.github/workflows/upstream-drift.yml`

**Interfaces:**
- Consumes: `upstream.lock` (`REPO`).
- Produces: `scripts/upstream-drift.sh` — prints a Markdown drift report to stdout and exits 0 when upstream `main` differs from the vendored files; prints nothing and exits 0 when in sync; exits non-zero if the clone fails.

- [ ] **Step 1: Write `scripts/upstream-drift.sh`**

Exact content:

```bash
#!/usr/bin/env bash
# Report drift between the vendored upstream contract and upstream main.
# Prints a Markdown report on drift, nothing when in sync. Non-zero on error.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
. ./upstream.lock
: "${REPO:?upstream.lock must define REPO}"

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
git clone -q --depth 1 "$REPO" "$tmp"   # exits non-zero if the clone fails
rev=$(git -C "$tmp" rev-parse HEAD)

report=""
add() { report="${report}$1"$'\n'; }

cmp -s openapi.yaml "$tmp/openapi.yaml" || add "- changed: \`openapi.yaml\`"
for f in test_cases/*.json; do
  n=${f#test_cases/}
  if [ ! -f "$tmp/$f" ]; then
    add "- removed upstream: \`test_cases/$n\`"
  elif ! cmp -s "$f" "$tmp/$f"; then
    add "- changed: \`test_cases/$n\`"
  fi
done
for f in "$tmp"/test_cases/*.json; do
  n=${f#"$tmp"/test_cases/}
  [ -f "test_cases/$n" ] || add "- added upstream: \`test_cases/$n\`"
done

[ -z "$report" ] && exit 0

printf 'Upstream main has contract changes since the vendored revision.\n\n'
printf 'Upstream HEAD: `%s`\n\n' "$rev"
printf '%s' "$report"
printf '\nAbsorb the changes with:\n\n```sh\nmake vendor-upstream REV=%s\n```\n' "$rev"
```

Then: `chmod +x scripts/upstream-drift.sh`.

- [ ] **Step 2: Verify the in-sync path**

Run: `bash scripts/upstream-drift.sh; echo "exit=$?"`
Expected: no output, `exit=0`.

- [ ] **Step 3: Verify all three drift kinds are reported (Review Focus 2, 3)**

```bash
printf ' ' >> test_cases/009-discharge-before-import.json      # changed
rm test_cases/028-attenuate-grid-peaks-long-horizon.json        # now "added upstream"
printf '{}' > test_cases/999-bogus.json                         # now "removed upstream"
bash scripts/upstream-drift.sh
git checkout -- test_cases && rm -f test_cases/999-bogus.json
```
Expected: the report lists `changed: \`test_cases/009-discharge-before-import.json\``, `added upstream: \`test_cases/028-attenuate-grid-peaks-long-horizon.json\``, `removed upstream: \`test_cases/999-bogus.json\``.

- [ ] **Step 4: Verify a clone failure is not read as in-sync (Review Focus 4)**

```bash
sed 's#^REPO=.*#REPO=https://example.invalid/nope#' upstream.lock > /tmp/lock.bad && cp upstream.lock /tmp/lock.orig && cp /tmp/lock.bad upstream.lock
bash scripts/upstream-drift.sh; echo "exit=$?"
cp /tmp/lock.orig upstream.lock
```
Expected: `exit=` a non-zero value and no report (an error, not "in sync").

- [ ] **Step 5: Write `.github/workflows/upstream-drift.yml`**

Exact content (checkout pinned to the same SHA as `ci.yml`):

```yaml
name: upstream-drift

# Weekly check that the vendored contract (openapi.yaml + test_cases/) still
# matches upstream main. On drift, open or update one issue. Never auto-merges.
on:
  schedule:
    - cron: "0 6 * * 1" # Mondays 06:00 UTC
  workflow_dispatch:

permissions:
  contents: read
  issues: write

jobs:
  drift:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7
      - name: Check upstream for drift
        id: drift
        run: |
          {
            echo "report<<EOF"
            bash scripts/upstream-drift.sh
            echo "EOF"
          } >> "$GITHUB_OUTPUT"
      - name: Ensure the label exists
        if: steps.drift.outputs.report != ''
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: gh label create upstream-drift --color FBCA04 2>/dev/null || true
      - name: Open or update the drift issue
        if: steps.drift.outputs.report != ''
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          REPORT: ${{ steps.drift.outputs.report }}
        run: |
          num=$(gh issue list --label upstream-drift --state open --json number --jq '.[0].number')
          if [ -z "$num" ]; then
            gh issue create --label upstream-drift --title "Upstream contract drift" --body "$REPORT"
          else
            gh issue edit "$num" --body "$REPORT"
          fi
      - name: Close a stale drift issue
        if: steps.drift.outputs.report == ''
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          for num in $(gh issue list --label upstream-drift --state open --json number --jq '.[].number'); do
            gh issue close "$num" --comment "Upstream is in sync with the vendored revision again."
          done
```

- [ ] **Step 6: Lint the workflow**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/upstream-drift.yml')); print('ok')"`
Expected: `ok`.
Run (if present): `command -v actionlint >/dev/null && actionlint .github/workflows/upstream-drift.yml || echo "actionlint not installed (skipped)"`.
Expected: no findings, or the skip notice. End-to-end issue creation requires a pushed branch; note that as manual.

- [ ] **Step 7: Commit**

```bash
git add scripts/upstream-drift.sh .github/workflows/upstream-drift.yml
git commit -m "ci: detect upstream contract drift weekly and open an issue"
```

---

### Task 3: Write `docs/compatibility.md`

**Files:**
- Create: `docs/compatibility.md`

**Interfaces:**
- Consumes: the acceptance rule already implemented in `internal/optimizer/conformance_test.go` (status equality + `objective_value` at-least + model invariants; schedules not compared).
- Produces: the policy document linked from the README in Task 4.

- [ ] **Step 1: Confirm the facts the document asserts**

Run: `go test ./internal/optimizer -run TestConformance`
Expected: PASS (the "match-or-beat" acceptance rule the document states is real).
Run: `grep -n "negative\|dt\|eta_c\|empty" internal/api/*.go`
Expected: hits confirming the four validator divergences exist where documented.

- [ ] **Step 2: Write `docs/compatibility.md`**

Exact content:

```markdown
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
| D1 | validation | negative `p_N`, `p_E`, `p_a` accepted | day-ahead prices and export tariffs go negative | `internal/api` validator tests |
| D2 | validation | `dt` must be `> 0` | a zero/negative step has no meaning in `h[t] = dt/3600` | `internal/api` validator tests |
| D3 | validation | empty series rejected; all `time_series` arrays must match `gt`'s length | an empty horizon cannot be solved | `internal/api` validator tests |
| D4 | validation | `eta_c`/`eta_d` must be in `(0, 1]` | a zero or above-one efficiency is not physical | `internal/api` validator tests |
| D5 | schedule | peak-profile smoothing: flatter profile at equal-or-better objective | upstream pins only the horizon maximum, so plateau and jagged score alike | `TestConformance` (match-or-beat) |
| D6 | schedule | proven-optimal tie-breaks (preference stage at zero MIP gap) | upstream stops at an approximate LP floor | `TestConformance` (match-or-beat) |
| D7 | schedule | continuity is a real objective under hard cost/peak/preference bounds | never trades money or preference value for a nicer schedule | `TestConformance` (match-or-beat) |

The D1–D4 rows restate the divergences documented in the README; if a row's test path
is wrong, correct it to the actual test file before committing.

## Our additions

Anything we add must be optional with an upstream-safe default and must never change
the shape of an existing response. There are no additions yet. When one is added, give
it a row here.
```

- [ ] **Step 3: Verify the divergence test paths**

Run: `ls internal/api/*_test.go`
Expected: existing test files; fix the "pinned by" column to name the real files (e.g. `internal/api/validate_test.go`), then re-run the grep from Step 1 if changed.

- [ ] **Step 4: Commit**

```bash
git add docs/compatibility.md
git commit -m "docs: add the upstream compatibility policy and divergence registry"
```

---

### Task 4: Link the policy from the README and finish the `optima` rename

**Files:**
- Modify: `README.md`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `docs/compatibility.md` (Task 3).
- Produces: README and Makefile naming consistent with the `optima` module.

- [ ] **Step 1: Rename the project in the README and Makefile**

- `README.md` line 1: `# optimizer-v2` → `# optima`.
- `README.md` lines with the image tag: `-t optimizer-v2 .` → `-t optima .`, and the `podman run … optima` line.
- `Makefile`: `-t optimizer-v2 .` → `-t optima .`.

- [ ] **Step 2: Add the policy link**

Append to the "API mapping to the upstream service" section, or as its own short
"Compatibility policy" line:

```markdown
See [`docs/compatibility.md`](docs/compatibility.md) for the compatibility contract,
how upstream changes are absorbed, and the registry of deliberate divergences.
```

- [ ] **Step 3: Verify no stale references remain**

Run: `grep -rn "optimizer-v2" README.md Makefile`
Expected: no output. (`cmd/optimizer` is a directory name, not the project name — leave it.)

- [ ] **Step 4: Verify the build and tests still pass**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add README.md Makefile
git commit -m "docs: link the compatibility policy and rename to optima"
```

---

## Self-review

- **Spec coverage:** §3 contract + acceptance → Task 3 Step 2; §4.1 lock → Task 1 Step 1; §4.2 vendor target → Task 1 Step 2; §4.3 workflow → Task 2 Step 5; §4.4 policy → Task 3; §4.5 README link → Task 4; rename → Task 4; §5 gate → Task 4 Step 4 + Task 3 Step 1; §6 testing → each task's verify steps; §7 out of scope → not implemented.
- **Review Focus:** 1 → Task 1 Step 4; 2+3 → Task 2 Step 3; 4 → Task 2 Step 4; 5 → Task 1 Step 5.
- **Type/name consistency:** `upstream.lock` keys (`REPO`/`REV`/`DATE`) are consistent across Tasks 1–3; `scripts/upstream-drift.sh` name matches its call site in the workflow; label `upstream-drift` consistent throughout.
- **No placeholders:** every step has an exact command, file, or value.
