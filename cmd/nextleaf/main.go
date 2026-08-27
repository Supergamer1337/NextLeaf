package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nextleaf/internal/config"
	"nextleaf/internal/library"
	"nextleaf/internal/picker"
	"nextleaf/internal/series"
	"nextleaf/internal/sources"
	"nextleaf/internal/web"
)

// resyncInterval is how often the whole read history is re-imported, catching
// anything the recent window slid past while the app was running.
const resyncInterval = 24 * time.Hour

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

	store, backfill := startSeriesTracking(source)
	if store != nil {
		defer func() { _ = store.Close() }()
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := &http.Server{
		Addr: addr,
		Handler: web.NewHandler(web.Deps{
			Source:   source,
			Store:    store,
			Backfill: backfill,
			Prefs:    picker.Prefs{IncludeNovellas: includeNovellas()},
		}),
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

// startSeriesTracking opens the series database and kicks off the one-time
// history import in the background, so the server starts listening immediately
// and only continuations wait. A store that cannot be opened is not fatal: the
// app falls back to variety picks alone rather than refusing to run.
func startSeriesTracking(source library.Source) (*series.Store, *series.Backfill) {
	if source == nil {
		return nil, nil
	}

	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, "nextleaf.db")

	store, err := series.Open(path)
	if err != nil {
		log.Printf("series tracking disabled, could not open %s: %v", path, err)
		return nil, nil
	}
	log.Printf("series tracking using %s", path)

	backfill := series.NewBackfill(store, library.AsHistoryProviders(source))
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			backfill.Run(ctx)
			cancel()

			status := backfill.Status()
			if len(status.Failed) > 0 {
				log.Printf("series history imported (%d books); retrying %v on the next resync",
					status.Imported, status.Failed)
			} else {
				log.Printf("series history imported (%d books)", status.Imported)
			}
			time.Sleep(resyncInterval)
		}
	}()

	return store, backfill
}

// includeNovellas reads the novella preference, which governs both series
// continuations and the volume that represents a series in a variety pick.
// Novellas are offered unless explicitly turned off.
func includeNovellas() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("INCLUDE_NOVELLAS"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
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
