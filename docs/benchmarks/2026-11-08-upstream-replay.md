# Upstream replay benchmark (2026-11-08)

Comparison of `optima` against the official
[`evcc-io/optimizer`](https://github.com/evcc-io/optimizer) on real traffic
captured from the live `optima` instance.

**Question.** On production-shaped requests, where does `optima` beat or lose to
upstream, in solution quality and in latency?

**Answer in one line.** Quality was equal or better everywhere (never worse by
more than solver noise, better on the long-horizon cases), but latency was about
38 % worse on the common small request because every request ran two full solves
where upstream's probe merges them into one. Adding that probe made ours faster
than upstream at mean and p50; see [After the probe](#after-the-probe).

## Setup

Host: macOS arm64, 8 cores, Podman (applehv). Both services on port 7050 in
their containers, published on different host ports:

| Service | Image | Image ID | Host port |
|---|---|---|---|
| official | `docker.io/evcc/optimizer:latest` (built 2026-11-07) | `ec5a5e6497a5` | 17050 |
| ours | `ghcr.io/computeralex92/optima:v1.4.0` (= HEAD `5802640`) | `5f05b5923d4b` | 27050 |

Both ran with the same production-shaped settings:

```sh
podman run -d --name opt-official -p 17050:7050 \
  -e OPTIMIZER_TIME_LIMIT=10 -e OPTIMIZER_NUM_THREADS=1 \
  docker.io/evcc/optimizer:latest
podman run -d --name opt-ours -p 27050:7050 \
  -e OPTIMIZER_TIME_LIMIT=10 -e OPTIMIZER_NUM_THREADS=1 \
  ghcr.io/computeralex92/optima:v1.4.0
```

The gap experiment used a third container on port 27051 with
`OPTIMIZER_GAP_ABS=0.01 OPTIMIZER_GAP_SHARE=0.0025`, upstream's defaults.

The official image is the current upstream release, not the revision pinned in
`upstream.lock` (2026-10-25). It is the hybrid CBC/HiGHS build described in
upstream's README.

## Traffic

Source: `requests.jsonl`, the `OPTIMIZER_DUMP_REQUESTS` capture from the live
instance, 2026-10-24 11:39 to 2026-11-08 09:04 UTC.

- 3,483 requests, all from `evcc` (`0.318`/`0.319` dev builds).
- All `Optimal` in production; no infeasible, unbounded or timed-out requests.
- One strategy everywhere: `charge_before_export` / `discharge_before_import`.
- 2,775 one-battery, 687 two-battery, 21 with a demand rate; no grid limits.
- Mostly short horizons: production mean solve time 72 ms, none above 1 s.

The conclusions below therefore describe the common case. Slow, infeasible and
grid-limited traffic is not represented in this capture.

## Method

`scripts/upstream-benchmark.py` is the harness.

1. **Full replay.** Every captured `request` POSTed to `/optimize/charge-schedule`,
   one complete pass per service (never both at once, so the services do not
   compete for CPU), four requests in flight within each pass. One JSONL result
   line per request.
2. **Sequential bench.** 400 requests sampled evenly across the capture, sent
   one at a time, alternating which service goes first, to measure latency
   without CPU contention.
3. **Gap experiment.** The same sequential bench against the gap-configured
   third container.
4. Stage times were read from our own per-request log (`{"solve": ...}` lines on
   stdout), 4,324 solves in total including the bench runs.

Reproduce:

```sh
# full replay + report
scripts/upstream-benchmark.py replay --source requests.jsonl \
  --official http://localhost:17050 --ours http://localhost:27050 \
  --out /tmp/upstream-replay.jsonl
scripts/upstream-benchmark.py report --results /tmp/upstream-replay.jsonl

# sequential latency + quality, optionally with a third endpoint
scripts/upstream-benchmark.py bench --source requests.jsonl \
  --endpoint official=http://localhost:17050 \
  --endpoint ours=http://localhost:27050 \
  --endpoint ours-gap=http://localhost:27051 --n 400
```

## Quality (before the probe)

All 3,483 requests: both services returned `Optimal`, no transport or solve
errors, no status mismatches.

| Delta (`ours - official`) | Requests |
|---|---|
| ours better by more than 1e-5 | **4** |
| ours worse by more than 1e-5 | **0** |
| equal within 1e-5 | 3,479 |
| absolute delta at most 1e-6 | 2,198 |
| absolute delta 1e-6 to 1e-5 | 1,281 |
| absolute delta above 1e-3 | 4 |

Another 471 rows are flagged "worse" at a 1e-6 tolerance, but all are at or
below 1e-6 absolute — solver noise, not a real loss. Total delta over the whole
capture is **+0.020 currency in our favour**.

The four real wins are all the same family: two batteries, 126–135 steps
(31–34 h horizon), objective around -2.4. Ours is 0.17–0.46 % better there (up
to +0.011 currency) because upstream stops at its 0.01 absolute gap while we
prove to zero.

Live-vs-fresh sanity check: 3,473 of 3,483 objectives are bit-identical; the
other ten differ by about 1e-6. The image reproduces what production served.

## Latency (before the probe)

Sequential, 400 requests, no contention (seconds):

| Endpoint | mean | p50 | p90 | p99 | max |
|---|---|---|---|---|---|
| official | **0.054** | **0.047** | **0.080** | **0.124** | 0.579 |
| ours (gap 0) | 0.075 | 0.057 | 0.144 | 0.354 | 0.603 |
| ours (`GAP_ABS=0.01`) | 0.071 | 0.054 | 0.145 | 0.307 | 0.604 |

Full replay under four-way concurrency tells the same story: official mean
0.072 s / p99 0.272 s, ours mean 0.087 s / p99 0.413 s.

## Root cause (before the probe)

Every request took path `cascade`: a `cost` stage, then a separate
`preference` stage, then `continuity` when needed. Over the 4,324 logged solves:

| Stage | Total | Share | p50 | p90 |
|---|---|---|---|---|
| cost | 177.2 s | 50 % | 26 ms | 97 ms |
| preference | 162.1 s | 46 % | 28 ms | 72 ms |
| continuity | 10.6 s | 3 % | 76 ms | 268 ms |

The preference stage is not idle work: it produced a nonzero preference value on
4,247 of 4,324 requests, so it cannot simply be deleted. The cost is that every
request runs two full solves.

Upstream avoids the second solve on the common path. Its probe optimizes cost
and preferences jointly and takes path `joint` when that proves optimal; only
unproven requests pay the split cost + tie-break. This repository deliberately
dropped that probe stage — see `docs/superpowers/specs/2026-10-10-optimizer-v2-design.md`
("`OPTIMIZER_PROBE_SECONDS` is dropped: there is no probe stage") — and `path` is
always `cascade`. That decision is what costs the 38 %.

The slowest requests (about 0.7 s) are the long-horizon two-battery cases, where
the continuity stage dominates at 0.25–0.35 s. That is the same family where we
win on quality.

## Gap tuning does not fix it

`OPTIMIZER_GAP_ABS=0.01` (upstream's default) buys about 5 % latency
(mean 75 → 71 ms) and loses quality: 95 of 400 requests got worse, total delta
-0.263 currency, worst single loss 0.0099. Since our gap is absolute and these
objectives are small, the permitted gap is a meaningful fraction of the answer.
Keep the default of proven optimality.

## After the probe

The probe stage was implemented on 2026-11-08 (design spec, section 6). The full
replay was rerun with one complete pass per service: all 3,483 requests on our
side took path `joint`, so the probe proved the combined objective inside its 2 s
slice every time and no request paid for a split fallback.

Sequential 400-request bench, seconds:

| Endpoint | mean | p50 | p90 | p99 |
|---|---|---|---|---|
| official | 0.054 | 0.047 | 0.081 | 0.123 |
| ours before | 0.075 | 0.057 | 0.144 | 0.354 |
| ours after | **0.041** | **0.024** | 0.091 | 0.282 |

Mean -45 %, p50 -58 %, p90 -36 %, p99 -20 %. Ours is now faster than official on
mean and p50. The remaining p90/p99 tail is the continuity stage on long-horizon
two-battery requests (97 of 5,487 logged solves, mean 128 ms), the same family
where we win on quality.

Quality: all 3,483 still `Optimal`. Against official, ours is better on 6 and
worse on 9; every loss is at most 3.6e-5 currency, and 3,468 are within 1e-5.
Against the pre-probe implementation, 3,474 objectives are identical and 9 are
lower by exactly 3.56e-5. The joint objective lets the tiny preference terms
trade at most their own magnitude for tie-break value — 0.0036 cents on a request
worth -3 to -4.4 currency. Only the joint path can do that; the split path (peak
strategies, preference budget) stays strictly lexicographic.

## Outcome

Implemented as the probe stage in section 6 of the design spec. It took the
common request from two full solves to one and closed the latency gap to
upstream without giving up the long-horizon quality wins. Tuning the gap,
dropping the preference stage and changing thread counts were rejected: none
addresses the two-solve structure.


## Caveats

- One client, one strategy, 1–2 batteries, no grid limits, all feasible. This is
  the common case, not the tail.
- The official image moved after the vendored revision, so this compares against
  current upstream, not the exact contract snapshot.
- Latency was measured on an 8-core Apple laptop, not on the production host.
  Ratios matter more than absolute milliseconds.
- The "before" full replay originally ran both services at once, which let them
  compete for CPU. Use the sequential 400-request bench for before/after
  comparisons; the replay is there for quality and path counts.
