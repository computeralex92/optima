# Explain Endpoint with OpenAI-Compatible Providers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve `/optimize/explain` through any OpenAI-compatible chat-completions provider, with the upstream Azure configuration as a fallback, and keep upstream-compatible 503 behavior when unconfigured.

**Architecture:** A new `internal/explain` package resolves provider configuration, builds the upstream plan digest, and makes one chat-completions request. `internal/api` gains two routes: explain calls the package and logs one line per request; feedback returns 503 because storage is not implemented.

**Tech Stack:** Go 1.27.2 standard library only (`net/http`, `encoding/json`, `httptest`), existing `internal/optimizer` types.

**Spec:** `docs/superpowers/specs/2026-10-17-explain-openai-compatible-design.md`

## Global Constraints

- No new third-party dependencies. The module keeps gohighs as its only dependency.
- Use the standard library for HTTP and JSON.
- No em dashes in new code, comments, commits, or docs. Use the `\u2014` escape where the em dash character is needed in code.
- Explain request, response, 400, 502, and 503 shapes must match the spec.
- Provider timeout is 45 seconds.
- Explain request body cap is `maxRequestBody` (8 MiB), reusing the existing constant.
- Never log the prompt, digest, plan, or API key.
- The existing charge-schedule and health behavior must not change.
- Conventional Commits. Commits are signed by the configured 1Password SSH key.

## File Structure

New:

- `internal/explain/types.go`: `Input`, `Details`, `BatteryDetail`, `DemandDetail`.
- `internal/explain/config.go`: `Config`, `Resolve`, `Configured`, `chatURL`.
- `internal/explain/client.go`: `Client`, `Usage`, `NewClient`, `Complete`.
- `internal/explain/digest.go`: digest renderer and helpers.
- `internal/explain/prompt.go`: `SystemPrompt`.
- `internal/explain/config_test.go`
- `internal/explain/client_test.go`
- `internal/explain/digest_test.go`
- `internal/api/explain.go`
- `internal/api/explain_test.go`

Modified:

- `internal/settings/settings.go`
- `internal/settings/settings_test.go`
- `internal/api/server.go`
- `README.md`
- `docs/compatibility.md`

The spec names `internal/explain/explain.go`. This plan splits the package into focused files with the same package and behavior. No other design change.

## Review Focus

1. A provider returns HTTP 200 with an empty `choices` array or empty `content`: the service must return 502, never an empty explanation.
2. Generic base URL variants: a trailing slash and a URL already ending in `/chat/completions` must not produce a doubled path.
3. Generic base URL set without a model: the service must be unconfigured and return 503, without silently falling back to Azure.
4. Missing, mismatched, or unparseable timestamps: the digest must fall back to cumulative `+H:MM` labels and still produce a plan.
5. A local provider with no API key: no `Authorization` header is sent and the call still works.

Each line is pinned by a test in the task that owns the code.

---

### Task 1: Settings fields

**Files:**
- Modify: `internal/settings/settings.go`
- Test: `internal/settings/settings_test.go`

**Interfaces:**
- Produces: `Settings.LLMBaseURL`, `Settings.LLMAPIKey`, `Settings.LLMModel`, `Settings.AzureOpenAIEndpoint`, `Settings.AzureOpenAIKey`, `Settings.AzureOpenAIDeployment` (all `string`).
- Produces: `Load` reads the six environment variables and defaults `AzureOpenAIDeployment` to `gpt-6-luna`.

- [ ] **Step 1: Write the failing tests**

Add the six variable names to `optimizerEnv` so `clearOptimizerEnv` clears them.

