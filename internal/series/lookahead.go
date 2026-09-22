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
	// err is a failed lookup, held only briefly (see failureTTL).
	err error
	at  time.Time
}

// failureTTL is how long a failed lookup is held before it is tried again. A
// throttled backend fails every row at once; retrying all of them on every
// render puts the round trip back into every page load for as long as it
// stays down. Long enough to stop that, short enough that a series is never
// hidden for more than a moment.
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

// Cached reports whether a fresh answer for q is already held, so callers can
// budget fresh queries separately from cache hits. A held failure is not an
// answer: it stops the backend being asked again, but nothing may be said on
// the strength of it.
func (l *Lookahead) Cached(q library.SeriesQuery) bool {
	k := keyFor(q)
	l.mu.Lock()
	defer l.mu.Unlock()
	cached, ok := l.answers[k]
	return ok && cached.err == nil && l.now().Sub(cached.at) < l.ttl
}

// Next returns the book following q's position, from cache when it is fresh.
// A failure is held too, but only for failureTTL: a throttled backend must not
// hide a series for a whole day, and must not be asked again by every render
// in the meantime either.
func (l *Lookahead) Next(ctx context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	// The position is part of the key, so finishing a book asks a new question
	// rather than reading back the answer for the previous one.
	k := keyFor(q)

	l.mu.Lock()
	cached, ok := l.answers[k]
	l.mu.Unlock()
	if ok {
		if age := l.now().Sub(cached.at); cached.err != nil && age < failureTTL {
			return library.Entry{}, false, cached.err
		} else if cached.err == nil && age < l.ttl {
			return cached.entry, cached.found, nil
		}
	}

	entry, found, err := l.resolver.NextInSeries(ctx, q)

	l.mu.Lock()
	l.answers[k] = answer{entry: entry, found: found, err: err, at: l.now()}
	l.mu.Unlock()
	if err != nil {
		return library.Entry{}, false, err
	}
	return entry, found, nil
}
