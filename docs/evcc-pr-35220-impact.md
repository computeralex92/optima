# evcc #35220 — impact on optima

Research note, 2026-11-11. Source PR:
<https://github.com/evcc-io/evcc/pull/35220> ("Optimizer: hide explain for
self-hosted optimizer"), merged into `master` as `78ab829` on 2026-10-24.

**Bottom line: no protocol change, no code change needed here.** The PR makes
evcc's web UI hide the "Explain plan" card whenever `OPTIMIZER_URI` points
anywhere but `https://optimizer.evcc.io`. A self-hosted optima is exactly that
case, so from evcc **0.319.0** on, the explain feature this service implements
is unreachable from the evcc UI, even when `OPTIMIZER_LLM_*` is fully
configured. The HTTP endpoint keeps working.

## What the PR changes

Three files, one commit (`7ad62ed`, merged as `78ab829`):

1. `core/site_optimizer.go` — the published optimizer result (`evopt` state)
   gains a boolean:

   ```go
   Hosted  bool  `json:"hosted"` // explain needs the hosted optimizer
   ...
   Hosted:  optimizerURI() == OPTIMIZER_URI,
   ```

   <https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer.go#L54>,
   <https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer.go#L241>.
   `OPTIMIZER_URI` is the literal `"https://optimizer.evcc.io"`
   (<https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer.go#L66>),
   and `optimizerURI()` is `cmp.Or(os.Getenv("OPTIMIZER_URI"), OPTIMIZER_URI)`.
   The flag is plain string equality, nothing else.

2. `assets/js/views/Optimize.vue` — the card is gated on it:

   ```vue
   <PlanExplanation
       v-if="evopt.hosted"
   ```

   <https://github.com/evcc-io/evcc/blob/0.319.0/assets/js/views/Optimize.vue#L72>.
   The card previously rendered unconditionally
   (<https://github.com/evcc-io/evcc/blob/0.318.3/assets/js/views/Optimize.vue#L71>).

3. `assets/js/types/evcc.ts` — `EvOpt` gains `hosted: boolean`
   (<https://github.com/evcc-io/evcc/blob/0.319.0/assets/js/types/evcc.ts#L1551>).

Full diff: <https://github.com/evcc-io/evcc/pull/35220.diff>.

### Why

`/optimize/explain` only exists where the optimizer service is configured with
a chat model (<https://github.com/evcc-io/optimizer> vendored contract:
`openapi.yaml:128-166`, "The endpoint is only available where the service is
configured with a chat model", 503 otherwise). A self-hosted optimizer without
that config answers 503, evcc relays **every** explain error as 502
(<https://github.com/evcc-io/evcc/blob/0.319.0/server/http_optimizer_handler.go#L16-L19>),
and the UI showed a bare "502".

- <https://github.com/evcc-io/optimizer/issues/264> — "Explain plan fails with
  502 when using local optimizer" (evcc 0.318.0, `OPTIMIZER_URI=http://localhost:7050`).
- <https://github.com/evcc-io/evcc/issues/35348> — same report against
  self-hosted, with the suggested fix "hide the explain button as long as a
  self hosted URL is set" — i.e. this PR.

### Versions

| evcc | `hosted` field | explain card for self-hosted |
|---|---|---|
| ≤ 0.318.3 | absent | shown (502 on click) |
| ≥ 0.319.0 (2026-11-08) | present | hidden |

Verified against the tag sources: `core/site_optimizer.go` at `0.318.0`,
`0.318.1`, `0.318.2`, `0.318.3` has no `Hosted` field; `0.319.0` and `master`
do. `0.319.0` was published 2026-11-08
(<https://github.com/evcc-io/evcc/releases/tag/0.319.0>); the commit is in the
`0.318.3...0.319.0` range
(<https://github.com/evcc-io/evcc/compare/0.318.3...0.319.0>).

### Is it breaking for consumers?

- **State / MQTT payload (`evopt`):** additive field only. `hosted` is added to
  the single JSON message published under `evopt`
  (<https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer.go#L46-L60>).
  Consumers that ignore unknown keys are unaffected; strict schema validators
  would need the field. optima does not consume evcc state, so irrelevant here.
- **Explain request body:** the body evcc POSTs to `/optimize/explain` embeds
  the whole published result and therefore now carries `"hosted": false`
  (<https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer_explain.go#L16-L20>,
  `:83`). optima decodes explain requests into a fixed struct with plain
  `json.Unmarshal` (`internal/api/explain.go:18-23`, no
  `DisallowUnknownFields` anywhere in `internal/`), so the extra key is
  ignored.
- **Server-side explain still works for self-hosted.** `optimizerExplainHandler`
  is registered unconditionally
  (<https://github.com/evcc-io/evcc/blob/0.319.0/server/http.go#L335>) and
  `ExplainOptimizerResult` has no `hosted` check
  (<https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer_explain.go#L74-L114>).
  Only the UI gate was added. `POST /api/optimizer/explain` still reaches a
  self-hosted optimizer.

### Companion change in the same release: #35238

<https://github.com/evcc-io/evcc/pull/35238> ("send sponsor token to hosted
optimizer only") landed in 0.319.0 too and is directly relevant: the sponsor
`Authorization: Bearer <token>` header is now attached only when
`optimizerURI() == OPTIMIZER_URI`
(<https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer.go#L124-L129>).
Applied to both the solve and `optimizerPost`
(<https://github.com/evcc-io/evcc/blob/0.319.0/core/site_optimizer_explain.go#L53>).

## What optima is, and how it relates to evcc

`optima` is a Go/HiGHS reimplementation of the `evcc-io/optimizer` service; evcc
talks to it as an HTTP client (`POST /optimize/charge-schedule`,
`/optimize/explain`, `GET /optimize/health`) on port 7050
(`README.md:3-10`, `README.md:103-111`, `docs/compatibility.md:1-20`).

It never reads evcc state, MQTT or the evcc REST API — the dependency is
one-directional: evcc → optima. Its own explain implementation is
`internal/api/explain.go:88` (503 "Plan explanations are not configured" when
no provider is set) and `internal/explain/*` (OpenAI-compatible provider,
divergence A1 in `docs/compatibility.md:79`).

## Impact points in this repo

Nothing fails to compile, no endpoint changes, no response shape changes. The
breakage is in the advertised behaviour and in one observability path.

1. **Explain is unreachable from the evcc UI.** `README.md:9-10` promises "Point
   evcc's `optimizer` URL at this service and it behaves like the original …
   Plan explanations are also served". From evcc 0.319.0 the explain card is
   not rendered for a custom `OPTIMIZER_URI`
   (`Optimize.vue:72`), so neither `/optimize/explain`
   (`internal/api/explain.go:88`) nor the feedback vote
   (`internal/api/explain.go:128`) is ever called from the UI. The endpoint
   itself is unchanged and still answers correctly to a direct POST.
2. **The 503 → 502 path in the PR description is still live for us** whenever a
   client does call explain without `OPTIMIZER_LLM_*` set:
   `internal/api/explain.go:88` returns 503 with "Plan explanations are not
   configured", evcc relays it as 502. Nothing to fix — the PR only removed the
   UI path to it.
3. **`OPTIMIZER_LOG_SUBJECT` loses its input on evcc traffic.** `handlers.go:117`
   reads the JWT subject from `Authorization: Bearer …` to fill
   `solve.subject` (`internal/optimizer/log.go:39`, enabled by
   `internal/settings/settings.go:17,53`). After #35238, evcc ≥ 0.319.0 sends
   no Authorization header to a custom optimizer URI, so the subject is
   silently `""` for every evcc-originated solve. The feature still works for
   other clients that send a token.
4. **Documentation drift.** `docs/compatibility.md:15` correctly keeps
   `/optimize/explain` in the frozen contract (the wire contract is intact), but
   nothing there or in `README.md` records that evcc's UI no longer offers it
   for self-hosted deployments. That is the one edit worth making.

## Not affected

- Charge-schedule request/response, status enum, `objective_value`, defaults:
  untouched by the PR.
- `OPTIMIZER_COARSEN_AFTER` (self-hosted-only knobs,
  `site_optimizer.go:1398-1410`) — unrelated, present before and after.
- Health endpoint, D1–D8 divergences, `TestConformance` acceptance gate.

## Options (no code needed)

- Leave as is and document it: explanations are an evcc-UI feature only when
  `OPTIMIZER_URI` is the hosted service; self-hosted users can still call
  `POST /optimize/explain` (or evcc's `POST /api/optimizer/explain`) directly.
- If UI explanations matter, the only lever available to us is upstream: an
  opt-in such as an extra field in the optimize response or an evcc setting.
  Nothing we can do server-side makes `hosted` true, since it is a local string
  comparison in evcc.

## Verification

- PR diff and metadata: <https://github.com/evcc-io/evcc/pull/35220>,
  <https://github.com/evcc-io/evcc/pull/35220.diff>.
- Per-tag source checks (`Hosted` field, `v-if="evopt.hosted"`) via
  `raw.githubusercontent.com/evcc-io/evcc/{0.318.0,0.318.1,0.318.2,0.318.3,0.319.0,master}/…`.
- Release range: GitHub compare `0.318.3...0.319.0`, release list API.
- Optimizer-side contract: vendored `openapi.yaml:128-166` (upstream rev
  `04f79ca9`, `upstream.lock`).
