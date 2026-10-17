# Explain endpoint with any OpenAI-compatible provider

Status: design, awaiting review before an implementation plan.

## 1. Context

Upstream added `POST /optimize/explain` and `POST /optimize/explain/feedback` to
the evcc optimizer. evcc calls both from `core/site_optimizer_explain.go`, so a
service that returns 404 for them is not a full drop-in replacement.

Upstream generates explanations through Azure OpenAI. A self-hosted service has
no explanation support unless `OPTIMIZER_AZURE_OPENAI_ENDPOINT` and
`OPTIMIZER_AZURE_OPENAI_KEY` are set. Feedback additionally needs an Azure Table
store. Both endpoints return 503 when their configuration is absent.

This design adds explanations through any OpenAI-compatible chat-completions
endpoint, including DeepSeek, OpenAI, OpenRouter, Ollama, and vLLM, while keeping
the upstream Azure configuration as a fallback. It deliberately does not add
feedback storage yet.

## 2. Goals and non-goals

Goals:

- Serve `POST /optimize/explain` with an upstream-compatible request, response,
  and error contract.
- Let an operator select any OpenAI-compatible provider with a base URL, API key,
  and model.
- Keep upstream `OPTIMIZER_AZURE_OPENAI_*` deployments working unchanged.
- Return 503 when explanations are not configured, as upstream does.
- Use only the Go standard library.
- Preserve the upstream plan digest and system prompt so explanation quality does
  not regress.

Non-goals:

- Feedback storage. The feedback route exists and returns 503.
- Streaming, tool calls, structured outputs, retries, or token counting beyond the
  provider's usage response.
- Provider-specific APIs that are not chat-completions compatible.
- Re-validating the charge-schedule request or result inside the explain
  endpoint. The payload is the plan the optimizer produced.
- New dependencies. The module keeps gohighs as its only third-party dependency.

## 3. Configuration

### 3.1 Generic provider

| Variable | Meaning |
|---|---|
| `OPTIMIZER_LLM_BASE_URL` | API base, for example `https://api.deepseek.com/v1`, `https://api.openai.com/v1`, or `http://localhost:11434/v1` |
| `OPTIMIZER_LLM_API_KEY` | API key. Optional, so local endpoints without auth work |
| `OPTIMIZER_LLM_MODEL` | Model name, for example `deepseek-chat` |

Generic mode is selected when `OPTIMIZER_LLM_BASE_URL` is non-empty.

- Explanations are configured only when both the base URL and the model are
  non-empty.
- If the base URL is set but the model is missing, the service is treated as
  unconfigured and returns 503. It does not silently fall back to Azure.
- If the API key is empty, no `Authorization` header is sent.
- The base URL may already end in `/chat/completions`; then it is used unchanged.
  Otherwise `/chat/completions` is appended after trimming one trailing slash.
- `http` is allowed so local endpoints work.

### 3.2 Azure fallback

When `OPTIMIZER_LLM_BASE_URL` is empty, these upstream variables are read:

| Variable | Meaning |
|---|---|
| `OPTIMIZER_AZURE_OPENAI_ENDPOINT` | Azure OpenAI resource endpoint |
| `OPTIMIZER_AZURE_OPENAI_KEY` | Azure OpenAI API key |
| `OPTIMIZER_AZURE_OPENAI_DEPLOYMENT` | Deployment name, default `gpt-6-luna` |

Explanations are configured when both the endpoint and the key are non-empty.
The URL is `<endpoint>/openai/v1/chat/completions` after trimming one trailing
slash from the endpoint, which is the endpoint shape upstream uses. The body and
auth header follow the Azure dialect in section 4.

### 3.3 Settings surface

`internal/settings.Settings` gains six string fields for the raw environment
values. `internal/api` maps them to an `explain.Config`. The `explain` package does
not import `settings`, and `settings` does not import `explain`.

The settings loader treats unset and empty values the same. It does not log or
expose keys.

## 4. Wire protocol

### 4.1 Generic OpenAI-compatible dialect

- `POST <base>/chat/completions`
- Header `Content-Type: application/json`
- Header `Authorization: Bearer <key>` only when the key is non-empty
- Body:

```json
{
  "model": "<OPTIMIZER_LLM_MODEL>",
  "messages": [
    {"role": "system", "content": "<system prompt>"},
    {"role": "user", "content": "Explain this plan.\n\n<digest>"}
  ],
  "max_tokens": 1500
}
```

`max_tokens` is used because it is the most widely accepted completion limit
across compatible providers. `reasoning_effort` is not sent because not every
provider accepts it.

### 4.2 Azure fallback dialect

- `POST <endpoint>/openai/v1/chat/completions`
- Header `Content-Type: application/json`
- Header `api-key: <key>`
- Body:

```json
{
  "model": "<deployment>",
  "messages": [
    {"role": "system", "content": "<system prompt>"},
    {"role": "user", "content": "Explain this plan.\n\n<digest>"}
  ],
  "reasoning_effort": "low",
  "max_completion_tokens": 1500
}
```

