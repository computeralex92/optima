package explain

import "github.com/computeralex92/optima/internal/optimizer"

// Input is the upstream explain payload: the request, the plan, and optional
// display details from evcc.
type Input struct {
	Req      optimizer.Input  `json:"req"`
	Res      optimizer.Result `json:"res"`
	Details  Details          `json:"details"`
	Currency string           `json:"currency"`
	Language string           `json:"language"`
}

// Details is the optional evcc state that makes the digest readable.
type Details struct {
	Timestamp      []string        `json:"timestamp"`
	BatteryDetails []BatteryDetail `json:"batteryDetails"`
	DemandDetails  []DemandDetail  `json:"demandDetails"`
}

// BatteryDetail names and sizes one battery or vehicle.
type BatteryDetail struct {
	Name     string  `json:"name"`
	Title    string  `json:"title"`
	Type     string  `json:"type"`
	Capacity float64 `json:"capacity"` // kWh
}

// DemandDetail describes a household load the plan includes.
type DemandDetail struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}
