package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alex/optimizer-v2/internal/optimizer"
	"github.com/alex/optimizer-v2/internal/settings"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what was
// written. The solve line is emitted to stdout, per spec 9.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// caseRequest loads a vendored golden case's raw request bytes and decoded Input.
func caseRequest(t *testing.T, path string) ([]byte, optimizer.Input) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var wrapper struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatal(err)
	}
	var in optimizer.Input
	if err := json.Unmarshal(wrapper.Request, &in); err != nil {
		t.Fatal(err)
	}
	return wrapper.Request, in
}

// postSolve posts raw to a fresh server and returns the recorder.
func postSolve(t *testing.T, s settings.Settings, raw []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/optimize/charge-schedule", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	NewServer(s).ServeHTTP(rec, req)
	return rec
}

// TestSolveLogLineKeys asserts exactly one {"solve": ...} stdout line with the
// documented key set, the request User-Agent as client, positive elapsed, T
// steps and a non-empty stages map.
func TestSolveLogLineKeys(t *testing.T) {
	raw, in := caseRequest(t, "../../test_cases/024-attenuate-demand-peaks.json")

	out := captureStdout(t, func() {
		rec := postSolve(t, settings.Settings{}, raw, map[string]string{"User-Agent": "evcc/0.311.1"})
		if rec.Code != http.StatusOK {
			t.Fatalf("code %d, body %s", rec.Code, rec.Body.String())
		}
	})

	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want exactly one log line, got %d: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], `{"solve"`) {
		t.Fatalf("line does not start with {\"solve\": %q", lines[0])
	}

	var envelope struct {
		Solve map[string]json.RawMessage `json:"solve"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &envelope); err != nil {
		t.Fatalf("line not JSON: %v (%q)", err, lines[0])
	}
	wantKeys := []string{"client", "elapsed", "stages", "path", "preferences", "continuity", "status", "steps"}
	if len(envelope.Solve) != len(wantKeys) {
		t.Fatalf("solve key set %v, want %v", keysOf(envelope.Solve), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := envelope.Solve[k]; !ok {
			t.Fatalf("missing key %q in %v", k, keysOf(envelope.Solve))
		}
	}

	var record struct {
		Client  string             `json:"client"`
		Elapsed float64            `json:"elapsed"`
		Stages  map[string]float64 `json:"stages"`
		Steps   int                `json:"steps"`
	}
	var decoded struct {
		Solve *struct {
			Client  string             `json:"client"`
			Elapsed float64            `json:"elapsed"`
			Stages  map[string]float64 `json:"stages"`
			Steps   int                `json:"steps"`
		} `json:"solve"`
	}
	decoded.Solve = &record
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("decode solve: %v", err)
	}
	if record.Client != "evcc/0.311.1" {
		t.Fatalf("client %q", record.Client)
	}
	if record.Elapsed <= 0 {
		t.Fatalf("elapsed %v, want > 0", record.Elapsed)
	}
	if record.Steps != len(in.Series.DT) {
		t.Fatalf("steps %d, want %d", record.Steps, len(in.Series.DT))
	}
	if len(record.Stages) == 0 {
		t.Fatal("stages is empty")
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// trivialInput is a minimal valid request (2 steps, no strategy, empty battery)
// that solves instantly, so the TimeLimit-0 dump path is exercised cheaply.
func trivialInput(t *testing.T) []byte {
	t.Helper()
	zero := 0.0
	in := optimizer.Input{
		Batteries: []optimizer.Battery{{
			SMin: &zero, SMax: &zero, SInitial: &zero,
			CMin: &zero, CMax: &zero, DMax: &zero, PA: &zero,
		}},
		Series: optimizer.Series{
			DT: []int{3600, 3600},
			GT: []float64{0, 0},
			FT: []float64{0, 0},
			PN: []float64{0.3, 0.3},
			PE: []float64{0, 0},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestSlowRequestsDumped appends one JSONL line per request that ran out its
// time limit (or stopped unproven). TimeLimit 0 makes both POSTs qualify.
func TestSlowRequestsDumped(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "nested", "slow.jsonl")
	zero := 0.0
	s := settings.Settings{DumpSlowRequests: dump, TimeLimit: &zero}
	raw := trivialInput(t)

	for i := 0; i < 2; i++ {
		rec := postSolve(t, s, raw, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: code %d, body %s", i, rec.Code, rec.Body.String())
		}
	}

	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("dump file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 dump lines, got %d: %q", len(lines), string(b))
	}
	for i, l := range lines {
		var entry struct {
			Elapsed float64         `json:"elapsed"`
			Status  string          `json:"status"`
			Request json.RawMessage `json:"request"`
		}
		if err := json.Unmarshal([]byte(l), &entry); err != nil {
			t.Fatalf("line %d not JSON: %v (%q)", i, err, l)
		}
		if entry.Elapsed <= 0 {
			t.Fatalf("line %d: elapsed %v, want > 0", i, entry.Elapsed)
		}
		if entry.Status == "" {
			t.Fatalf("line %d: empty status", i)
		}
		var reqBody map[string]any
		if err := json.Unmarshal(entry.Request, &reqBody); err != nil || reqBody == nil {
			t.Fatalf("line %d: request not a JSON object: %v (%s)", i, err, entry.Request)
		}
	}
}

// signHS256 builds a JWT with the given claims for tests.
func signHS256(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// TestSolveLogSubject: with LogSubject on and a verifiable HS256 token the solve
// line carries sub; with LogSubject off it carries no subject key at all.
func TestSolveLogSubject(t *testing.T) {
	const secret = "test-secret"
	t.Setenv("JWT_TOKEN_SECRET", secret)
	token := signHS256(t, secret, map[string]any{"sub": "alice"})
	raw, _ := caseRequest(t, "../../test_cases/024-attenuate-demand-peaks.json")
	auth := map[string]string{"Authorization": "Bearer " + token}

	subjectLine := captureStdout(t, func() {
		rec := postSolve(t, settings.Settings{LogSubject: true}, raw, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("code %d", rec.Code)
		}
	})
	var envelope struct {
		Solve map[string]json.RawMessage `json:"solve"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(subjectLine)), &envelope); err != nil {
		t.Fatalf("line not JSON: %v (%q)", err, subjectLine)
	}
	var subject string
	if raw, ok := envelope.Solve["subject"]; ok {
		if err := json.Unmarshal(raw, &subject); err != nil {
			t.Fatal(err)
		}
	}
	if subject != "alice" {
		t.Fatalf("subject %q, want alice (line %q)", subject, subjectLine)
	}

	offLine := captureStdout(t, func() {
		postSolve(t, settings.Settings{LogSubject: false}, raw, auth)
	})
	var off struct {
		Solve map[string]json.RawMessage `json:"solve"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(offLine)), &off); err != nil {
		t.Fatalf("line not JSON: %v (%q)", err, offLine)
	}
	if _, ok := off.Solve["subject"]; ok {
		t.Fatalf("subject logged while LogSubject is off: %q", offLine)
	}
}