```go
func TestLoadLLMConfig(t *testing.T) {
	clearOptimizerEnv(t)
	t.Setenv("OPTIMIZER_LLM_BASE_URL", "https://api.deepseek.com/v1")
	t.Setenv("OPTIMIZER_LLM_API_KEY", "secret")
	t.Setenv("OPTIMIZER_LLM_MODEL", "deepseek-chat")
	t.Setenv("OPTIMIZER_AZURE_OPENAI_ENDPOINT", "https://example.openai.azure.com")
	t.Setenv("OPTIMIZER_AZURE_OPENAI_KEY", "azure-secret")
	t.Setenv("OPTIMIZER_AZURE_OPENAI_DEPLOYMENT", "my-deployment")

	s := Load()
	if s.LLMBaseURL != "https://api.deepseek.com/v1" ||
		s.LLMAPIKey != "secret" ||
		s.LLMModel != "deepseek-chat" {
		t.Fatalf("generic LLM config = %+v", s)
	}
	if s.AzureOpenAIEndpoint != "https://example.openai.azure.com" ||
		s.AzureOpenAIKey != "azure-secret" ||
		s.AzureOpenAIDeployment != "my-deployment" {
		t.Fatalf("Azure config = %+v", s)
	}
}

func TestLoadAzureDeploymentDefault(t *testing.T) {
	clearOptimizerEnv(t)
	if got := Load().AzureOpenAIDeployment; got != "gpt-6-luna" {
		t.Fatalf("AzureOpenAIDeployment = %q, want gpt-6-luna", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/settings -run 'TestLoadLLMConfig|TestLoadAzureDeploymentDefault' -v`

Expected: FAIL because the fields do not exist.

- [ ] **Step 3: Implement the fields and loading**

Add the six string fields to `Settings`. In `Load`, read them with `os.Getenv`. After reading `OPTIMIZER_AZURE_OPENAI_DEPLOYMENT`, set it to `gpt-6-luna` when empty.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/settings -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/settings/settings.go internal/settings/settings_test.go
git commit -m "feat: load explain provider settings"
```

---

### Task 2: Provider configuration resolution

**Files:**
- Create: `internal/explain/config.go`
- Test: `internal/explain/config_test.go`

**Interfaces:**
- Produces:

```go
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Azure   bool
}

func Resolve(genericBaseURL, genericAPIKey, genericModel, azureEndpoint, azureAPIKey, azureDeployment string) Config
func (c Config) Configured() bool
func (c Config) chatURL() string
```

- `Resolve` selects generic mode when `genericBaseURL` is non-empty. Otherwise it selects Azure when the endpoint and key are non-empty.
- `Configured` is true when `BaseURL` and `Model` are non-empty and, in Azure mode, `APIKey` is non-empty.
- `chatURL` trims one trailing slash. Generic mode returns the base URL unchanged when it already ends in `/chat/completions`, otherwise appends `/chat/completions`. Azure mode appends `/openai/v1/chat/completions`.

- [ ] **Step 1: Write the failing tests**

```go
func TestResolveGeneric(t *testing.T) {
	c := Resolve("https://api.deepseek.com/v1", "key", "deepseek-chat", "https://azure", "azkey", "deploy")
	if c != (Config{BaseURL: "https://api.deepseek.com/v1", APIKey: "key", Model: "deepseek-chat"}) {
		t.Fatalf("config = %+v", c)
	}
	if !c.Configured() {
		t.Fatal("generic config should be configured")
	}
	if got := c.chatURL(); got != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("chatURL = %q", got)
	}
}

func TestResolveGenericMissingModelDoesNotFallBack(t *testing.T) {
	c := Resolve("https://api.deepseek.com/v1", "", "", "https://azure", "azkey", "deploy")
	if c.Azure {
		t.Fatalf("must not fall back to Azure: %+v", c)
	}
	if c.Configured() {
		t.Fatalf("missing model must be unconfigured: %+v", c)
	}
}

func TestResolveAzureFallback(t *testing.T) {
	c := Resolve("", "", "", "https://example.openai.azure.com/", "azkey", "deploy")
	if !c.Azure || c.Model != "deploy" || c.APIKey != "azkey" {
		t.Fatalf("config = %+v", c)
	}
	if !c.Configured() {
		t.Fatal("Azure config should be configured")
	}
	if got := c.chatURL(); got != "https://example.openai.azure.com/openai/v1/chat/completions" {
		t.Fatalf("chatURL = %q", got)
	}
}

