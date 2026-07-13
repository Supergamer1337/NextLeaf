// Package sources wires the configured reading backends into a single
// library.Source. It is the one place that knows which backends exist and how
// they are enabled; add new backends here as they are implemented.
package sources

import (
	"os"
	"time"

	"nextleaf/internal/grimmory"
	"nextleaf/internal/hardcover"
	"nextleaf/internal/library"
)

// cacheTTL is how long a source's data is reused before a refetch — long enough
// to keep page loads fast and stay well under provider rate limits.
const cacheTTL = 10 * time.Minute

// FromEnv builds the reading source from the environment: every backend whose
// credentials are present, each cached and merged into one. It returns nil when
// FromEnv builds a combined source from the configured Hardcover and Grimmory backends.
// It returns nil when no backend has the required environment configuration.
func FromEnv() library.Source {
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

	return library.Combine(enabled...)
}
