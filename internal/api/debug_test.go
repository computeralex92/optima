package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/computeralex92/optima/internal/optimizer"
	"github.com/computeralex92/optima/internal/settings"
)

func TestDebugPageServes(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/debug", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "Debug Page") {
		t.Fatal("body does not contain the page title")
	}
}

func TestLastPlanEmpty(t *testing.T) {
	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/last-plan", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestLastPlanAfterSolve(t *testing.T) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validExplainBody), &payload); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(settings.Settings{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/optimize/charge-schedule", bytes.NewReader(payload["req"]))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("solve code = %d, body %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/last-plan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("last-plan code = %d, body %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Req optimizer.Input  `json:"req"`
		Res optimizer.Result `json:"res"`
		At  string           `json:"at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Req.Series.DT) != 1 {
		t.Fatalf("req steps = %d, want 1", len(got.Req.Series.DT))
	}
	if got.Res.Status == "" || len(got.Res.Batteries) != 1 {
		t.Fatalf("res = %+v", got.Res)
	}
	if got.At == "" {
		t.Fatal("missing at")
	}
}
