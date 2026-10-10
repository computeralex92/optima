// Command client posts a case file to a running optimizer service and renders
// the resulting charge schedule.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/computeralex92/optima/internal/optimizer"
)

const defaultURL = "http://localhost:7050"

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("usage: %s <case.json>", os.Args[0])
	}
	req, err := loadCase(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	res, err := post(serviceURL()+"/optimize/charge-schedule", req)
	if err != nil {
		log.Fatal(err)
	}
	render(os.Stdout, *res)
}

// serviceURL returns $OPTIMIZER_URL or the default.
func serviceURL() string {
	if u := os.Getenv("OPTIMIZER_URL"); u != "" {
		return u
	}
	return defaultURL
}

// loadCase reads path and returns the request it carries. The file is either an
// upstream test case with a top-level "request" key or a bare request object.
func loadCase(path string) (*optimizer.Input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env struct {
		Request *optimizer.Input `json:"request"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if env.Request != nil {
		return env.Request, nil
	}
	var bare optimizer.Input
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &bare, nil
}

// post sends the request and decodes the result.
func post(url string, req *optimizer.Input) (*optimizer.Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST %s: %s: %s", url, resp.Status, msg)
	}
	var res optimizer.Result
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}

// render writes one row per step per battery (charge / discharge / SOC) with the
// per-step grid import/export, then a footer line with the objective and status.
func render(w io.Writer, r optimizer.Result) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STEP\tBATTERY\tCHARGE\tDISCHARGE\tSOC\tGRID_IMPORT\tGRID_EXPORT")
	for t := range r.GridImport {
		imp := r.GridImport[t]
		exp := at(r.GridExport, t)
		for i, b := range r.Batteries {
			fmt.Fprintf(tw, "%d\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\n",
				t, i, at(b.ChargingPower, t), at(b.DischargingPower, t),
				at(b.StateOfCharge, t), imp, exp)
		}
	}
	tw.Flush()
	if r.ObjectiveValue != nil {
		fmt.Fprintf(w, "objective: %.4f status: %s\n", *r.ObjectiveValue, r.Status)
	} else {
		fmt.Fprintf(w, "objective: n/a status: %s\n", r.Status)
	}
}

// at returns s[i] or 0 when the slice is short.
func at(s []float64, i int) float64 {
	if i < len(s) {
		return s[i]
	}
	return 0
}
