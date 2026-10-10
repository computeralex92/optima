package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/alex/optimizer-v2/internal/optimizer"
	"github.com/alex/optimizer-v2/internal/settings"
)

func TestHealth(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/optimize/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["status"] != "healthy" {
		t.Fatalf("status %q, body %v", body["status"], body)
	}
	if body["message"] != "EV Charging MILP API is running" {
		t.Fatalf("message %q", body["message"])
	}
}

// TestChargeScheduleRejectsBadInput posts a request whose p_N is shorter than
// gt; the handler must answer 400 with a non-empty JSON body carrying a
// "message" key (and the per-field details).
func TestChargeScheduleRejectsBadInput(t *testing.T) {
	pmax := 0.0
	zero := 0.0
	in := optimizer.Input{
		Batteries: []optimizer.Battery{{
			SMin: &zero, SMax: &pmax, SInitial: &zero,
			CMin: &zero, CMax: &pmax, DMax: &zero, PA: &zero,
		}},
		Series: optimizer.Series{
			DT: []int{3600, 3600},
			GT: []float64{0, 0},
			FT: []float64{0, 0},
			PN: []float64{0.3}, // mismatched: length 1, gt length 2
			PE: []float64{0, 0},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/optimize/charge-schedule", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty body")
	}
	var body struct {
		Message string            `json:"message"`
		Details map[string]string `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, rec.Body.String())
	}
	if body.Message == "" {
		t.Fatalf("missing message key: %q", rec.Body.String())
	}
	if len(body.Details) == 0 {
		t.Fatalf("missing details: %q", rec.Body.String())
	}
}

// TestChargeScheduleRejectsOversizedBody pins the http.MaxBytesReader cap: a
// body larger than maxRequestBody is rejected with 400, never buffered whole.
func TestChargeScheduleRejectsOversizedBody(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/optimize/charge-schedule",
		bytes.NewReader(make([]byte, maxRequestBody+1)))
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400 (body cap)", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty body")
	}
}

// TestChargeScheduleSolves posts the golden case 024 request and expects a 200
// with an Optimal status and a non-null objective_value.
func TestChargeScheduleSolves(t *testing.T) {
	raw, err := os.ReadFile("../../test_cases/024-attenuate-demand-peaks.json")
	if err != nil {
		t.Fatal(err)
	}
	var wrapper struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/optimize/charge-schedule", bytes.NewReader(wrapper.Request))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d, body %s", rec.Code, rec.Body.String())
	}
	var got optimizer.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, rec.Body.String())
	}
	if got.Status != "Optimal" {
		t.Fatalf("status %q", got.Status)
	}
	if got.ObjectiveValue == nil {
		t.Fatal("objective_value is null")
	}
}
