package main

import (
	"log"
	"net/http"
	"os"

	"insights/internal/api"
	"insights/internal/ingest"
	"insights/internal/store"
)

func main() {
	s, err := store.New()
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	defer s.Close()

	mux := http.NewServeMux()

	// OTLP ingest: metrics and GenAI spans are stored; logs are accepted and
	// discarded so senders pointing OTEL_EXPORTER_OTLP_ENDPOINT here don't
	// log export failures.
	mux.Handle("POST /v1/metrics", ingest.NewHandler(s))
	mux.Handle("POST /v1/traces", ingest.NewTracesHandler(s))
	mux.Handle("POST /v1/logs", ingest.DiscardLogs())

	// Dashboard API
	apiHandler := api.NewHandler(s)
	apiHandler.Register(mux)

	// Vue SPA (embedded)
	mux.Handle("/", newSPAHandler())

	addr := os.Getenv("INSIGHTS_ADDR")
	if addr == "" {
		addr = ":4318"
	}

	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
