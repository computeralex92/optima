# optimizer-v2

A ground-up rewrite of the [evcc charging optimizer](https://github.com/evcc-io/optimizer)
in Go, backed by [HiGHS](https://highs.dev) instead of PuLP/CBC.

It is a **drop-in replacement** for the Python service next to evcc: the same two
endpoints, the same request/response JSON, the same status semantics. Point evcc's
`optimizer` URL at this service and it behaves like the original — with better
performance and better end-results (see *Improvements* below).

## Run

Local (needs a cgo/C++ toolchain, see caveats):

```sh
go run ./cmd/optimizer            # listens on :7050
```

Container (needs an OCI engine, podman or docker):

```sh
make docker                       # podman build -t optimizer-v2 .
podman run --rm -p 7050:7050 optimizer-v2
```

Check it:

```sh
curl localhost:7050/optimize/health
# {"status":"healthy","message":"EV Charging MILP API is running"}
```

Solve a vendored case and render the schedule:

```sh
go run ./cmd/client test_cases/009-discharge-before-import.json
```

Make targets: `build`, `test`, `run`, `lint`, `docker`
(`CONTAINER_ENGINE=podman` by default, override as needed).

## Build caveats

1. **Local builds need a cgo/C++ toolchain.** The optimizer links a static HiGHS
   through the [`gohighs`](https://github.com/bartolsthoorn/gohighs) cgo binding, so
   `CGO_ENABLED=1` and a C++ compiler (`g++` on Linux, the Xcode toolchain on macOS)
   are required. `CGO_ENABLED=0` will not build.
2. **Linux images are built with an OCI engine.** cgo plus static HiGHS means no plain
   `CGO_ENABLED=0` cross-build; the `Dockerfile` builds Linux targets in a
   `golang:1.27-bookworm` builder that installs `g++` and `zlib1g-dev` (gohighs links
   `-lstdc++ … -lz`). The runtime stage is `debian:bookworm-slim` because HiGHS needs
   glibc and libstdc++ — not `scratch`.

## API mapping to the upstream service

The vendored `openapi.yaml` is the source of truth; both endpoints match the original
1:1 (same paths, methods, JSON shapes and network port `7050`).

| Upstream | This service | Notes |
|---|---|---|
| `POST /optimize/charge-schedule` | `POST /optimize/charge-schedule` | body `OptimizationInput` → `OptimizationResult`, identical fields and defaults |
| `GET /optimize/health` | `GET /optimize/health` | `{"status":"healthy","message":"EV Charging MILP API is running"}` |
| `status` values | same | `Optimal, Feasible, Infeasible, Unbounded, Undefined, Not Solved` |
| `objective_value` | same | clean economic value; `null` on non-solved status; all arrays `[]` never `null` |
| env settings | same names | `OPTIMIZER_*` (`TIME_LIMIT`, `GAP_ABS`, `GAP_SHARE`, `PREFERENCE_BUDGET`, `NUM_THREADS`, …) |

## Improvements over the baseline

- **Peak-profile smoothing.** The original penalizes only the horizon maximum, so once
  a fixed load pins the peak, a plateau and a jagged profile score alike. Here stage B
  minimizes the peak, then stage C minimizes total step-to-step change under the pinned
  peak — flatter profiles without raising the peak.
- **Proven-optimal tie-breaks.** The preference stage runs at a zero MIP gap, so
  cost-neutral strategies get the actual best tie break instead of the original's
  approximate LP floor.
- **Continuity as a real objective.** Charge starts are minimized under hard cost,
  peak and preference bounds, so the result can never trade away money or preference
  value for a nicer schedule.
- **Better performance.** A modern solver, a shorter lexicographic cascade with fewer
  solves than the original's five stages, and native parallelism.

## Layout

```
cmd/optimizer     HTTP service
cmd/client        CLI client
internal/api      types, normalize, validate, handlers, server
internal/optimizer model, objective, solve, result
internal/settings OPTIMIZER_* env
test_cases/*.json vendored golden cases (conformance harness)
```
