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
	DumpSlowRequests string   // "" = disabled
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
	s.DumpSlowRequests = os.Getenv("OPTIMIZER_DUMP_SLOW_REQUESTS")
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
