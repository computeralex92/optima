// Package api is the HTTP transport for the optimizer. It holds no solver
// logic: it decodes and validates a request, calls optimizer.Solve, and
// encodes the result.
package api

import (
	"net/http"

	"github.com/computeralex92/optima/internal/explain"
	"github.com/computeralex92/optima/internal/settings"
)

// NewServer builds the optimizer HTTP handler. Routes use Go 1.22 method+path
// patterns, so a wrong method gets a 405 without extra code.
func NewServer(s settings.Settings) http.Handler {
	h := &handler{
		settings: s,
		explainClient: explain.NewClient(explain.Resolve(
			s.LLMBaseURL, s.LLMAPIKey, s.LLMModel,
			s.AzureOpenAIEndpoint, s.AzureOpenAIKey, s.AzureOpenAIDeployment,
		)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /optimize/charge-schedule", h.chargeSchedule)
	mux.HandleFunc("GET /optimize/health", h.health)
	mux.HandleFunc("POST /optimize/explain", h.explainPlan)
	mux.HandleFunc("POST /optimize/explain/feedback", h.explainFeedback)
	return mux
}
