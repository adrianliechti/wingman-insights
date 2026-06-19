package main

import (
	"log"
	"net/http"
	"os"
	"strings"

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

	// OTLP ingest stays at the root regardless of the UI base path: external
	// senders target it directly via OTEL_EXPORTER_OTLP_ENDPOINT. Metrics and
	// GenAI spans are stored; logs are accepted and discarded so senders don't
	// log export failures.
	mux.Handle("POST /v1/metrics", ingest.NewHandler(s))
	mux.Handle("POST /v1/traces", ingest.NewTracesHandler(s))
	mux.Handle("POST /v1/logs", ingest.DiscardLogs())

	// The dashboard (its /api and the embedded Vue SPA) can be mounted under a
	// sub-path (e.g. /insights) via INSIGHTS_BASE_PATH. ServeMux auto-redirects
	// the bare prefix to the trailing-slash form, and StripPrefix hands the
	// inner mux root-relative paths.
	basePath := normalizeBasePath(os.Getenv("INSIGHTS_BASE_PATH"))

	app := http.NewServeMux()
	api.NewHandler(s).Register(app)
	app.Handle("/", newSPAHandler(basePath))

	// basePath is "" at the root (then basePath+"/" is "/" and StripPrefix("")
	// is a no-op), or "/insights" under a sub-path. One line covers both.
	mux.Handle(basePath+"/", http.StripPrefix(basePath, app))

	addr := os.Getenv("INSIGHTS_ADDR")
	if addr == "" {
		addr = ":4318"
	}

	log.Printf("listening on %s (UI base path %q)", addr, basePath+"/")
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// normalizeBasePath cleans a configured base path into a canonical form with a
// leading slash and no trailing slash; an empty or "/" value yields "" (root).
func normalizeBasePath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}