func TestResolveAzureDefaultDeployment(t *testing.T) {
	if got := Resolve("", "", "", "https://azure", "azkey", "").Model; got != "gpt-6-luna" {
		t.Fatalf("model = %q", got)
	}
}

func TestChatURLVariants(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{BaseURL: "https://api.openai.com/v1/"}, "https://api.openai.com/v1/chat/completions"},
		{Config{BaseURL: "https://api.openai.com/v1/chat/completions"}, "https://api.openai.com/v1/chat/completions"},
		{Config{BaseURL: "http://localhost:11434/v1", Model: "x"}, "http://localhost:11434/v1/chat/completions"},
		{Config{BaseURL: "https://azure/", Azure: true}, "https://azure/openai/v1/chat/completions"},
	}
	for _, tc := range cases {
		if got := tc.cfg.chatURL(); got != tc.want {
			t.Errorf("chatURL(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestConfiguredRequiresAzureKey(t *testing.T) {
	if (Config{BaseURL: "https://azure", Model: "deploy", Azure: true}).Configured() {
		t.Fatal("Azure without a key must be unconfigured")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/explain -run 'TestResolve|TestChatURL|TestConfigured' -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement `config.go`**

Use `strings.TrimSuffix` for one trailing slash. Keep generic and Azure URL construction in `chatURL`. Do not add an interface or a provider registry.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/explain -run 'TestResolve|TestChatURL|TestConfigured' -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/explain/config.go internal/explain/config_test.go
git commit -m "feat: resolve OpenAI-compatible explain config"
```

---

### Task 3: Chat-completions client

**Files:**
- Create: `internal/explain/client.go`
- Test: `internal/explain/client_test.go`

**Interfaces:**
- Consumes: `Config` from Task 2.
- Produces:

```go
type Client struct {
	cfg        Config
	httpClient *http.Client
}

func NewClient(cfg Config) *Client
func (c *Client) Configured() bool

type Usage struct {
	TotalTokens int `json:"total_tokens"`
}

func (c *Client) Complete(ctx context.Context, system, user string) (string, Usage, error)
```

- Generic body: `model`, `messages`, `max_tokens: 1500`.
- Azure body: `model`, `messages`, `reasoning_effort: "low"`, `max_completion_tokens: 1500`.
- Generic auth: `Authorization: Bearer <key>` only when the key is non-empty.
- Azure auth: `api-key: <key>`.
- Errors use the exact prefixes `model returned <code>: <body>`, `model unreachable: <error>`, and `model returned no answer`.

- [ ] **Step 1: Write the failing tests**

Use `httptest.NewServer`. Assert the path, auth header, and decoded body fields inside the handler. Return `{"choices":[{"message":{"content":"Sunny \u2014 cheap"}}],"usage":{"total_tokens":42}}` and expect `"Sunny, cheap"`.

```go
func TestCompleteGenericRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "deepseek-chat" {
			t.Errorf("model = %v", body["model"])
		}
		if body["max_tokens"] != float64(1500) {
			t.Errorf("max_tokens = %v", body["max_tokens"])
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"Sunny \u2014 cheap"}}],"usage":{"total_tokens":42}}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "test-key", Model: "deepseek-chat"})
	text, usage, err := c.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if text != "Sunny, cheap" {
		t.Fatalf("text = %q", text)
	}
	if usage.TotalTokens != 42 {
		t.Fatalf("tokens = %d", usage.TotalTokens)
	}
}

func TestCompleteGenericWithoutKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "local-model"})
	if _, _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteGenericFullURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL + "/v1/chat/completions", Model: "m"})
	if _, _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteAzureRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("api-key"); got != "azkey" {
			t.Errorf("api-key = %q", got)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["max_completion_tokens"] != float64(1500) {
			t.Errorf("max_completion_tokens = %v", body["max_completion_tokens"])
		}
		if body["reasoning_effort"] != "low" {
			t.Errorf("reasoning_effort = %v", body["reasoning_effort"])
		}
		if _, ok := body["max_tokens"]; ok {
			t.Error("Azure body must not carry max_tokens")
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "azkey", Model: "deploy", Azure: true})
	if _, _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, "slow down")
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "model returned 429: slow down") {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || err.Error() != "model returned no answer" {
		t.Fatalf("err = %v", err)
	}
}

func TestCompleteTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewClient(Config{BaseURL: url, Model: "m"})
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "model unreachable") {
		t.Fatalf("err = %v", err)
	}
}
```

Add an empty-content test: return `{"choices":[{"message":{"content":"  "}}]}` and expect `model returned no answer`.

Add a timeout test:

```go
func TestCompleteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	c.httpClient = &http.Client{Timeout: 10 * time.Millisecond}
	_, _, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "model unreachable") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/explain -run TestComplete -v`

Expected: FAIL because `NewClient` and `Complete` do not exist.

- [ ] **Step 3: Implement `client.go`**

`NewClient` sets `httpClient: &http.Client{Timeout: 45 * time.Second}`. Build the request with `http.NewRequestWithContext`. Cap an error response body at 200 bytes with `io.LimitReader`. Decode `choices[0].message.content` and `usage.total_tokens`. Trim whitespace before treating content as empty. Apply the three dash replacements in order.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/explain -run TestComplete -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/explain/client.go internal/explain/client_test.go
git commit -m "feat: add OpenAI-compatible chat client"
```

---

### Task 4: Request types, digest, and prompt

**Files:**
- Create: `internal/explain/types.go`
- Create: `internal/explain/digest.go`
- Create: `internal/explain/prompt.go`
- Test: `internal/explain/digest_test.go`

**Interfaces:**
- Consumes: `optimizer.Input`, `optimizer.Result`.
- Produces:

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
	Capacity float64 `json:"capacity"`
}

type DemandDetail struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

