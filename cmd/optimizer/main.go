// Command optimizer runs the EV charging MILP HTTP service.
package main

import (
	"log"
	"net/http"

	"github.com/alex/optimizer-v2/internal/api"
	"github.com/alex/optimizer-v2/internal/settings"
)

func main() {
	s := settings.Load()
	const addr = ":7050"
	log.Printf("optimizer listening on %s", addr)
	if err := http.ListenAndServe(addr, api.NewServer(s)); err != nil {
		log.Fatal(err)
	}
}