This matches the upstream request in `src/optimizer/explain.py`.

### 4.3 Response and errors

- Success: HTTP 200 with `{"explanation": "<markdown>"}`.
- Read `choices[0].message.content`. `usage.total_tokens` is optional and is used
  only in the log line.
- Apply the upstream answer cleanup in this order: replace U+2014 (em dash) with
  `, `, including the spaced form, then replace U+2013 (en dash) with `-`:

```go
text = strings.ReplaceAll(text, " \u2014 ", ", ")
text = strings.ReplaceAll(text, "\u2014", ", ")
text = strings.ReplaceAll(text, "\u2013", "-")
```
- Non-2xx provider response: return 502 with `model returned <code>: <body prefix>`.
  The body prefix is capped at 200 bytes.
- Transport error or timeout: return 502 with `model unreachable: <error>`.
- Missing or malformed answer: return 502 with `model returned no answer`.
- Timeout: 45 seconds, matching upstream's `CHAT_TIMEOUT`.
- HTTP client redirects and TLS verification keep their defaults.

## 5. Explain endpoint

### 5.1 Request types

The handler decodes this payload:

```go
type Input struct {
    Req      optimizer.Input  `json:"req"`
    Res      optimizer.Result `json:"res"`
    Details  Details          `json:"details"`
    Currency string           `json:"currency"`
    Language string           `json:"language"`
}

type Details struct {
    Timestamp      []string        `json:"timestamp"`
    BatteryDetails []BatteryDetail `json:"batteryDetails"`
    DemandDetails  []DemandDetail  `json:"demandDetails"`
}

type BatteryDetail struct {
    Name     string  `json:"name"`
    Title    string  `json:"title"`
    Type     string  `json:"type"`
    Capacity float64 `json:"capacity"` // kWh
}

type DemandDetail struct {
    Type  string `json:"type"`
    Title string `json:"title"`
}
```

`demandDetails` is not in the OpenAPI schema but upstream's digest reads it, so
the handler accepts it. Unknown fields are ignored.

### 5.2 Validation

Mirror upstream's `plan_payload`:

- Missing `req`: 400 with `{"message": "req is a required property", "details": {"req": "required"}}`.
- Missing `res`: 400 with `{"message": "res is a required property", "details": {"res": "required"}}`.
- Missing `res.status`: 400 with `{"message": "res.status is a required property", "details": {"res.status": "required"}}`.
- Missing `res.batteries`: 400 with `{"message": "res.batteries is a required property", "details": {"res.batteries": "required"}}`.
- Malformed JSON: 400 with `{"message": "invalid request body: <error>"}`.
- Body larger than 8 MiB: 400 with a message.

Presence checks for `req`, `res`, `res.status`, and `res.batteries` use the raw
JSON object before the typed decode. A missing key and an explicit `null` are
both treated as missing. An empty `batteries` array is present and is allowed.

An empty `req.batteries` or `req.time_series` is not rejected here. The digest
renders whatever it receives; the request already came from the optimizer.

### 5.3 Flow

1. Reject oversized bodies with `http.MaxBytesReader`.
2. Decode `Input`.
3. Check required fields.
4. If the explain client is unconfigured, return 503 `{"message": "Plan explanations are not configured"}`.
5. Build the user message as `Explain this plan.\n\n` plus the digest.
6. Call the provider with a 45 second context timeout.
7. On success, write `{"explanation": text}` and log one line:

```json
{"explain":{"client":"<User-Agent>","elapsed":1.234,"steps":5,"tokens":42}}
```

8. On provider failure, return 502 with the provider message.

`elapsed` is rounded to milliseconds and `steps` is `len(req.time_series.dt)`.
The log line never includes the prompt, the digest, the plan, or the API key.
The provider URL is operator configuration, never request data, so the handler
does not fetch a caller-supplied URL. The API key is read from the environment
and is never logged.

### 5.4 Digest and prompt

`internal/explain` ports these functions from upstream
`src/optimizer/explain.py` at the vendored revision:

- `step_labels`
- `kwh`
- `price_format` and `PRICE_SUBUNIT`
- `ranges`
- `capacity_wh`
- `battery_line`
- `digest`
- `SYSTEM_PROMPT`

The prompt stays verbatim, including its length and wording. It is behavior, not
configuration. The language line is appended exactly as upstream does:
`Answer in the language with IETF tag <language>.` The default language is `en`;
the default currency is `EUR`. Digest defaults match upstream: `eta_c` and `eta_d`
are 0.95 when absent, and a missing strategy is `no preference`.

Digest details that must match:

- Labels use `details.timestamp` when its length equals `dt`, otherwise cumulative
  `+H:MM` labels from the start. Timestamps are parsed as RFC3339Nano and keep
  their parsed location. The first timestamp's date is "today"; the next day is
  "tomorrow"; later days use the weekday name. If a timestamp cannot be parsed,
  fall back to cumulative labels for the whole plan.
