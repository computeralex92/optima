package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/computeralex92/optima/internal/optimizer"
)

//go:embed debug.html
var debugHTML []byte

// storedPlan is the last successful solve, kept for the debug page.
// ponytail: one global slot, last write wins; a ring buffer belongs here when
// multiple concurrent clients need their own plans.
type storedPlan struct {
	Req json.RawMessage  `json:"req"`
	Res optimizer.Result `json:"res"`
	At  time.Time        `json:"at"`
}

func (h *handler) rememberPlan(req []byte, res optimizer.Result) {
	h.planMu.Lock()
	h.lastPlan = &storedPlan{Req: json.RawMessage(req), Res: res, At: time.Now()}
	h.planMu.Unlock()
}

// debugPage serves the embedded debug page.
func (h *handler) debugPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(debugHTML)
}

// lastPlanHandler returns the last solved plan, 404 before the first solve.
func (h *handler) lastPlanHandler(w http.ResponseWriter, _ *http.Request) {
	h.planMu.RLock()
	plan := h.lastPlan
	h.planMu.RUnlock()
	if plan == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Message: "no plan solved yet"})
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
