// Package sources wires the configured reading backends into a single
// library.Source. It is the one place that knows which backends exist and how
// they are enabled; add new backends here as they are implemented.
package sources

import (
	"os"
	"time"

	"nextleaf/internal/hardcover"
	"nextleaf/internal/library"
)

// cacheTTL is how long a source's data is reused before a refetch — long enough
// to keep page loads fast and stay well under provider rate limits.
const cacheTTL = 10 * time.Minute

// FromEnv builds the reading source from the environment: every backend whose
// credentials are present, each cached and merged into one. It returns nil when
// nothing is configured, so the caller can surface a setup hint.
func FromEnv() library.Source {
	var enabled []library.Source

	if token := os.Getenv("HARDCOVER_TOKEN"); token != "" {
		enabled = append(enabled, library.NewCached(hardcover.New(token), cacheTTL))
	}

	return library.Combine(enabled...)
}
