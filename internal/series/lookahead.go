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
	at    time.Time
}

// NewLookahead wraps resolver with a ttl-long cache.
func NewLookahead(resolver library.SeriesResolver, ttl time.Duration) *Lookahead {
	return &Lookahead{
		resolver: resolver,
		ttl:      ttl,
		now:      time.Now,
		answers:  make(map[string]answer),
	}
}

// Next returns the book following q's position, from cache when it is fresh.
// Errors are never cached: a throttled backend must not hide a series for a
// whole day.
func (l *Lookahead) Next(ctx context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	// The position is part of the key, so finishing a book asks a new question
	// rather than reading back the answer for the previous one.
	pos, _ := q.Series.Slot()
	k := key(q.Series.Name) + "\x00" + formatPos(pos)

	l.mu.Lock()
	cached, ok := l.answers[k]
	l.mu.Unlock()
	if ok && l.now().Sub(cached.at) < l.ttl {
		return cached.entry, cached.found, nil
	}

	entry, found, err := l.resolver.NextInSeries(ctx, q)
	if err != nil {
		return library.Entry{}, false, err
	}

	l.mu.Lock()
	l.answers[k] = answer{entry: entry, found: found, at: l.now()}
	l.mu.Unlock()
	return entry, found, nil
}
