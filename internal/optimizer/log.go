package optimizer

import (
	"encoding/json"
	"time"

	"github.com/computeralex92/optima/internal/settings"
)

// SolveInfo is the per-request observability metadata (spec 9). The cascade
// fills everything but Client and Subject, which the API layer knows from the
// request (User-Agent and the verified JWT sub). It marshals as the body of the
// {"solve": {...}} log line; Elapsed is rendered in seconds.
type SolveInfo struct {
	Client      string
	Elapsed     time.Duration
	Stages      map[string]float64
	Path        string
	Preferences string
	Continuity  string
	Status      string
	Steps       int
	Subject     string
}

// MarshalJSON renders the solve record with elapsed in seconds (spec 9), not the
// Duration's nanosecond integer. subject is omitted when empty so the key set
// stays stable.
func (s SolveInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Client      string             `json:"client"`
		Elapsed     float64            `json:"elapsed"`
		Stages      map[string]float64 `json:"stages"`
		Path        string             `json:"path"`
		Preferences string             `json:"preferences"`
		Continuity  string             `json:"continuity"`
		Status      string             `json:"status"`
		Steps       int                `json:"steps"`
		Subject     string             `json:"subject,omitempty"`
	}{
		Client:      s.Client,
		Elapsed:     s.Elapsed.Seconds(),
		Stages:      s.Stages,
		Path:        s.Path,
		Preferences: s.Preferences,
		Continuity:  s.Continuity,
		Status:      s.Status,
		Steps:       s.Steps,
		Subject:     s.Subject,
	})
}

// SolveLogged is Solve plus the per-request cascade metadata. Elapsed covers
// normalize, model build and the whole cascade. Solve is a thin wrapper over it
// so existing callers and tests are unaffected.
func SolveLogged(in Input, client string, s settings.Settings) (Result, SolveInfo, error) {
	start := time.Now()
	p, err := in.Normalize()
	if err != nil {
		return Result{}, SolveInfo{}, err
	}
	mo := buildModel(p)
	res, err := mo.run(s)
	info := SolveInfo{
		Client:      client,
		Elapsed:     time.Since(start),
		Stages:      mo.stages,
		Path:        "cascade",
		Preferences: mo.prefSummary,
		Continuity:  mo.contSummary,
		Status:      res.Status,
		Steps:       mo.T,
	}
	return res, info, err
}
