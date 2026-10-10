package optimizer

import (
	"testing"

	"github.com/alex/optimizer-v2/internal/settings"
)

// TestSolveLoggedMetadata pins the observable seam: SolveLogged returns the
// Result plus the cascade metadata the handler renders, while Solve stays a
// thin wrapper with the same Result.
func TestSolveLoggedMetadata(t *testing.T) {
	in := loadCase(t, "../../test_cases/024-attenuate-demand-peaks.json")

	res, info, err := SolveLogged(in, "evcc/0.311.1", settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Client != "evcc/0.311.1" {
		t.Fatalf("client %q", info.Client)
	}
	if info.Elapsed <= 0 {
		t.Fatalf("elapsed %v, want > 0", info.Elapsed)
	}
	if info.Path != "cascade" {
		t.Fatalf("path %q, want cascade", info.Path)
	}
	if info.Status != res.Status {
		t.Fatalf("info status %q != result status %q", info.Status, res.Status)
	}
	if info.Steps != len(in.Series.DT) {
		t.Fatalf("steps %d, want %d", info.Steps, len(in.Series.DT))
	}
	if len(info.Stages) == 0 {
		t.Fatal("stages is empty; the cost stage always runs")
	}
	for name, secs := range info.Stages {
		if secs < 0 {
			t.Fatalf("stage %q has negative time %v", name, secs)
		}
	}

	res2, err := Solve(in, settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != res.Status || res2.ObjectiveValue == nil || res.ObjectiveValue == nil ||
		*res2.ObjectiveValue != *res.ObjectiveValue {
		t.Fatalf("Solve wrapper diverged: %+v vs %+v", res2, res)
	}
}
