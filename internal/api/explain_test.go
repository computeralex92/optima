package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/computeralex92/optima/internal/settings"
)

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

func TestExplainUnconfigured(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/optimize/explain", strings.NewReader(validExplainBody)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Message != "Plan explanations are not configured" {
		t.Fatalf("message = %q", body.Message)
	}
}

func TestExplainReturnsExplanation(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("provider path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"because"}}],"usage":{"total_tokens":7}}`)
	}))
	defer provider.Close()

	srv := NewServer(settings.Settings{LLMBaseURL: provider.URL, LLMModel: "test-model"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/optimize/explain", strings.NewReader(validExplainBody)))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Explanation string `json:"explanation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Explanation != "because" {
		t.Fatalf("explanation = %q", body.Explanation)
	}
}

func TestExplainMissingFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"missing req", func(p map[string]any) { delete(p, "req") }, "req is a required property"},
		{"missing res", func(p map[string]any) { delete(p, "res") }, "res is a required property"},
		{"missing status", func(p map[string]any) {
			delete(p["res"].(map[string]any), "status")
		}, "res.status is a required property"},
		{"missing batteries", func(p map[string]any) {
			delete(p["res"].(map[string]any), "batteries")
		}, "res.batteries is a required property"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal([]byte(validExplainBody), &payload); err != nil {
				t.Fatal(err)
			}
			tc.mutate(payload)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}

			srv := NewServer(settings.Settings{})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest("POST", "/optimize/explain", bytes.NewReader(raw)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, body %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Message != tc.want {
				t.Fatalf("message = %q, want %q", body.Message, tc.want)
			}
		})
	}
}

func TestExplainProviderFailure(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, "slow down")
	}))
	defer provider.Close()

	srv := NewServer(settings.Settings{LLMBaseURL: provider.URL, LLMModel: "test-model"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/optimize/explain", strings.NewReader(validExplainBody)))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Message, "model returned 429: slow down") {
		t.Fatalf("message = %q", body.Message)
	}
}

func TestExplainOversizedBody(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/optimize/explain", bytes.NewReader(make([]byte, maxRequestBody+1))))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestExplainFeedbackUnconfigured(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/optimize/explain/feedback", strings.NewReader(validExplainBody)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Message != "Explanation feedback is not configured" {
		t.Fatalf("message = %q", body.Message)
	}
}

func TestExplainLogsOneLine(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"because"}}],"usage":{"total_tokens":7}}`)
	}))
	defer provider.Close()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	srv := NewServer(settings.Settings{LLMBaseURL: provider.URL, LLMModel: "test-model"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/optimize/explain", strings.NewReader(validExplainBody))
	req.Header.Set("User-Agent", "test-client")
	srv.ServeHTTP(rec, req)

	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	var line struct {
		Explain struct {
			Client  string  `json:"client"`
			Elapsed float64 `json:"elapsed"`
			Steps   int     `json:"steps"`
			Tokens  int     `json:"tokens"`
		} `json:"explain"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &line); err != nil {
		t.Fatalf("log line %q: %v", out, err)
	}
	if line.Explain.Client != "test-client" {
		t.Fatalf("client = %q", line.Explain.Client)
	}
	if line.Explain.Steps != 1 || line.Explain.Tokens != 7 {
		t.Fatalf("steps/tokens = %d/%d", line.Explain.Steps, line.Explain.Tokens)
	}
	if line.Explain.Elapsed < 0 {
		t.Fatalf("elapsed = %v", line.Explain.Elapsed)
	}
}
