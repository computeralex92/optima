# optima

A ground-up rewrite of the [evcc charging optimizer](https://github.com/evcc-io/optimizer)
in Go, backed by [HiGHS](https://highs.dev) instead of PuLP/CBC.

It is a **drop-in replacement** for the Python service next to evcc: the same
optimizer endpoints, the same request/response JSON, the same status semantics.
Point evcc's `optimizer` URL at this service and it behaves like the original,
with better performance and better end-results (see *Improvements* below). Plan
explanations are also served, against any OpenAI-compatible provider.

## Background and motivation

The [evcc charging optimizer](https://github.com/evcc-io/optimizer) plans the
cheapest way to run a home energy system — when to charge batteries and EVs,
when to import from or export to the grid — over a horizon of hours or days.
The original service is Python 3.13 (Flask + PuLP) and solves the resulting
MILP with CBC across up to five solver stages.

Upstream has been moving toward a better solver as well. [Issue #76](https://github.com/evcc-io/optimizer/issues/76)
asked for better handling of binary variables, real multithreading and model
analytics; the answer was HiGHS, first as a selectable solver
([#197](https://github.com/evcc-io/optimizer/pull/197)) and then as a hybrid in
production, with CBC on the joint probe and HiGHS on the split stages
([#199](https://github.com/evcc-io/optimizer/pull/199)). This project follows
that conclusion to its end: HiGHS only, with no PuLP/CBC compatibility layer.

The rewrite is in Go rather than Python: one static binary, native parallelism
and no interpreter in the deployment. It is a drop-in replacement — the same
endpoints, request/response JSON and status semantics, so evcc cannot tell the
difference. The contract stays frozen (see [`docs/compatibility.md`](docs/compatibility.md))
while everything behind it is new. This is an independent, unaffiliated MIT
rewrite of the upstream project.

## Run

Local (needs a cgo/C++ toolchain, see caveats):

```sh
go run ./cmd/optimizer            # listens on :7050
```

Container (needs an OCI engine, podman or docker):

```sh
make docker                       # podman build -t optima .
podman run --rm -p 7050:7050 optima
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

Make targets: `build`, `test`, `run`, `lint`, `docker`, `vendor-upstream`
(`CONTAINER_ENGINE=podman` by default, override as needed). `make vendor-upstream`
refreshes the vendored upstream contract — see [`docs/compatibility.md`](docs/compatibility.md).

## Build caveats

1. **Local builds need a cgo/C++ toolchain.** The optimizer links a static HiGHS
   through the [`gohighs`](https://github.com/bartolsthoorn/gohighs) cgo binding, so
   `CGO_ENABLED=1` and a C++ compiler (`g++` on Linux, the Xcode toolchain on macOS)
   are required. `CGO_ENABLED=0` will not build.
2. **Linux images are built with an OCI engine.** cgo plus static HiGHS means no plain
   `CGO_ENABLED=0` cross-build; the `Dockerfile` builds Linux targets in a
   `golang:1.27-bookworm` builder that installs `g++`, `zlib1g-dev` and
   `ca-certificates` (gohighs links `-lstdc++ … -lz`). The binary is linked fully
   static (`-extldflags "-static"`), so the runtime stage is a `scratch` image: no
   libc, libstdc++ or zlib at runtime, just the ~16 MB binary and the CA bundle
   needed for outbound HTTPS.

## Validator divergences from `openapi.yaml`

The vendored `openapi.yaml` does not pin every numeric edge, and the validator
deliberately diverges from a literal reading in four places. Do not "fix" these
back:

1. Negative `p_N`, `p_E` and `p_a` are accepted: real day-ahead prices and
   export tariffs go negative.
2. `dt` must be `> 0` (a zero or negative step has no meaning in `h[t] = dt/3600`).
3. Empty series arrays are rejected: every `time_series` array needs at least one
   element, and all must match `gt`'s length.
4. `eta_c` / `eta_d` must be in `(0, 1]` (a zero or above-one efficiency is not
   physical).

## API mapping to the upstream service

The vendored `openapi.yaml` is the source of truth. The charge-schedule and health
endpoints match the original 1:1 (same paths, methods, JSON shapes and network port
`7050`).

| Upstream | This service | Notes |
|---|---|---|
| `POST /optimize/charge-schedule` | `POST /optimize/charge-schedule` | body `OptimizationInput` → `OptimizationResult`, identical fields and defaults |
| `GET /optimize/health` | `GET /optimize/health` | `{"status":"healthy","message":"EV Charging MILP API is running"}` |
| `POST /optimize/explain` | `POST /optimize/explain` | body `ExplainInput` → `ExplainResult`; any OpenAI-compatible provider or the Azure fallback |
| `POST /optimize/explain/feedback` | `POST /optimize/explain/feedback` | returns 503; feedback storage is not implemented (D8) |
| `status` values | same | `Optimal, Feasible, Infeasible, Unbounded, Undefined, Not Solved` |
| `objective_value` | same | clean economic value; `null` on non-solved status; all arrays `[]` never `null` |
| env settings | same names plus explain settings | `OPTIMIZER_*` (`TIME_LIMIT`, `GAP_ABS`, `GAP_SHARE`, `PREFERENCE_BUDGET`, `NUM_THREADS`, `DUMP_REQUESTS`, `LLM_*`, …) |

`OPTIMIZER_DUMP_REQUESTS=/path/requests.jsonl` appends every solved request and
its response as one JSONL line for offline replay. It is opt-in and grows without
rotation, so use it for a capture window.

Plan explanations use any OpenAI-compatible chat-completions provider through
`OPTIMIZER_LLM_BASE_URL`, `OPTIMIZER_LLM_API_KEY`, and `OPTIMIZER_LLM_MODEL`. When
the generic base URL is unset, the upstream `OPTIMIZER_AZURE_OPENAI_*` variables are
used. Without either, `/optimize/explain` returns 503.

Plan data is sent to that provider whenever `/optimize/explain` is called. Leave
both the generic and Azure settings unset to keep explanations disabled; the
charge-schedule endpoint never calls an external service.

See [`docs/compatibility.md`](docs/compatibility.md) for the compatibility contract,
how upstream changes are absorbed, and the registry of deliberate divergences.

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
internal/explain  provider config, chat client, plan digest, prompt
internal/optimizer model, objective, solve, result
internal/settings OPTIMIZER_* env
test_cases/*.json vendored golden cases (conformance harness)
```

## License

MIT — see [`LICENSE`](LICENSE). The vendored `openapi.yaml` and `test_cases/`
come from [`evcc-io/optimizer`](https://github.com/evcc-io/optimizer) under the
same license.
