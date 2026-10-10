package api

import (
	"encoding/json"
	"errors"
	"net/http"

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
// decode or validation failure, 500 for a solve failure, 200 otherwise.
func (h *handler) chargeSchedule(w http.ResponseWriter, r *http.Request) {
	var in optimizer.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
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

	res, err := optimizer.Solve(in, h.settings)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Message: "solve failed: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
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
