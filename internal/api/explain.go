package api

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/computeralex92/optima/internal/explain"
	"github.com/computeralex92/optima/internal/optimizer"
)

// explainRequest keeps req and res raw so required fields can be checked
// before the typed decode turns a missing key into a zero value.
type explainRequest struct {
	Req      json.RawMessage `json:"req"`
	Res      json.RawMessage `json:"res"`
	Details  explain.Details `json:"details"`
	Currency string          `json:"currency"`
	Language string          `json:"language"`
}

// explainPlan implements POST /optimize/explain. It validates the plan
// envelope, then asks the configured provider to explain the digest.
func (h *handler) explainPlan(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "invalid request body: " + err.Error()})
		return
	}

	var req explainRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "invalid request body: " + err.Error()})
		return
	}
	if !present(req.Req) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Message: "req is a required property",
			Details: map[string]string{"req": "required"},
		})
		return
	}
	if !present(req.Res) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Message: "res is a required property",
			Details: map[string]string{"res": "required"},
		})
		return
	}

	var resFields map[string]json.RawMessage
	if err := json.Unmarshal(req.Res, &resFields); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "invalid request body: " + err.Error()})
		return
	}
	if !present(resFields["status"]) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Message: "res.status is a required property",
			Details: map[string]string{"res.status": "required"},
		})
		return
	}
	if !present(resFields["batteries"]) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Message: "res.batteries is a required property",
			Details: map[string]string{"res.batteries": "required"},
		})
		return
	}

	var in optimizer.Input
	if err := json.Unmarshal(req.Req, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "invalid request body: " + err.Error()})
		return
	}
	var res optimizer.Result
	if err := json.Unmarshal(req.Res, &res); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Message: "invalid request body: " + err.Error()})
		return
	}

	if !h.explainClient.Configured() {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Message: "Plan explanations are not configured"})
		return
	}

	language := req.Language
	if language == "" {
		language = "en"
	}
	system := explain.SystemPrompt + "\nAnswer in the language with IETF tag " + language + "."

	payload := explain.Input{
		Req:      in,
		Res:      res,
		Details:  req.Details,
		Currency: req.Currency,
		Language: language,
	}

	start := time.Now()
	user := "Explain this plan.\n\n" + explain.Digest(payload)
	text, usage, err := h.explainClient.Complete(r.Context(), system, user)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Message: err.Error()})
		return
	}

	logExplain(explainLog{
		Client:  r.UserAgent(),
		Elapsed: math.Round(time.Since(start).Seconds()*1000) / 1000,
		Steps:   len(in.Series.DT),
		Tokens:  usage.TotalTokens,
	})
	writeJSON(w, http.StatusOK, struct {
		Explanation string `json:"explanation"`
	}{Explanation: text})
}

// explainFeedback always reports the upstream unconfigured behavior. Feedback
// storage is not implemented.
func (h *handler) explainFeedback(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusServiceUnavailable, errorResponse{Message: "Explanation feedback is not configured"})
}

type explainLog struct {
	Client  string  `json:"client"`
	Elapsed float64 `json:"elapsed"`
	Steps   int     `json:"steps"`
	Tokens  int     `json:"tokens"`
}

func logExplain(info explainLog) {
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Explain explainLog `json:"explain"`
	}{Explain: info})
}

func present(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
