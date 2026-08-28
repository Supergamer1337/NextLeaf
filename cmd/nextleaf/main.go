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

// warmInterval is how often the next-in-series cache is refreshed in the
// background, which is also what notices a newly published book on its own.
const warmInterval = 24 * time.Hour

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

	names := make([]string, len(enabled))
	for i, s := range enabled {
		names[i] = s.Name()
	}
	engine, store := startSeriesTracking(source, names)
	if store != nil {
		defer func() { _ = store.Close() }()
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           web.NewHandler(web.Deps{Source: source, Engine: engine}),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds the body read too, so a slow client cannot hold
		// a connection open through a decision POST.
		ReadTimeout: 30 * time.Second,
		// WriteTimeout must outlast the 30s the recommendation flow may spend
		// on its sources, or slow-but-successful page loads get cut off.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  2 * time.Minute,
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

// startSeriesTracking opens the statement store and builds the series engine,
// then keeps the next-in-series cache warm in the background so the drawer is
// full and new releases are noticed without anyone visiting the page. A store
// that cannot be opened is not fatal: the app falls back to variety picks
// alone rather than refusing to run.
func startSeriesTracking(source library.Source, order []string) (*series.Engine, *series.Store) {
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

	engine := series.NewEngine(store, source, picker.Prefs{IncludeNovellas: includeNovellas()})
	engine.SourceOrder = order
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			engine.Warm(ctx)
			cancel()
			log.Print("series next-book lookups refreshed")
			time.Sleep(warmInterval)
		}
	}()
	return engine, store
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
