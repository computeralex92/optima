package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/alex/optimizer-v2/internal/optimizer"
	"github.com/alex/optimizer-v2/internal/settings"
)

type handler struct {
	settings settings.Settings
}

// healthResponse is the fixed health body (spec section 3). A struct, not a
// map, so the key order matches the contract byte for byte.
type healthResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// errorResponse is the non-2xx body. Message is always present; Details is
// emitted only for a *optimizer.ValidationError.
type errorResponse struct {
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// health answers with the fixed contract body.
func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "healthy",
		Message: "EV Charging MILP API is running",
	})
}

// chargeSchedule decodes and validates an optimizer.Input, solves it, and
// returns the Result. Every path writes a JSON body (never empty): 400 for a
// decode or validation failure, 500 for a solve failure, 200 otherwise. On a
// successful solve it emits exactly one {"solve": {...}} line to stdout and,
// when configured, appends a slow-request line (spec 9).
func (h *handler) chargeSchedule(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Message: "invalid request body: " + err.Error(),
		})
		return
	}
	var in optimizer.Input
	if err := json.Unmarshal(raw, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Message: "invalid request body: " + err.Error(),
		})
		return
	}

	if err := in.Validate(); err != nil {
		var ve *optimizer.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Message: ve.Message,
				Details: ve.Details,
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: err.Error()})
		return
	}

	res, info, err := optimizer.SolveLogged(in, r.UserAgent(), h.settings)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Message: "solve failed: " + err.Error(),
		})
		return
	}

	info.Subject = h.subject(r)
	logSolve(info)
	h.dumpSlow(info, raw)

	writeJSON(w, http.StatusOK, res)
}

// logSolve emits the one structured line per request (spec 9).
func logSolve(info optimizer.SolveInfo) {
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Solve optimizer.SolveInfo `json:"solve"`
	}{Solve: info})
}

// subject returns the JWT sub claim when OPTIMIZER_LOG_SUBJECT is on, the
// request carries a Bearer token and it verifies against JWT_TOKEN_SECRET.
// Otherwise it returns "" (and the raw token is never logged).
func (h *handler) subject(r *http.Request) string {
	if !h.settings.LogSubject {
		return ""
	}
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		return ""
	}
	return jwtSubject(token, os.Getenv("JWT_TOKEN_SECRET"))
}

// bearer extracts the token from an "Authorization: Bearer <jwt>" header.
func bearer(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	return ""
}

// jwtSubject verifies an HS256 JWT and returns its sub claim, or "" on any
// failure. Minimal stdlib implementation scoped to the sub log (spec 9): split
// header.payload.signature, HMAC-SHA256 header.payload with the secret, compare
// against the decoded signature in constant time, then read sub.
func jwtSubject(token, secret string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// dumpSlow appends one JSONL record for a request that stopped unproven
// (Feasible) or used its whole time limit (spec 9). It creates parent
// directories as needed and is a no-op when dumping is disabled or the request
// was not slow.
func (h *handler) dumpSlow(info optimizer.SolveInfo, raw []byte) {
	if h.settings.DumpSlowRequests == "" {
		return
	}
	if info.Status != "Feasible" &&
		!(h.settings.TimeLimit != nil && info.Elapsed.Seconds() >= *h.settings.TimeLimit) {
		return
	}
	if dir := filepath.Dir(h.settings.DumpSlowRequests); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(h.settings.DumpSlowRequests, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(struct {
		Elapsed float64         `json:"elapsed"`
		Status  string          `json:"status"`
		Request json.RawMessage `json:"request"`
	}{Elapsed: info.Elapsed.Seconds(), Status: info.Status, Request: json.RawMessage(raw)})
}

// writeJSON marshals v (no trailing newline, so health is byte-exact) and
// writes it with the status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"message":"response encoding failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
