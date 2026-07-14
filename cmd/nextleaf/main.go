package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nextleaf/internal/config"
	"nextleaf/internal/library"
	"nextleaf/internal/sources"
	"nextleaf/internal/web"
)

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		log.Printf("loading .env: %v", err)
	}

	source, enabled := sources.FromEnv()
	if source == nil {
		log.Print("no reading sources configured; the home page will show a configuration hint")
	} else {
		reportSources(enabled)
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           web.NewHandler(source),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("nextleaf listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
		os.Exit(1)
	}
}

// reportSources logs which backends are active and whether each one's
// credentials are accepted, so a bad token or password shows up at startup
// rather than as a silently broken page. A failed check is logged, not fatal —
// the source may be reachable once its credentials are corrected.
func reportSources(enabled []library.Source) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, r := range sources.Verify(ctx, enabled) {
		switch {
		case r.Err != nil:
			log.Printf("source %q activated, but its credentials were rejected: %v", r.Name, r.Err)
		default:
			log.Printf("source %q activated, credentials OK", r.Name)
		}
	}
}