const SystemPrompt = "..."
func Digest(in Input) string
```

- [ ] **Step 1: Write the failing digest tests**

Add the local helpers:

```go
func f64(v float64) *float64 { return &v }
func boolp(v bool) *bool    { return &v }
```

Build the upstream fixture from `tests/conftest.py` at `df9cc55`:

```go
func testInput() Input {
	return Input{
		Req: optimizer.Input{
			Batteries: []optimizer.Battery{{
				ChargeFromGrid: boolp(true),
				SMin:           f64(0), SMax: f64(10000), SInitial: f64(5000),
				CMin: f64(0), CMax: f64(5000), DMax: f64(5000), PA: f64(0.0002),
			}},
			Series: optimizer.Series{
				DT: []int{900, 900, 900, 900, 3600},
				GT: []float64{250, 250, 250, 250, 1000},
				FT: []float64{0, 0, 0, 0, 2000},
				PN: []float64{0.0003, 0.0003, 0.0001, 0.0001, 0.0002},
				PE: []float64{0.0001, 0.0001, 0.0001, 0.0001, 0.0001},
			},
		},
		Res: optimizer.Result{
			Status: "Optimal",
			Batteries: []optimizer.BatteryResult{{
				ChargingPower:    []float64{0, 0, 1250, 1250, 0},
				DischargingPower: []float64{250, 250, 0, 0, 0},
				StateOfCharge:    []float64{4750, 4500, 5750, 7000, 7000},
			}},
			GridImport: []float64{0, 0, 1500, 1500, 0},
			GridExport: []float64{0, 0, 0, 0, 1000},
		},
		Details: Details{
			Timestamp: []string{
				"2026-10-15T10:00:00+02:00", "2026-10-15T10:15:00+02:00",
				"2026-10-15T10:30:00+02:00", "2026-10-15T10:45:00+02:00",
				"2026-10-15T11:00:00+02:00",
			},
			BatteryDetails: []BatteryDetail{{Name: "bat", Title: "Home battery", Type: "battery", Capacity: 10}},
		},
		Currency: "EUR",
		Language: "de",
	}
}
```

Assert these exact substrings:

```go
text := Digest(testInput())
want := []string{
	"Now: today 10:00. The plan runs until today 12:00. Status Optimal.",
	"Prices in ct/kWh, energies in kWh. Charging and discharging efficiency 0.95 and 0.95.",
	`home battery "Home battery": capacity 10.0 kWh, now 50% (5.0 kWh), kept between 0% and 100%, charges up to 5.0 kW, discharges up to 5.0 kW, may charge from the grid, does not discharge into the grid`,
	"today 10:30 | 15 | 10.0 | 10.0 | 0.0 | 0.2 | 1.5/0.0 | 1.2/0.0/57",
	"today 11:00 | 60 | 20.0 | 10.0 | 2.0 | 1.0 | 0.0/1.0 | 0.0/0.0/70",
}
for _, w := range want {
	if !strings.Contains(text, w) {
		t.Errorf("digest missing %q", w)
	}
}
```

Add these tests:

- `TestStepLabelsWithoutTimestamps`: `DT` `[900,900,900,900,3600]` gives `["+0:00","+0:15","+0:30","+0:45","+1:00","+2:00"]`.
- `TestStepLabelsWithTimestamps`: timestamps `2026-10-15T23:45:00.123456789+02:00`, `2026-10-16T00:00:00+02:00`, `2026-10-17T10:00:00+02:00` with `DT` `[900,900,3600]` gives `["today 23:45","tomorrow 00:00","Saturday 10:00","Saturday 11:00"]`.
- `TestStepLabelsInvalidTimestampFallsBack`: replace the first timestamp with `"not-a-time"` and assert the first label is `"+0:00"`.
- `TestDigestGoalsDemandsAndLimits`: start from `testInput()` and apply this mutation:

```go
in := testInput()
b := &in.Req.Batteries[0]
b.DMax = f64(0)
b.CMin = f64(1400)
b.CActive = boolp(true)
b.SMax = f64(8000)
b.SGoal = []float64{0, 0, 0, 8000, 0}
b.PDemand = []float64{350, 350, 0, 0, 1000}
in.Details.BatteryDetails[0] = BatteryDetail{Title: "Carport (i3s)", Type: "vehicle", Capacity: 10}
in.Details.DemandDetails = []DemandDetail{{Type: "home"}, {Type: "heating", Title: "Heat pump"}}
in.Req.Grid.PmaxImp = f64(11000)
in.Res.LimitViolations.GridImportLimitExceeded = true
in.Currency = "GBP"
```

Assert these substrings:

```text
vehicle "Carport (i3s)": capacity 10.0 kWh, now 50% (5.0 kWh), kept between 0% and 80%, charges up to 5.0 kW, at least 1.4 kW or not at all, never discharges, may charge from the grid, charging right now, charging goal 80% by today 11:00, must charge today 10:00 to today 10:30, must charge today 11:00 to today 12:00
grid import limited to 11.0 kW.
the plan needs more grid import than the limit allows.
The household load includes heating "Heat pump".
Prices in p/kWh
```
- `TestDigestUnknownCurrency`: set `Currency` to `HUF` and assert a row starts with `today 10:00 | 15 | 0.300 | 0.100`.
- `TestDigestCapacityFallback`: set `BatteryDetails` to nil and assert `home battery "home battery 1": capacity 10.0 kWh`.
- `TestSystemPrompt`: assert it contains `You explain an energy plan` and `Never use dashes as punctuation`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/explain -run 'TestStepLabels|TestDigest|TestSystemPrompt' -v`

Expected: FAIL because `Digest` and `SystemPrompt` do not exist.

- [ ] **Step 3: Add the prompt**

