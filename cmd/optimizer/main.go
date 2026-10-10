// Command optimizer runs the EV charging MILP HTTP service.
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/alex/optimizer-v2/internal/api"
	"github.com/alex/optimizer-v2/internal/settings"
)

func main() {
	s := settings.Load()
	const addr = ":7050"
	// WriteTimeout starts when the request headers are read and therefore covers
	// the solve, so it must outlast OPTIMIZER_TIME_LIMIT; the others only bound
	// connection setup, request reading and idle keep-alive.
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewServer(s),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("optimizer listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
