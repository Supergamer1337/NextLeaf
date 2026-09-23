package series

import (
	"context"
	"sync"
	"time"

	"nextleaf/internal/library"
)

// Lookahead caches next-in-series lookups. Asking a catalogue what follows a
// book is a per-series round trip, and the answer changes only when something
// is published, so caching for a day keeps a reader with many tracked series
// from hammering the backend on every page load.
type Lookahead struct {
	resolver library.SeriesResolver
	ttl      time.Duration
	now      func() time.Time // overridable in tests

	mu      sync.Mutex
	answers map[string]answer
}

type answer struct {
	entry library.Entry
	found bool
	err   error // a failed lookup, held for failureTTL only
	at    time.Time
}

// failureTTL is how long a failure is held. Unheld, a throttled backend would
// be asked again by every render, putting the round trip into every page load.
const failureTTL = time.Minute

// NewLookahead wraps resolver with a ttl-long cache.
func NewLookahead(resolver library.SeriesResolver, ttl time.Duration) *Lookahead {
	return &Lookahead{
		resolver: resolver,
		ttl:      ttl,
		now:      time.Now,
		answers:  make(map[string]answer),
	}
}

// keyFor identifies a question, carrying every dimension that changes the
// answer: the same name is a different series on another provider, an unplaced
// series is not slot zero, and a novella-inclusive answer does not serve a
// query that excludes them.
func keyFor(q library.SeriesQuery) string {
	pos, placed := q.Series.Slot()
	slot := "unplaced"
	if placed {
		slot = formatPos(pos)
	}
	novellas := "novellas:no"
	if q.IncludeNovellas {
		novellas = "novellas:yes"
	}
	return q.Series.Source + "\x00" + q.Series.Slug + "\x00" + key(q.Series.Name) + "\x00" + slot + "\x00" + novellas
}

// Cached reports whether Next would answer q without a round trip, so callers
// can budget fresh queries separately from cache hits. A held failure counts:
// reading it back costs nothing.
func (l *Lookahead) Cached(q library.SeriesQuery) bool {
	k := keyFor(q)
	l.mu.Lock()
	defer l.mu.Unlock()
	cached, ok := l.answers[k]
	return ok && l.fresh(cached)
}

func (l *Lookahead) fresh(a answer) bool {
	ttl := l.ttl
	if a.err != nil {
		ttl = failureTTL
	}
	return l.now().Sub(a.at) < ttl
}

// Next returns the book following q's position, from cache when it is fresh.
func (l *Lookahead) Next(ctx context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	// The position is part of the key, so finishing a book asks a new question
	// rather than reading back the answer for the previous one.
	k := keyFor(q)

	l.mu.Lock()
	cached, ok := l.answers[k]
	l.mu.Unlock()
	if ok && l.fresh(cached) {
		return cached.entry, cached.found, cached.err
	}

	entry, found, err := l.resolver.NextInSeries(ctx, q)
	if err != nil {
		entry, found = library.Entry{}, false
	}
	l.mu.Lock()
	l.answers[k] = answer{entry: entry, found: found, err: err, at: l.now()}
	l.mu.Unlock()
	return entry, found, err
}