Copy the `SYSTEM_PROMPT` value from upstream `src/optimizer/explain.py` at `df9cc5563f7aee52f46400af78a32595485f1892` into `prompt.go` as `const SystemPrompt`. Keep the wording verbatim.

```bash
tmp=$(mktemp -d)
git init -q "$tmp"
git -C "$tmp" remote add origin https://github.com/evcc-io/optimizer
git -C "$tmp" fetch -q --depth 1 origin df9cc5563f7aee52f46400af78a32595485f1892
git -C "$tmp" show FETCH_HEAD:src/optimizer/explain.py
```

The prompt contains two backticks around `- **<name>**: <one sentence>`, so a single Go raw string literal will not compile. Use an interpreted string with `\n`, or concatenate raw strings around that fragment.

The upstream source uses backslash line continuations inside the triple-quoted string. Those backslashes are not part of the value. Join those lines with the space before the backslash and no newline.

- [ ] **Step 4: Implement `types.go` and `digest.go`**

Port `step_labels`, `kwh`, `price_format`, `PRICE_SUBUNIT`, `ranges`, `capacity_wh`, `battery_line`, and `digest` from the same upstream file. Match the upstream output exactly, including the table header and row separators.

`stepLabels` uses timestamps only when their length equals `len(dt)`. Parse with `time.RFC3339Nano`. If any parse fails, fall back to cumulative labels for the whole plan. The end label for the timestamp path is the last timestamp plus the last `dt` in seconds.

`Digest` defaults `Currency` to `EUR` and uses `eta_c`/`eta_d` 0.95 when absent. A missing strategy is `no preference`. Format efficiency values with a Python-like float formatter so `1.0` renders as `1.0`, not `1`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/explain -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/explain/types.go internal/explain/digest.go internal/explain/prompt.go internal/explain/digest_test.go
git commit -m "feat: add explain plan digest and prompt"
```

---

### Task 5: API routes and handlers

**Files:**
- Create: `internal/api/explain.go`
- Create: `internal/api/explain_test.go`
- Modify: `internal/api/handlers.go` (add the client field to `handler`)
- Modify: `internal/api/server.go`

**Interfaces:**
- Consumes: `explain.Resolve`, `explain.NewClient`, `explain.Digest`, `explain.SystemPrompt`, `explain.Input`.
- Produces: `POST /optimize/explain` and `POST /optimize/explain/feedback` routes.
- Produces: `handler.explainClient *explain.Client`.
- Produces: `explainRequest` raw decode type in `internal/api/explain.go`.

- [ ] **Step 1: Write the failing API tests**

Use a small valid payload helper:

```go
const validExplainBody = `{
  "req": {
    "batteries": [{"s_min":0,"s_max":10000,"s_initial":5000,"c_min":0,"c_max":5000,"d_max":5000,"p_a":0.1}],
    "time_series": {"dt":[3600],"gt":[1000],"ft":[2000],"p_N":[0.3],"p_E":[0.1]}
  },
  "res": {
    "status": "Optimal",
    "batteries": [{"charging_power":[0],"discharging_power":[0],"state_of_charge":[5000]}],
    "grid_import": [0],
    "grid_export": [1000]
  },
  "details": {"timestamp": ["2026-10-17T10:00:00+02:00"]},
  "currency": "EUR",
  "language": "en"
}`
```

Tests:

- `TestExplainUnconfigured`: `NewServer(settings.Settings{})` and valid body gives 503 with message `Plan explanations are not configured`.
- `TestExplainReturnsExplanation`: fake provider returns `{"choices":[{"message":{"content":"because"}}]}`. Set `settings.Settings{LLMBaseURL: srv.URL, LLMModel: "test-model"}`. Expect 200 and `{"explanation":"because"}`.
- `TestExplainMissingFields`: table over raw JSON with `req`, `res`, `res.status`, and `res.batteries` removed. Expect 400 and the matching `message`.
- `TestExplainProviderFailure`: fake provider returns 429 with `slow down`. Expect 502 and message containing `model returned 429: slow down`.
- `TestExplainOversizedBody`: body of `maxRequestBody + 1` bytes gives 400.
- `TestExplainFeedbackUnconfigured`: POST valid body gives 503 with `Explanation feedback is not configured`.
- `TestExplainLogsOneLine`: redirect `os.Stdout` through `os.Pipe`, call the configured handler, and assert the captured line is one JSON object with an `explain` object carrying `client`, `elapsed`, `steps`, and `tokens`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api -run TestExplain -v`

