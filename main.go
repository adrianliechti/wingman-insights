package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"insights/internal/api"
	"insights/internal/ingest"
	mcpserver "insights/internal/mcp"
	"insights/internal/store"
	"insights/pkg/directory/entra"
	filedir "insights/pkg/directory/file"
)

func main() {
	s, err := store.New()
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	// Closed last (deferred LIFO, after the server has drained below) so DuckDB
	// flushes its WAL and closes the file intact — a hard kill mid-write can
	// corrupt the database.
	defer s.Close()

	// Optional Entra directory: resolves the OTel user.id / user.email values
	// (object ids, emails or usernames) to display names and kinds. Unconfigured
	// (no INSIGHTS_ENTRA_* env) → the store keeps raw ids and adds no resolution.
	if dir, ok := entra.FromEnv(); ok {
		s.SetDirectory(dir)
		s.SetDepartmentPrefix(strings.EqualFold(os.Getenv("INSIGHTS_ENTRA_DEPARTMENT_MODE"), "prefix"))
		// Keep the directory and its in-DB mirror fresh. Queries resolve
		// identities by JOINing the directory table (not per-row Lookup), so the
		// table must be re-materialized after each refresh — nothing else drives
		// it. Warm both once at startup, then re-sync on an interval.
		go func() {
			sync := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if err := dir.Refresh(ctx); err != nil {
					log.Printf("directory: refresh failed: %v", err)
					return
				}
				if err := s.SyncDirectory(ctx); err != nil {
					log.Printf("directory: table sync failed: %v", err)
				}
			}
			sync()
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for range t.C {
				sync()
			}
		}()
	} else if dir, ok, err := filedir.FromEnv(); err != nil {
		// A configured-but-broken directory file is a startup error: silently
		// resolving nothing would make the department/location views look empty
		// for a reason the operator can't see.
		log.Fatalf("directory file: %v", err)
	} else if ok {
		// Static NDJSON directory (local/dev/demo or air-gapped). It never
		// changes at runtime, so materialize the in-DB table once; no refresh
		// loop. Entra, when configured, takes precedence over the file.
		s.SetDirectory(dir)
		s.SetDepartmentPrefix(strings.EqualFold(os.Getenv("INSIGHTS_ENTRA_DEPARTMENT_MODE"), "prefix"))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		if err := s.SyncDirectory(ctx); err != nil {
			log.Printf("directory: table sync failed: %v", err)
		}
		cancel()
	}

	// Optional retention: without it the database grows unboundedly. When
	// INSIGHTS_RETENTION_DAYS is set (> 0), telemetry older than that is pruned
	// once at startup and then hourly; unset/0 keeps everything.
	if days, _ := strconv.Atoi(os.Getenv("INSIGHTS_RETENTION_DAYS")); days > 0 {
		go func() {
			prune := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if n, err := s.PruneBefore(ctx, time.Now().UTC().AddDate(0, 0, -days)); err != nil {
					log.Printf("retention: prune failed: %v", err)
				} else if n > 0 {
					log.Printf("retention: pruned %d rows older than %dd", n, days)
				}
			}
			prune()
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for range t.C {
				prune()
			}
		}()
	}

	mux := http.NewServeMux()

	// OTLP ingest stays at the root regardless of the UI base path: external
	// senders target it directly via OTEL_EXPORTER_OTLP_ENDPOINT. Metrics and
	// GenAI spans are stored; logs are accepted and discarded so senders don't
	// log export failures.
	metricsHandler := ingest.NewBufferedHandler(s)
	mux.Handle("POST /v1/metrics", metricsHandler)
	mux.HandleFunc("GET /metrics", metricsHandler.ServeMetrics)
	mux.Handle("POST /v1/traces", ingest.NewTracesHandler(s))
	mux.Handle("POST /v1/logs", ingest.DiscardLogs())

	// The dashboard (its /api and the embedded Vue SPA) can be mounted under a
	// sub-path (e.g. /insights) via INSIGHTS_BASE_PATH. ServeMux auto-redirects
	// the bare prefix to the trailing-slash form, and StripPrefix hands the
	// inner mux root-relative paths.
	basePath := normalizeBasePath(os.Getenv("INSIGHTS_BASE_PATH"))

	app := http.NewServeMux()
	// Resolved before any handler is built so a misconfigured development
	// bypass fails fast rather than after auth setup succeeds.
	mcpOpts := mcpOptions()
	h := api.NewHandler(s)

	h.Register(app)
	h.RegisterPersonal(app)
	if os.Getenv("INSIGHTS_MCP_ENABLED") == "true" {
		app.Handle("/mcp", mcpserver.NewHandler(s, os.Getenv("INSIGHTS_ADMIN_GROUP"), mcpOpts...))
	}

	app.Handle("/", newSPAHandler(basePath))

	// basePath is "" at the root (then basePath+"/" is "/" and StripPrefix("")
	// is a no-op), or "/insights" under a sub-path. One line covers both.
	mux.Handle(basePath+"/", http.StripPrefix(basePath, app))

	addr := os.Getenv("INSIGHTS_ADDR")
	if addr == "" {
		addr = ":4318"
	}

	// Timeouts bound a stalled/slow client to a fixed amount of held-open memory
	// (goroutine + partial read buffer) instead of indefinitely — otlpBodyLimit
	// caps size, these cap time.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Trap SIGINT/SIGTERM so we drain in-flight requests and close DuckDB
	// cleanly instead of letting the default handler hard-kill the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("listening on %s (UI base path %q)", addr, basePath+"/")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	stop() // restore default handling: a second Ctrl-C force-quits
	log.Println("shutting down…")

	// Stop accepting connections and wait for active handlers (which may be
	// writing to DuckDB) to finish before s.Close() runs.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown timed out, forcing close: %v", err)
		srv.Close()
	}
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer flushCancel()
	if err := metricsHandler.Close(flushCtx); err != nil {
		log.Printf("metrics drain failed: %v", err)
	}
}

// mcpOptions resolves the optional MCP browser allowlist and the
// development-only auth bypass. Both are inert unless explicitly requested and,
// because the bypass hands org-wide data to anonymous callers, it additionally
// requires dashboard auth to already be disabled — so no single variable can
// open up a real deployment.
func mcpOptions() []mcpserver.Option {
	var opts []mcpserver.Option
	if origins := splitList(os.Getenv("INSIGHTS_MCP_ALLOWED_ORIGINS")); len(origins) > 0 {
		opts = append(opts, mcpserver.WithAllowedOrigins(origins...))
		if slices.Contains(origins, "*") {
			log.Print("WARNING: INSIGHTS_MCP_ALLOWED_ORIGINS=* lets any website call MCP from a browser; prefer an explicit origin list")
		}
	}
	if os.Getenv("INSIGHTS_MCP_AUTH_DISABLED") != "true" {
		return opts
	}
	if os.Getenv("INSIGHTS_AUTH_DISABLED") != "true" {
		log.Fatal("INSIGHTS_MCP_AUTH_DISABLED=true is a development-only bypass and also requires INSIGHTS_AUTH_DISABLED=true")
	}
	user := strings.TrimSpace(os.Getenv("INSIGHTS_MCP_DEV_USER"))
	if user == "" {
		user = "dev"
	}
	admin := os.Getenv("INSIGHTS_DEV_ADMIN") == "true"
	log.Printf("WARNING: MCP auth is disabled; unauthenticated callers act as %q (admin=%t) — development only", user, admin)
	return append(opts, mcpserver.WithDevFallback(user, admin))
}

// splitList parses a comma-separated env value, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
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
