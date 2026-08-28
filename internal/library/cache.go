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

	healthMu    sync.Mutex
	lastSuccess time.Time
	lastErr     error
	servingOld  bool
}

// NewCached returns a Source that caches src's results for ttl.
func NewCached(src Source, ttl time.Duration) *Cached {
	return &Cached{src: src, ttl: ttl, now: time.Now}
}

func (c *Cached) Name() string { return c.src.Name() }

// Unwrap exposes the wrapped Source so optional capabilities (e.g.
// SeriesResolver) remain reachable through the cache via AsSeriesResolver.
func (c *Cached) Unwrap() Source { return c.src }

func (c *Cached) fresh(at time.Time, ok bool) bool {
	return ok && c.now().Sub(at) < c.ttl
}

// Health reports whether this source is being served from stale fallback data.
func (c *Cached) Health() Health {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	h := Health{Source: c.src.Name(), Stale: c.servingOld, Since: c.lastSuccess}
	if c.lastErr != nil {
		h.Err = c.lastErr.Error()
	}
	return h
}

// noteSuccess records a clean fetch, clearing any staleness.
func (c *Cached) noteSuccess() {
	c.healthMu.Lock()
	c.lastSuccess, c.lastErr, c.servingOld = c.now(), nil, false
	c.healthMu.Unlock()
}

// noteFallback records that err forced serving older data.
func (c *Cached) noteFallback(err error) {
	c.healthMu.Lock()
	c.lastErr, c.servingOld = err, true
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
			c.noteFallback(err)
			return c.reading, nil
		}
		return nil, err
	}
	c.noteSuccess()
	c.reading, c.readingAt, c.readingOK = entries, c.now(), true
	return entries, nil
}

// RecentReads serves the cached reads when fresh and requested with the same
// limit; otherwise it fetches once, holding the lock so concurrent callers wait
// and reuse the result.
func (c *Cached) RecentReads(ctx context.Context, limit int) ([]Entry, error) {
	c.readsMu.Lock()
	defer c.readsMu.Unlock()

	if c.readsLimit == limit && c.fresh(c.readsAt, c.readsOK) {
		return c.reads, nil
	}

	entries, err := c.src.RecentReads(ctx, limit)
	if err != nil {
		// A source that answered before is down, not gone: old data with a
		// visible staleness flag beats an error page.
		if c.readsOK {
			c.noteFallback(err)
			return c.reads, nil
		}
		return nil, err
	}
	c.noteSuccess()
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
			c.noteFallback(err)
			return c.toRead, nil
		}
		return nil, err
	}
	c.noteSuccess()
	c.toRead, c.toReadAt, c.toReadOK = entries, c.now(), true
	return entries, nil
}