Expected: FAIL with 404 for the new routes.

- [ ] **Step 3: Implement `internal/api/explain.go`**

The handler reads the body with `http.MaxBytesReader(w, r.Body, maxRequestBody)`. Decode into this raw struct:

```go
type explainRequest struct {
	Req      json.RawMessage  `json:"req"`
	Res      json.RawMessage  `json:"res"`
	Details  explain.Details  `json:"details"`
	Currency string           `json:"currency"`
	Language string           `json:"language"`
}
```

Presence checks use the raw JSON before typed decoding. A missing key and an explicit `null` are both missing. Unmarshal `res` into `map[string]json.RawMessage` and require `status` and `batteries`. Then unmarshal into `optimizer.Input` and `optimizer.Result`.

If the client is unconfigured, return 503. Otherwise build the digest, append the language line, call `Complete`, write the explanation, and emit the log line.

The language defaults to `en`. The system message is `explain.SystemPrompt + "\nAnswer in the language with IETF tag " + language + "."`.

Use `writeJSON` and `errorResponse` from `handlers.go`. Return 400 for decode and required-field failures, 502 for provider failures, and 503 for unconfigured and feedback.

`explainFeedback` does not read the body and always returns 503.

- [ ] **Step 4: Wire the routes and client**

Add `explainClient *explain.Client` to the `handler` struct in `handlers.go`. In `NewServer`, construct `explain.NewClient(explain.Resolve(...))` from the six settings fields and store it on `handler`. Register:

```go
mux.HandleFunc("POST /optimize/explain", h.explainPlan)
mux.HandleFunc("POST /optimize/explain/feedback", h.explainFeedback)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/api -run TestExplain -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/explain.go internal/api/explain_test.go internal/api/server.go
git commit -m "feat: serve explain endpoint"
```

---

### Task 6: Documentation and compatibility registry

**Files:**
- Modify: `README.md`
- Modify: `docs/compatibility.md`

**Interfaces:**
- Consumes: the final endpoint and configuration behavior from Tasks 1-5.

- [ ] **Step 1: Update `README.md`**

- Add `POST /optimize/explain` to the API mapping table.
- Add `POST /optimize/explain/feedback` with the 503-only note.
- Add `OPTIMIZER_LLM_BASE_URL`, `OPTIMIZER_LLM_API_KEY`, and `OPTIMIZER_LLM_MODEL` to the environment list.
- Remove the note that the explain endpoints are not served.
- Keep the two-endpoint wording only where it describes evcc's optimizer calls, not the whole API.

- [ ] **Step 2: Update `docs/compatibility.md`**

- Add `POST /optimize/explain` and `POST /optimize/explain/feedback` to the frozen path list.
- Add `OPTIMIZER_LLM_*` under "Our additions" as optional variables with the Azure fallback.
- Narrow D8 to: feedback storage is not implemented, so the feedback route always returns 503 even when feedback environment variables are set.

- [ ] **Step 3: Run the full verification**

Run:

```bash
go test ./...
go vet ./...
git diff --check
```

Expected: all tests pass, vet is clean, and `git diff --check` reports nothing.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/compatibility.md
git commit -m "docs: document explain endpoint and provider settings"
```

---

## Final Verification

- [ ] `go test ./...` passes.
- [ ] `go vet ./...` passes.
- [ ] `git diff --check` reports nothing.
- [ ] `git log --oneline` shows the six task commits.
- [ ] No new dependency appears in `go.mod`.
- [ ] The charge-schedule conformance tests still pass.
