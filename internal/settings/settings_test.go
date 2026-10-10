package settings

import "testing"

var optimizerEnv = []string{
	"OPTIMIZER_TIME_LIMIT",
	"OPTIMIZER_GAP_ABS",
	"OPTIMIZER_GAP_SHARE",
	"OPTIMIZER_PREFERENCE_BUDGET",
	"OPTIMIZER_NUM_THREADS",
	"OPTIMIZER_LOG_SUBJECT",
	"OPTIMIZER_DUMP_SLOW_REQUESTS",
}

func clearOptimizerEnv(t *testing.T) {
	t.Helper()
	for _, k := range optimizerEnv {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearOptimizerEnv(t)
	t.Setenv("OPTIMIZER_GAP_ABS", "")
	s := Load()
	// No MIP gap by default: the cost stage solves to proven optimality.
	if s.GapAbs != nil {
		t.Fatalf("GapAbs = %v, want nil (proven optimality)", s.GapAbs)
	}
	if s.GapShare != 0 || s.PreferenceBudget != 0 {
		t.Fatalf("defaults wrong: %+v", s)
	}
	if s.TimeLimit != nil || s.NumThreads != nil {
		t.Fatalf("optional defaults should be nil: %+v", s)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearOptimizerEnv(t)
	t.Setenv("OPTIMIZER_TIME_LIMIT", "10")
	t.Setenv("OPTIMIZER_NUM_THREADS", "4")
	t.Setenv("OPTIMIZER_LOG_SUBJECT", "true")
	t.Setenv("OPTIMIZER_GAP_ABS", "0.05")
	t.Setenv("OPTIMIZER_GAP_SHARE", "0.001")
	s := Load()
	if s.GapAbs == nil || *s.GapAbs != 0.05 {
		t.Fatalf("GapAbs = %v", s.GapAbs)
	}
	if s.GapShare != 0.001 {
		t.Fatalf("GapShare = %v", s.GapShare)
	}
	if s.TimeLimit == nil || *s.TimeLimit != 10 {
		t.Fatalf("TimeLimit = %v", s.TimeLimit)
	}
	if s.NumThreads == nil || *s.NumThreads != 4 {
		t.Fatalf("NumThreads = %v", s.NumThreads)
	}
	if !s.LogSubject {
		t.Fatalf("LogSubject = false")
	}
}