- Battery kind is `vehicle` when `details.batteryDetails[i].type` is `vehicle`,
  otherwise `home battery`. Title order is title, then name, then
  `<kind> <i+1>`.
- Capacity is the first non-zero of `s_capacity`,
  `details.batteryDetails[i].capacity * 1000`, and `s_max`, then 1.
- Facts include capacity, current charge in whole percent and kWh, kept range,
  charge power and optional minimum, discharge power or "never discharges", grid
  charging, grid discharging, `c_active`, every positive `s_goal` with its end
  label, and every positive `p_demand` run.
- Header notes include grid import and export limits, limit violations, demand
  details, and the charging strategy label. Demand type `heating` is rendered as
  "heating"; `unmodelled` as "charging point without vehicle data". The strategy
  labels are `charge_before_export` "fill battery first",
  `attenuate_demand_peaks` "reduce grid import peaks",
  `attenuate_feedin_peaks` "reduce grid export peaks",
  `attenuate_grid_peaks` "reduce grid peaks", and anything else "no preference".
- The table has one row per step and ends with the final label. Prices use the
  currency subunit when known, otherwise the currency code with three decimals.
- Energies in the table are kWh for the step.

## 6. Feedback endpoint

`POST /optimize/explain/feedback` is registered and always returns:

- HTTP 503
- `{"message": "Explanation feedback is not configured"}`

This matches upstream's behavior for a valid vote when no store is configured.
The handler does not parse the body, so it also returns 503 for a malformed vote.
No data is stored.

## 7. Packages and files

New:

- `internal/explain/explain.go`: config, client, request and response types,
  digest, prompt.
- `internal/explain/explain_test.go`: digest and client tests.
- `internal/api/explain.go`: HTTP handlers and logging.
- `internal/api/explain_test.go`: endpoint tests.

Changed:

- `internal/settings/settings.go`: LLM and Azure fields.
- `internal/settings/settings_test.go`: precedence and defaults.
- `internal/api/server.go`: register both routes.
- `README.md`: API mapping and `OPTIMIZER_LLM_*`.
- `docs/compatibility.md`: explain endpoints in the frozen contract, generic
  provider addition, narrowed D8.

No new dependencies.

## 8. Testing

`internal/explain`:

- Step labels with and without timestamps, including the day rollover and the
  extra end label.
- Digest contains the expected battery line, goal, demand, limit, strategy, and
  table rows for a small 2-step plan.
- Currency subunit and unknown-currency price formatting.
- Dash cleanup order.
- Generic client: request path `/chat/completions`, Bearer header, `max_tokens`,
  model and messages, answer parsing, `usage.total_tokens`.
- Generic client with no key: no `Authorization` header.
- Base URL already ending in `/chat/completions` is not doubled.
- Azure client: `/openai/v1/chat/completions`, `api-key`, `max_completion_tokens`,
  `reasoning_effort`.
- Provider 429 returns an error containing the status and body prefix.
- Empty `choices` returns "model returned no answer".
- Timeout and transport errors return "model unreachable: ...".

`internal/settings`:

- Generic variables are read.
- Azure variables are read.
- Generic base URL plus model takes precedence over Azure.
- Generic base URL without a model leaves explanations unconfigured.
- Azure deployment defaults to `gpt-6-luna`.

`internal/api`:

- Unconfigured explain returns 503 with the upstream message.
- Configured explain returns 200 and the explanation against an `httptest`
  provider.
- Missing `req`, `res`, `res.status`, and `res.batteries` return 400.
- Provider failure returns 502.
- Oversized body returns 400.
- Feedback returns 503.

No test uses a real provider or a real API key.

## 9. Compatibility and documentation

`docs/compatibility.md` changes:

- The frozen path list becomes `POST /optimize/charge-schedule`,
  `GET /optimize/health`, `POST /optimize/explain`, and
  `POST /optimize/explain/feedback`.
- The explain request, response, 400, 502, and 503 shapes are frozen.
- The `OPTIMIZER_LLM_*` variables are recorded under "Our additions": optional,
  with Azure fallback, and never changing an existing response.
- D8 is narrowed to: feedback storage is not implemented, so
  `/optimize/explain/feedback` always returns 503 even when feedback environment
  variables are set.

`README.md` changes:

- The API mapping table gains the explain endpoint.
- The environment list gains `OPTIMIZER_LLM_BASE_URL`, `OPTIMIZER_LLM_API_KEY`,
  and `OPTIMIZER_LLM_MODEL`.
- The note about explain endpoints not being served is removed.

## 10. Acceptance

- `go test ./...` passes.
- `go vet ./...` passes.
- An `httptest` provider is enough to prove the generic and Azure dialects, the
  digest, the prompt plumbing, and all error paths.
- The existing charge-schedule conformance suite is unchanged.
