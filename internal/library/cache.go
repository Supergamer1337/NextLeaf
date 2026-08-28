package library

import (
	"context"
	"sync"
	"time"
)

// Cached wraps a Source with a time-to-live cache. It keeps page loads fast and
// keeps the app well under provider rate limits: results are reused until the
// TTL lapses, and concurrent callers share a single in-flight fetch rather than
// stampeding the backend. Errors are never cached — and when a fetch fails
// with older data in hand, that data is served instead, with Health reporting
// the staleness so the page can say so. An outage degrades to an honest old
// page, never a broken one.
type Cached struct {
	src Source
	ttl time.Duration
	now func() time.Time // overridable in tests

	readingMu sync.Mutex
	reading   []Entry
	readingAt time.Time
	readingOK bool

	readsMu    sync.Mutex
	reads      []Entry
	readsLimit int
	readsAt    time.Time
	readsOK    bool

	toReadMu sync.Mutex
	toRead   []Entry
	toReadAt time.Time
	toReadOK bool

	healthMu sync.Mutex
	// Staleness is per query: one query recovering must not mask another
	// still serving fallback data.
	stale    map[string]bool
	success  map[string]time.Time
	staleErr map[string]error
}

// NewCached returns a Source that caches src's results for ttl.
func NewCached(src Source, ttl time.Duration) *Cached {
	return &Cached{
		src: src, ttl: ttl, now: time.Now,
		stale:    make(map[string]bool),
		success:  make(map[string]time.Time),
		staleErr: make(map[string]error),
	}
}

func (c *Cached) Name() string { return c.src.Name() }

// Unwrap exposes the wrapped Source so optional capabilities (e.g.
// SeriesResolver) remain reachable through the cache via AsSeriesResolver.
func (c *Cached) Unwrap() Source { return c.src }

func (c *Cached) fresh(at time.Time, ok bool) bool {
	return ok && c.now().Sub(at) < c.ttl
}

// Health reports whether any of this source's queries is being served from
// stale fallback data. Since is when the oldest such data was last fresh.
func (c *Cached) Health() Health {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	h := Health{Source: c.src.Name()}
	for query, isStale := range c.stale {
		if !isStale {
			continue
		}
		h.Stale = true
		at := c.success[query]
		if h.Since.IsZero() || at.Before(h.Since) {
			h.Since = at
			// The error travels with the query whose age sets Since, so the
			// report never dates one outage while describing another.
			h.Err = ""
			if err := c.staleErr[query]; err != nil {
				h.Err = err.Error()
			}
		}
	}
	return h
}

// noteSuccess records a clean fetch of one query, clearing its staleness.
func (c *Cached) noteSuccess(query string) {
	c.healthMu.Lock()
	c.success[query], c.stale[query] = c.now(), false
	delete(c.staleErr, query)
	c.healthMu.Unlock()
}

// noteFallback records that err forced one query onto older data.
func (c *Cached) noteFallback(query string, err error) {
	c.healthMu.Lock()
	c.staleErr[query], c.stale[query] = err, true
	c.healthMu.Unlock()
}

// CurrentlyReading serves the cached in-progress list when fresh; otherwise it
// fetches once, holding the lock so concurrent callers wait and reuse it.
func (c *Cached) CurrentlyReading(ctx context.Context) ([]Entry, error) {
	c.readingMu.Lock()
	defer c.readingMu.Unlock()

	if c.fresh(c.readingAt, c.readingOK) {
		return c.reading, nil
	}

	entries, err := c.src.CurrentlyReading(ctx)
	if err != nil {
		// A source that answered before is down, not gone: old data with a
		// visible staleness flag beats an error page.
		if c.readingOK {
			c.noteFallback("reading", err)
			return c.reading, nil
		}
		return nil, err
	}
	c.noteSuccess("reading")
	c.reading, c.readingAt, c.readingOK = entries, c.now(), true
	return entries, nil
}

// RecentReads serves the cached reads when fresh and requested with the same
// limit; otherwise it fetches once, holding the lock so concurrent callers wait
// and reuse the result.
func (c *Cached) RecentReads(ctx context.Context, limit int) ([]Entry, error) {
	c.readsMu.Lock()
	defer c.readsMu.Unlock()

	// The full history (limit 0) answers any cap by slicing; a capped result
	// answers only its own cap. That lets the engine's full fetch and the
	// picker's window share one cache entry instead of thrashing it.
	servable := func() bool {
		return c.readsOK && (c.readsLimit == limit || c.readsLimit == 0)
	}
	capped := func() []Entry {
		if limit > 0 && limit < len(c.reads) {
			return c.reads[:limit]
		}
		return c.reads
	}

	if servable() && c.fresh(c.readsAt, c.readsOK) {
		return capped(), nil
	}

	entries, err := c.src.RecentReads(ctx, limit)
	if err != nil {
		// A source that answered before is down, not gone: old data with a
		// visible staleness flag beats an error page — but only data that
		// actually answers this limit; a capped cache cannot answer an
		// uncapped request.
		if servable() {
			c.noteFallback("reads", err)
			return capped(), nil
		}
		return nil, err
	}
	c.noteSuccess("reads")
	c.reads, c.readsLimit, c.readsAt, c.readsOK = entries, limit, c.now(), true
	return entries, nil
}

// ToRead serves the cached TBR list when fresh; otherwise it fetches once,
// holding the lock so concurrent callers wait and reuse the result.
func (c *Cached) ToRead(ctx context.Context) ([]Entry, error) {
	c.toReadMu.Lock()
	defer c.toReadMu.Unlock()

	if c.fresh(c.toReadAt, c.toReadOK) {
		return c.toRead, nil
	}

	entries, err := c.src.ToRead(ctx)
	if err != nil {
		// A source that answered before is down, not gone: old data with a
		// visible staleness flag beats an error page.
		if c.toReadOK {
			c.noteFallback("toRead", err)
			return c.toRead, nil
		}
		return nil, err
	}
	c.noteSuccess("toRead")
	c.toRead, c.toReadAt, c.toReadOK = entries, c.now(), true
	return entries, nil
}
