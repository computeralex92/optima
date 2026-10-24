// Package settings loads the OPTIMIZER_* environment variables into a Settings
// struct, applying defaults for unset or unparseable values.
package settings

import (
	"os"
	"strconv"
)

// Settings holds the runtime configuration read from the environment.
type Settings struct {
	TimeLimit        *float64 // nil = solve out
	GapAbs           *float64 // nil = proven optimality (default; no MIP gap)
	GapShare         float64  // default 0.0
	PreferenceBudget float64  // default 0.0
	NumThreads       *int     // nil = HiGHS default
	LogSubject       bool     // default false
	DumpRequests     string   // "" = disabled
	DumpSlowRequests string   // "" = disabled

	LLMBaseURL            string // generic OpenAI-compatible base URL
	LLMAPIKey             string // optional for local endpoints
	LLMModel              string
	AzureOpenAIEndpoint   string
	AzureOpenAIKey        string
	AzureOpenAIDeployment string
}

// Load reads OPTIMIZER_* from the environment and applies defaults.
// Unset or unparseable values leave the field at its default; optional
// fields stay nil when unset. The MIP gap is off by default: the cost stage
// solves to proven optimality. Set OPTIMIZER_GAP_ABS (and optionally
// OPTIMIZER_GAP_SHARE) to trade optimality for speed. Upstream defaults to
// 0.01 / 0.0025; we deliberately do not. OPTIMIZER_PROBE_SECONDS is
// intentionally not read: there is no probe stage.
func Load() Settings {
	s := Settings{}
	if v, ok := floatEnv("OPTIMIZER_TIME_LIMIT"); ok {
		s.TimeLimit = &v
	}
	if v, ok := floatEnv("OPTIMIZER_GAP_ABS"); ok {
		s.GapAbs = &v
	}
	if v, ok := floatEnv("OPTIMIZER_GAP_SHARE"); ok {
		s.GapShare = v
	}
	if v, ok := floatEnv("OPTIMIZER_PREFERENCE_BUDGET"); ok {
		s.PreferenceBudget = v
	}
	if v, ok := intEnv("OPTIMIZER_NUM_THREADS"); ok {
		s.NumThreads = &v
	}
	if v, ok := boolEnv("OPTIMIZER_LOG_SUBJECT"); ok {
		s.LogSubject = v
	}
	s.DumpRequests = os.Getenv("OPTIMIZER_DUMP_REQUESTS")
	s.DumpSlowRequests = os.Getenv("OPTIMIZER_DUMP_SLOW_REQUESTS")
	s.LLMBaseURL = os.Getenv("OPTIMIZER_LLM_BASE_URL")
	s.LLMAPIKey = os.Getenv("OPTIMIZER_LLM_API_KEY")
	s.LLMModel = os.Getenv("OPTIMIZER_LLM_MODEL")
	s.AzureOpenAIEndpoint = os.Getenv("OPTIMIZER_AZURE_OPENAI_ENDPOINT")
	s.AzureOpenAIKey = os.Getenv("OPTIMIZER_AZURE_OPENAI_KEY")
	s.AzureOpenAIDeployment = os.Getenv("OPTIMIZER_AZURE_OPENAI_DEPLOYMENT")
	if s.AzureOpenAIDeployment == "" {
		s.AzureOpenAIDeployment = "gpt-6-luna"
	}
	return s
}

func floatEnv(key string) (float64, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func intEnv(key string) (int, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func boolEnv(key string) (bool, bool) {
	v := os.Getenv(key)
	if v == "" {
		return false, false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, false
	}
	return b, true
}
