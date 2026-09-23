// Package sources wires the configured reading backends into a single
// library.Source. It is the one place that knows which backends exist and how
// they are enabled; add new backends here as they are implemented.
package sources

import (
	"context"
	"os"
	"time"

	"nextleaf/internal/grimmory"
	"nextleaf/internal/hardcover"
	"nextleaf/internal/library"
)

// cacheTTL bounds how long a source's data is reused before a read refetches
// it, until something refreshes it ahead of need. The series engine does — on
// a schedule, and behind any page load that finds it more than a few seconds
// old — and from then on a read never waits on the source.
const cacheTTL = time.Hour

// FromEnv builds the reading source from the environment: every backend whose
// credentials are present, each cached. It returns the merged Source (nil when
// no backend is configured, so the app can detect the unconfigured case)
// alongside the individual enabled sources, in activation order, so the caller
// can report and verify each one.
func FromEnv() (library.Source, []library.Source) {
	var enabled []library.Source

	if token := os.Getenv("HARDCOVER_TOKEN"); token != "" {
		enabled = append(enabled, library.NewCached(hardcover.New(token), cacheTTL))
	}

	if url := os.Getenv("GRIMMORY_URL"); url != "" {
		user, pass := os.Getenv("GRIMMORY_USERNAME"), os.Getenv("GRIMMORY_PASSWORD")
		if user != "" && pass != "" {
			enabled = append(enabled, library.NewCached(grimmory.New(url, user, pass), cacheTTL))
		}
	}

	return library.Combine(enabled...), enabled
}

// Report is the outcome of checking one activated source's credentials at
// startup. Err is nil when the credentials were accepted, or when the source
// needs none (nothing to check).
type Report struct {
	Name string
	Err  error
}

// Verify checks each source's credentials, returning one Report per source in
// the given order. Sources that do not support verification report a nil error.
// A failed check is reported, never fatal: the server should still start so the
// credentials can be fixed without downtime.
func Verify(ctx context.Context, srcs []library.Source) []Report {
	reports := make([]Report, 0, len(srcs))
	for _, s := range srcs {
		r := Report{Name: s.Name()}
		if v, ok := library.AsVerifier(s); ok {
			r.Err = v.Verify(ctx)
		}
		reports = append(reports, r)
	}
	return reports
}
