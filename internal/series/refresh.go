package series

import (
	"context"
	"log"
	"time"

	"nextleaf/internal/library"
)

const (
	// refreshPause spaces out lookups so refreshing a reader tracked in
	// hundreds of series stays a background trickle. Hardcover throttles
	// noticeably faster than four requests a second.
	refreshPause = 1500 * time.Millisecond
	// refreshBackoff is the extra wait after a lookup fails, before retrying
	// it once. A throttled source recovers in seconds, and waiting for the
	// next daily pass would leave the drawer wrong far longer than it needs.
	refreshBackoff = 10 * time.Second
)

// Refresher resolves the next book of every tracked series and records it, so
// the series drawer can show covers without the page ever waiting on a lookup,
// and so a volume published since the last pass is noticed on its own.
type Refresher struct {
	store           *Store
	lookahead       *Lookahead
	includeNovellas bool
	pause           time.Duration
	backoff         time.Duration
}

// NewRefresher prepares a refresh pass over store using lookahead.
func NewRefresher(store *Store, lookahead *Lookahead, includeNovellas bool) *Refresher {
	return &Refresher{
		store:           store,
		lookahead:       lookahead,
		includeNovellas: includeNovellas,
		pause:           refreshPause,
		backoff:         refreshBackoff,
	}
}

// Run looks up every eligible series in turn. It is best effort: a series whose
// lookup fails keeps whatever was known about it and is retried next pass,
// because mistaking a throttled backend for "nothing left to read" would file a
// live series under Finished.
func (r *Refresher) Run(ctx context.Context) {
	tracked, err := r.store.List(ctx)
	if err != nil {
		log.Printf("series refresh: listing tracked series: %v", err)
		return
	}

	for i, t := range tracked {
		if !r.worthAsking(t) {
			continue
		}
		if i > 0 && !r.wait(ctx, r.pause) {
			return
		}

		q := library.SeriesQuery{
			Series:          library.Series{Name: t.Name, Position: t.Position, Slug: t.Slug},
			IncludeNovellas: r.includeNovellas,
		}
		entry, found, err := r.lookahead.Next(ctx, q)
		if err != nil {
			// Almost always throttling, which passes in seconds, so back off
			// and give it one more go before leaving it to the next pass.
			if !r.wait(ctx, r.backoff) {
				return
			}
			entry, found, err = r.lookahead.Next(ctx, q)
		}
		if err != nil {
			log.Printf("series refresh: looking up %q: %v", t.Name, err)
			continue
		}
		if err := r.store.SetNext(ctx, t.Name, entry, found, time.Now()); err != nil {
			log.Printf("series refresh: recording %q: %v", t.Name, err)
		}
	}
}

// wait sleeps for d, reporting false when the context is cancelled first.
func (r *Refresher) wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// worthAsking skips series a lookup could tell us nothing useful about: one the
// reader dropped, one with no known position, and one the author has ended
// where the reader is already caught up.
func (r *Refresher) worthAsking(t Tracked) bool {
	switch {
	case t.Decision == Dropped:
		return false
	case t.Position == 0:
		return false
	case t.Completed && t.CaughtUp:
		return false
	default:
		return true
	}
}
