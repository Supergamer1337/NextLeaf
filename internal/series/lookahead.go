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
	// save, when set, keeps each answer beyond the process.
	save func(key string, a CachedAnswer)

	mu      sync.Mutex
	answers map[string]answer
}

// answer is the last answer to a question, and the last failure to re-check
// it: a failure is held briefly without displacing the answer before it.
type answer struct {
	entry      library.Entry
	found      bool
	at         time.Time // zero until a question has been answered
	freshUntil time.Time
	err        error
	failedAt   time.Time
	fails      int // failures since the last answer
}

const (
	// failureTTL is how long a failure is held. Unheld, a throttled backend
	// would be asked again by every render, putting the round trip into every
	// page load.
	failureTTL = time.Minute
	// settledTTL is how long "nothing left" holds for a series its provider
	// calls complete: it will not grow, so asking daily is wasted.
	settledTTL = 7 * 24 * time.Hour
	// giveUpAfter is how many failures running a question takes to be given
	// up until the scheduled pass. The first may be a hiccup, so it stays
	// pending and is asked again once its hold ends; one that keeps failing
	// would otherwise be asked every minute, all day.
	giveUpAfter = 2
)

// NewLookahead wraps resolver with a ttl-long cache.
func NewLookahead(resolver library.SeriesResolver, ttl time.Duration) *Lookahead {
	return &Lookahead{
		resolver: resolver,
		ttl:      ttl,
		now:      time.Now,
		answers:  make(map[string]answer),
	}
}

// Load seeds the cache with answers kept from an earlier process.
func (l *Lookahead) Load(kept map[string]CachedAnswer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, a := range kept {
		l.answers[k] = answer{entry: a.Entry, found: a.Found, at: a.At, freshUntil: a.FreshUntil}
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
// can budget fresh queries separately from cache hits: a fresh answer, or a
// failure still held.
func (l *Lookahead) Cached(q library.SeriesQuery) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.answers[keyFor(q)]
	return ok && (l.held(a) || l.fresh(a))
}

func (l *Lookahead) fresh(a answer) bool { return !a.at.IsZero() && l.now().Before(a.freshUntil) }

func (l *Lookahead) held(a answer) bool {
	return a.err != nil && l.now().Sub(a.failedAt) < failureTTL
}

// Last is the last answer to q however old, for showing while it is re-checked.
func (l *Lookahead) Last(q library.SeriesQuery) (library.Entry, bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.answers[keyFor(q)]
	if !ok || a.at.IsZero() {
		return library.Entry{}, false, false
	}
	return a.entry, a.found, true
}

// Failed reports whether q has never been answered and has failed often
// enough to be given up (see giveUpAfter): no answer is coming until the
// scheduled pass asks again.
func (l *Lookahead) Failed(q library.SeriesQuery) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.answers[keyFor(q)]
	return ok && a.at.IsZero() && a.fails >= giveUpAfter
}

// Next returns the book following q's position, from cache when it is fresh.
func (l *Lookahead) Next(ctx context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	// The position is part of the key, so finishing a book asks a new question
	// rather than reading back the answer for the previous one.
	k := keyFor(q)

	l.mu.Lock()
	a := l.answers[k]
	l.mu.Unlock()
	switch {
	case l.held(a):
		return library.Entry{}, false, a.err
	case l.fresh(a):
		return a.entry, a.found, nil
	}

	asked := l.now()
	entry, found, err := l.resolver.NextInSeries(ctx, q)
	now := l.now()
	if err != nil && ctx.Err() != nil {
		return library.Entry{}, false, err // cut short by the caller, not failed
	}
	if err != nil {
		l.mu.Lock()
		// Another ask may have answered while this one was out.
		if cur := l.answers[k]; cur.at.IsZero() || cur.at.Before(asked) {
			// One recorded while this ask was out is the same hiccup.
			if cur.fails == 0 || cur.failedAt.Before(asked) {
				cur.fails++
			}
			cur.err, cur.failedAt = err, now
			l.answers[k] = cur
		}
		l.mu.Unlock()
		return library.Entry{}, false, err
	}
	ttl := l.ttl
	if !found && q.Series.Completed {
		ttl = settledTTL
	}
	a = answer{entry: entry, found: found, at: now, freshUntil: now.Add(ttl)}
	l.mu.Lock()
	l.answers[k] = a
	l.mu.Unlock()
	if l.save != nil {
		l.save(k, CachedAnswer{Entry: entry, Found: found, At: now, FreshUntil: a.freshUntil})
	}
	return entry, found, nil
}
