package series

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
)

const (
	// lookaheadTTL is how long a next-in-series answer is reused. A book is
	// published at most once a day, so asking more often only burns rate limit.
	lookaheadTTL = 24 * time.Hour
	// maxRequestLookups caps catalogue lookups on the request path; the warm
	// pass fills the rest in the background.
	maxRequestLookups = 4
	// warmPause spaces the warm pass's lookups so a reader tracked in many
	// series stays a background trickle the source will not throttle.
	warmPause = 1500 * time.Millisecond
	// anchorCap bounds how many book keys a statement records.
	anchorCap = 8
	// cacheKeep is how long a kept answer outlives the last time it was asked
	// for; past that, the question is one nothing will ask again.
	cacheKeep = 30 * 24 * time.Hour
)

// Engine computes the series view from the sources and the statement log, and
// records new statements. It owns the only background machinery left: the
// disposable next-in-series cache.
type Engine struct {
	src       library.Source
	store     *Store
	prefs     picker.Prefs
	lookahead *Lookahead
	now       func() time.Time
	rng       *rand.Rand
	// SourceOrder is the configured source order (see Input.SourceOrder).
	SourceOrder []string

	// finders and found power the offer to continue a catalogue-less
	// provider's series on one that has a catalogue. found is disposable, like
	// the lookahead: series claims looked up by ISBN, per plain book key.
	finders []library.SeriesFinder
	mu      sync.Mutex
	found   map[string]foundClaims
	// findFailed holds the book keys whose last lookup failed: no answer is
	// coming for them until a later pass asks again.
	findFailed map[string]bool

	// gen counts answers landed; changed is closed when the next one does.
	gen     uint64
	changed chan struct{}
	nudge   chan struct{}

	// The reader's library is refreshed ahead of need (see RefreshLibrary):
	// libAt is when a refresh last finished, libGen counts the ones that
	// changed something, and libDone is closed when the one in flight ends.
	libMu   sync.Mutex
	libAt   time.Time
	libGen  uint64
	libDone chan struct{}
	// pace spaces the background pass's lookups; retryGap is the least time
	// between passes (see Run). Fields so tests need not wait on them.
	pace, retryGap time.Duration
}

type foundClaims struct {
	claims []library.Series
	at     time.Time
}

// NewEngine wires the engine to its collaborators. src's optional
// SeriesResolver capability, when present, powers new-release lookups.
func NewEngine(store *Store, src library.Source, prefs picker.Prefs) *Engine {
	e := &Engine{
		src: src, store: store, prefs: prefs, now: time.Now, found: map[string]foundClaims{}, findFailed: map[string]bool{},
		changed: make(chan struct{}), nudge: make(chan struct{}, 1),
		pace: warmPause, retryGap: failureTTL,
	}
	e.finders = library.AsSeriesFinders(src)
	if resolver, ok := library.AsSeriesResolver(src); ok {
		e.lookahead = NewLookahead(resolver, lookaheadTTL)
	}
	e.restore()
	return e
}

// restore picks up what an earlier process looked up, and keeps what this one
// does, so a restart shows the drawer as it was and asks nothing it knows.
func (e *Engine) restore() {
	if e.store == nil {
		return
	}
	ctx := context.Background()
	if err := e.store.PruneCache(ctx, e.now().Add(-cacheKeep)); err != nil {
		log.Printf("series: pruning the lookup cache: %v", err)
	}
	if e.lookahead != nil {
		kept, err := e.store.Answers(ctx)
		if err != nil {
			log.Printf("series: loading kept answers: %v", err)
		}
		e.lookahead.Load(kept)
		e.lookahead.save = func(k string, a CachedAnswer) {
			if err := e.store.SaveAnswer(context.Background(), k, a); err != nil {
				log.Printf("series: keeping an answer: %v", err)
			}
		}
	}
	kept, err := e.store.Claims(ctx)
	if err != nil {
		log.Printf("series: loading kept ISBN matches: %v", err)
	}
	for k, c := range kept {
		e.found[k] = foundClaims{claims: c.Claims, at: c.At}
	}
}

// View computes the current series view and enriches it with cached catalogue
// answers, spending at most maxRequestLookups fresh queries.
func (e *Engine) View(ctx context.Context) (View, error) {
	return e.viewWithin(ctx, maxRequestLookups)
}

// ViewCached is the view from answers already held, asking nothing new. It
// is what a drawer refresh shows: the background pass does the fetching.
func (e *Engine) ViewCached(ctx context.Context) (View, error) { return e.viewWithin(ctx, 0) }

// Changes returns how many answers have landed, and a channel closed when the
// next one does: the moment a render would show something new.
func (e *Engine) Changes() (uint64, <-chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.gen, e.changed
}

func (e *Engine) bump() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gen++
	close(e.changed)
	e.changed = make(chan struct{})
}

// Nudge asks the background pass to come round now: a render has answers
// still to come. Nudges made before a pass starts are covered by it.
func (e *Engine) Nudge() {
	select {
	case e.nudge <- struct{}{}:
	default:
	}
}

// RefreshLibrary fetches the reader's library afresh unless a refresh is
// already in flight, and returns a channel closed when it is done. Readers are
// served what is held meanwhile, so nothing waits on it unless it chooses to.
// A refresh that changes the library wakes waiting drawers, and nudges the
// background pass for any new questions it raises.
func (e *Engine) RefreshLibrary() <-chan struct{} {
	e.libMu.Lock()
	defer e.libMu.Unlock()
	if e.libDone != nil {
		return e.libDone
	}
	done := make(chan struct{})
	e.libDone = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		changed, err := library.Refresh(ctx, e.src)
		if err != nil {
			log.Printf("series: refreshing the library: %v", err)
		}
		e.libMu.Lock()
		e.libAt = e.now()
		if changed {
			e.libGen++
		}
		e.libDone = nil
		e.libMu.Unlock()
		close(done)
		if changed {
			e.bump()
			e.Nudge()
		}
	}()
	return done
}

// Library reports when the library was last refreshed, and how many
// refreshes have changed it.
func (e *Engine) Library() (at time.Time, gen uint64) {
	e.libMu.Lock()
	defer e.libMu.Unlock()
	return e.libAt, e.libGen
}

// LibraryRefreshing returns the channel of the refresh in flight, nil if none.
func (e *Engine) LibraryRefreshing() <-chan struct{} {
	e.libMu.Lock()
	defer e.libMu.Unlock()
	return e.libDone
}

// Nudged delivers a nudge not yet taken up; Run is what waits on it.
func (e *Engine) Nudged() <-chan struct{} { return e.nudge }

// Run keeps the library and the lookup cache fresh until ctx ends, so page
// loads read and never wait: a pass at once, then one every interval, and one
// whenever a render nudges. Nudged passes run at once but at least retryGap
// apart: a failure is held that long, so a sooner one could not answer
// anything new, and renders would otherwise nudge in a loop.
func (e *Engine) Run(ctx context.Context, every time.Duration) {
	var lastNudged time.Time
	for {
		e.Warm(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		case <-e.nudge:
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(lastNudged.Add(e.retryGap))):
			}
			lastNudged = time.Now()
		}
	}
}

// viewWithin is View with the lookup budget named. A budget of zero still uses
// every cached answer, so it costs nothing beyond the sources View reads anyway.
func (e *Engine) viewWithin(ctx context.Context, budget int) (View, error) {
	v, err := e.compute(ctx, budget, 0, false)
	return v, err
}

// compute builds the view and spends the lookup budget on it: first finding
// series for rows whose provider cannot look ahead, which changes what Compute
// sees, then asking what comes next. thorough, for the warm pass, looks up
// every row and every identity rather than only what this render needs.
func (e *Engine) compute(ctx context.Context, budget int, pause time.Duration, thorough bool) (View, error) {
	in, err := e.input(ctx)
	if err != nil {
		return View{}, err
	}
	v := Compute(in)
	if spent := e.discover(ctx, &v, budget, pause, thorough); spent > 0 {
		budget -= spent
		if in, err = e.input(ctx); err != nil {
			return View{}, err
		}
		v = Compute(in)
	}
	e.enrich(ctx, &v, budget, pause, thorough)
	return v, nil
}

func (e *Engine) input(ctx context.Context) (Input, error) {
	reads, err := e.src.RecentReads(ctx, 0)
	if err != nil {
		return Input{}, err
	}
	reading, err := e.src.CurrentlyReading(ctx)
	if err != nil {
		return Input{}, err
	}
	toRead, err := e.src.ToRead(ctx)
	if err != nil {
		return Input{}, err
	}
	statements, err := e.store.Statements(ctx)
	if err != nil {
		return Input{}, err
	}
	return Input{
		Reads: e.withFound(reads), Reading: e.withFound(reading), ToRead: toRead,
		Statements: statements, Prefs: e.prefs, SourceOrder: e.SourceOrder,
	}, nil
}

// withFound adds the series claims found by ISBN to the entries they were
// found for. Sources hand back retained slices, so entries are copied.
func (e *Engine) withFound(entries []library.Entry) []library.Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.found) == 0 {
		return entries
	}
	out := make([]library.Entry, len(entries))
	for i, entry := range entries {
		if f := e.found[library.BookKey(entry)]; len(f.claims) > 0 && entry.Book.Series != nil {
			others := make([]library.Series, 0, len(entry.Book.OtherSeries)+len(f.claims))
			others = append(others, entry.Book.OtherSeries...)
			entry.Book.OtherSeries = append(others, f.claims...)
		}
		out[i] = entry
	}
	return out
}

// shelfOnly reports whether a row has run out of shelf with no catalogue of
// its own to ask.
func (e *Engine) shelfOnly(g *Group) bool {
	return !g.NextFromShelf && g.Decision != Dropped && !library.ResolvesSeries(e.src, g.Source)
}

// unclaimed reports whether a row is displayed under an identity none of its
// books carries: the reader followed a series found by ISBN, and what was
// found has since been forgotten, as it is on every restart.
func (e *Engine) unclaimed(g *Group) bool {
	return !hasMembership(g.memberships, library.Series{Source: g.Source, Name: g.Name})
}

// candidate is a series the row could be continued under: the alternative the
// reader sees, and the claim behind it, which carries the provider's own
// identifier.
type candidate struct {
	claim library.Series
	alt   *Alternative
}

// candidates are the row's candidates on providers with a catalogue, in the
// order the offer prefers them: the claim sharing the row's name, then the
// provider's own ranking. Checking in any other order — the alternatives are
// sorted by name — spends the budget on candidates the offer would not take.
func (e *Engine) candidates(g *Group) []candidate {
	var same, rest []candidate
	for _, m := range g.memberships {
		if !library.ResolvesSeries(e.src, m.Source) {
			continue
		}
		alt := alternativeFor(g, m)
		if alt == nil {
			continue
		}
		c := candidate{claim: m, alt: alt}
		if key(m.Name) == key(g.Name) {
			same = append(same, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(same, rest...)
}

func (e *Engine) hasContinuation(g *Group) bool { return len(e.candidates(g)) > 0 }

// offer is the continuation for a row that has run out: the first candidate
// whose catalogue has confirmed a book past the reader's place. Unchecked
// candidates are never offered, since every series has a counterpart
// somewhere, finished ones included.
func (e *Engine) offer(g *Group) *Alternative {
	for _, c := range e.candidates(g) {
		if c.alt.NextTitle != "" {
			return c.alt
		}
	}
	return nil
}

// checkMode is how much a render may ask to fill in its candidates.
type checkMode int

const (
	cachedOnly    checkMode = iota // ask nothing
	untilOffered                   // ask until one candidate leads somewhere
	everyIdentity                  // ask about all of them: the warm pass
)

// check fills in what each candidate holds next, for the wheel and the offer.
func (e *Engine) check(ctx context.Context, g *Group, spent *int, budget int, pause time.Duration, mode checkMode) {
	if e.lookahead == nil {
		return
	}
	offered := false
	for _, c := range e.candidates(g) {
		// Only the identity's own numbers place the reader in it; with none,
		// there is nothing to ask after and no answer is coming.
		_, pos := reachIn(g, c.claim.Source, c.claim.Name, true)
		if pos == nil {
			continue
		}
		q := library.SeriesQuery{
			Series: library.Series{
				Name: c.claim.Name, Position: pos, Slug: c.claim.Slug,
				Source: c.claim.Source, Completed: c.claim.Completed,
			},
			IncludeNovellas: e.prefs.IncludeNovellas,
		}
		ask := mode == everyIdentity || mode == untilOffered && !offered
		entry, found, held := e.lookup(ctx, q, spent, budget, pause, ask, mode == everyIdentity)
		if !held {
			c.alt.Pending = !e.lookahead.Failed(q)
			continue
		}
		c.alt.Checked = true
		if found {
			c.alt.NextTitle = entry.Book.Title
			if entry.Book.Series != nil {
				c.alt.NextPosition = entry.Book.Series.Position
			}
			offered = true
		}
	}
}

// lookup answers q from what is known, asking the catalogue if ask allows and
// the budget lasts. A known answer is shown even when it is due a re-check,
// and only a refresh — the background pass — spends a lookup re-checking one;
// a failed re-check leaves it standing. held is false only when nothing is
// known yet: an answer is still to come, unless the last try failed.
func (e *Engine) lookup(ctx context.Context, q library.SeriesQuery, spent *int, budget int, pause time.Duration, ask, refresh bool) (entry library.Entry, found, held bool) {
	last, lastFound, known := e.lookahead.Last(q)
	asked := false
	switch {
	case e.lookahead.Cached(q):
		// Fresh, or a failure still held: no round trip either way.
	case known && !refresh:
		return last, lastFound, true
	case !ask || *spent >= budget:
		return last, lastFound, known
	default:
		if pause > 0 {
			select {
			case <-ctx.Done():
				return last, lastFound, known
			case <-time.After(pause):
			}
		}
		// Charged even if it fails: a failure costs a round trip too.
		*spent++
		asked = true
		defer e.bump()
	}
	entry, found, err := e.lookahead.Next(ctx, q)
	if err != nil {
		if asked {
			log.Printf("series: asking %s about %q: %v", q.Series.Source, q.Series.Name, err)
		}
		return last, lastFound, known
	}
	return entry, found, true
}

func alternativeFor(g *Group, m library.Series) *Alternative {
	for i, alt := range g.Alternatives {
		if alt.Source == m.Source && key(alt.Name) == key(m.Name) {
			return &g.Alternatives[i]
		}
	}
	return nil
}

// unasked reports whether any of the row's books could be looked up by ISBN
// and never has been. A stale lookup does not count: its answer still shows.
// Nor does a failed one: no answer is coming until a later pass.
func (e *Engine) unasked(g *Group) bool {
	if len(e.finders) == 0 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, b := range g.books {
		if len(b.isbns) == 0 {
			continue
		}
		asked := false
		for _, k := range b.plainKeys {
			if _, ok := e.found[k]; ok || e.findFailed[k] {
				asked = true
			}
		}
		if !asked {
			return true
		}
	}
	return false
}

// discover looks up, by ISBN, the series a catalogue files a row's books
// under. One lookup covers a row; answers, including empty ones, are kept for
// a day, and it returns how many it spent. A request looks up only rows that
// have nowhere to continue or follow a series found this way before; the warm
// pass looks up every row, so any row's wheel offers every provider's series.
func (e *Engine) discover(ctx context.Context, v *View, budget int, pause time.Duration, thorough bool) int {
	if len(e.finders) == 0 {
		return 0
	}
	spent := 0
	for i := range v.Groups {
		g := &v.Groups[i]
		if g.Decision == Dropped {
			continue
		}
		// A kept row turns other orderings down, so it looks for none; but the
		// one it follows must still be found again once its match is lost.
		needed := e.unclaimed(g) || (!g.Kept && e.shelfOnly(g) && !e.hasContinuation(g))
		if !needed && (!thorough || g.Kept) {
			continue
		}
		var isbns []string
		var asking []*book
		e.mu.Lock()
		for _, b := range g.books {
			fresh := false
			for _, k := range b.plainKeys {
				if f, ok := e.found[k]; ok && e.now().Sub(f.at) < lookaheadTTL {
					fresh = true
				}
			}
			if !fresh && len(b.isbns) > 0 {
				isbns = append(isbns, b.isbns...)
				asking = append(asking, b)
			}
		}
		e.mu.Unlock()
		if len(asking) == 0 || spent >= budget {
			continue
		}
		if pause > 0 {
			select {
			case <-ctx.Done():
				return spent
			case <-time.After(pause):
			}
		}

		answers := map[string][]library.Series{}
		failed := false
		for _, f := range e.finders {
			got, err := f.SeriesByISBN(ctx, isbns)
			if err != nil {
				log.Printf("series: finding %q by ISBN: %v", g.Name, err)
				failed = true
				break
			}
			for isbn, claims := range got {
				answers[isbn] = append(answers[isbn], claims...)
			}
		}
		spent++
		if failed {
			e.mu.Lock()
			for _, b := range asking {
				for _, k := range b.plainKeys {
					e.findFailed[k] = true
				}
			}
			e.mu.Unlock()
			continue
		}

		e.mu.Lock()
		for _, b := range asking {
			var claims []library.Series
			for _, isbn := range b.isbns {
				if claims = answers[isbn]; len(claims) > 0 {
					break
				}
			}
			inferred := make([]library.Series, len(claims))
			for j, c := range claims {
				c.Inferred = true
				inferred[j] = c
			}
			for _, k := range b.plainKeys {
				e.found[k] = foundClaims{claims: inferred, at: e.now()}
				if e.store != nil {
					if err := e.store.SaveClaims(ctx, k, inferred, e.now()); err != nil {
						log.Printf("series: keeping ISBN matches: %v", err)
					}
				}
			}
		}
		e.mu.Unlock()
		e.bump()
	}
	return spent
}

// enrich fills in what only a catalogue can know: the next book beyond the
// shelf, and whether the reader is caught up. A row is only ever looked up in
// its own provider's catalogue; a dropped one not at all, and a kept one not
// about continuing elsewhere.
//
// The budget goes where the reader needs it first: every row's own next book,
// so one row's wheel never costs another row its content; then where a row
// that has run out can be continued; then the rest of the wheel.
func (e *Engine) enrich(ctx context.Context, v *View, budget int, pause time.Duration, thorough bool) {
	spent := 0
	rows := func(want func(*Group) bool, do func(*Group)) {
		for i := range v.Groups {
			if g := &v.Groups[i]; g.Decision != Dropped && want(g) {
				do(g)
			}
		}
	}
	stuck, wheel := untilOffered, cachedOnly
	if thorough {
		stuck, wheel = everyIdentity, everyIdentity
	}

	rows(func(*Group) bool { return true }, func(g *Group) {
		if e.shelfOnly(g) {
			// As far as this provider can say, the reader is caught up.
			g.CaughtUp = true
			return
		}
		e.next(ctx, g, &spent, budget, pause, thorough)
	})
	rows(e.shelfOnly, func(g *Group) {
		if g.Kept {
			return
		}
		g.finding = !e.hasContinuation(g) && e.unasked(g)
		e.check(ctx, g, &spent, budget, pause, stuck)
		g.ContinueOn = e.offer(g)
	})
	rows(func(g *Group) bool { return !e.shelfOnly(g) && !g.Kept }, func(g *Group) {
		e.check(ctx, g, &spent, budget, pause, wheel)
	})
	e.fillTwins(v)
}

// next fills the row's own next book from its provider's catalogue. With
// nothing known yet, the row is pending rather than wrongly finished.
func (e *Engine) next(ctx context.Context, g *Group, spent *int, budget int, pause time.Duration, refresh bool) {
	if e.lookahead == nil || g.NextFromShelf || g.Position == nil {
		return
	}
	q := library.SeriesQuery{
		Series: library.Series{
			Name: g.Name, Position: g.Position, Slug: g.Slug,
			Source: g.Source, Completed: g.Completed,
		},
		IncludeNovellas: e.prefs.IncludeNovellas,
	}
	entry, found, held := e.lookup(ctx, q, spent, budget, pause, true, refresh)
	if !held {
		g.NextPending = !e.lookahead.Failed(q)
		return
	}
	if !found {
		g.CaughtUp = true
		return
	}
	clone := entry
	g.NextEntry = &clone
	g.NextKey = library.BookKey(entry)
	g.NextTitle = entry.Book.Title
	g.NextCoverURL = entry.Book.CoverURL
	g.NextURL = entry.Book.URL
	if entry.Book.Series != nil {
		g.NextPosition = entry.Book.Series.Position
	}
}

// fillTwins gives a twin — the same series name tracked as its own row on
// another provider — that row's answer. A row with no catalogue is "caught
// up" only because nobody could ask, so it speaks for nothing.
func (e *Engine) fillTwins(v *View) {
	for i := range v.Groups {
		g := &v.Groups[i]
		for j := range g.Alternatives {
			alt := &g.Alternatives[j]
			if alt.Checked {
				continue
			}
			for k := range v.Groups {
				twin := &v.Groups[k]
				if twin == g || twin.Source != alt.Source || key(twin.Name) != key(alt.Name) {
					continue
				}
				answered := twin.NextFromShelf || library.ResolvesSeries(e.src, twin.Source)
				switch {
				case answered && (twin.CaughtUp || twin.NextTitle != ""):
					alt.Checked = true
					alt.NextTitle, alt.NextPosition = twin.NextTitle, twin.NextPosition
				case twin.NextPending:
					alt.Pending = true
				}
			}
		}
	}
}

// Warm refreshes the library, then asks the catalogue whatever is new or due,
// paced so it cannot trip the source's rate limit. Even the first pass is
// paced: run flat out against Hardcover it tripped the limit within seconds.
func (e *Engine) Warm(ctx context.Context) {
	select { // this pass covers any nudge made before it
	case <-e.nudge:
	default:
	}
	select {
	case <-e.RefreshLibrary():
	case <-ctx.Done():
		return
	}
	pass, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	before, _ := e.Changes()
	// Generous rather than exact: the budget only has to outlast the rows.
	if _, err := e.compute(pass, 1<<20, e.pace, true); err != nil {
		log.Printf("series warm: %v", err)
	}
	if after, _ := e.Changes(); after != before {
		log.Print("series lookups refreshed")
	}
	// Whatever is still outstanding now waits on a later pass, and a drawer
	// waiting on this one should hear that it is over.
	e.bump()
}

// Recommendation is the engine's pick together with the group a decision on
// the card would apply to, if any.
type Recommendation struct {
	Rec       picker.Recommendation
	OK        bool
	Decidable bool
	// Continuation is true when the book is offered because it is the next in
	// a series, rather than drawn for variety. The card shows no free skip
	// past one: stepping over a series is a park, not a reroll.
	Continuation bool
	// Group is the display name decisions should be recorded against, which
	// is not always the series the book itself is labelled with.
	Group string
}

// Recommend produces one recommendation: continue the series that ranks
// highest (initial load), or a variety-weighted pick (reroll, or when no
// series has anything to offer). It also returns the view it worked from, so
// the caller renders exactly what was decided on.
func (e *Engine) Recommend(ctx context.Context, reroll bool) (Recommendation, View, error) {
	return e.RecommendWithin(ctx, reroll, maxRequestLookups)
}

// RecommendWithin is Recommend with the catalogue lookup budget named. The
// decision handler passes zero: a park or a drop is recorded and re-rendered
// without waiting on a catalogue that may be slow.
func (e *Engine) RecommendWithin(ctx context.Context, reroll bool, budget int) (Recommendation, View, error) {
	return e.recommend(ctx, reroll, budget, "")
}

// RecommendKeeping is RecommendWithin for a page already showing the book
// keyed keep: a series continuation still comes first, but in place of a new
// variety pick the card stays, if it is still on the list.
func (e *Engine) RecommendKeeping(ctx context.Context, keep string, budget int) (Recommendation, View, error) {
	return e.recommend(ctx, false, budget, keep)
}

func (e *Engine) recommend(ctx context.Context, reroll bool, budget int, keep string) (Recommendation, View, error) {
	v, err := e.viewWithin(ctx, budget)
	if err != nil {
		return Recommendation{}, View{}, err
	}

	if !reroll {
		for i := range v.Groups {
			g := &v.Groups[i]
			if g.Decision == Parked || g.Decision == Dropped || g.NextEntry == nil {
				continue
			}
			rec := picker.ContinueSeries(*g.NextEntry, g.LastRating)
			return Recommendation{Rec: rec, OK: true, Decidable: true, Continuation: true, Group: g.Name}, v, nil
		}
	}

	toRead, err := e.src.ToRead(ctx)
	if err != nil {
		return Recommendation{}, View{}, err
	}
	reads, err := e.src.RecentReads(ctx, picker.RecentWindow)
	if err != nil {
		return Recommendation{}, View{}, err
	}
	reading, err := e.src.CurrentlyReading(ctx)
	if err != nil {
		return Recommendation{}, View{}, err
	}

	// A dropped series is out of the running entirely, so its books leave the
	// variety pool too.
	candidates := withoutDropped(toRead, v.Groups)
	rec, ok := picker.Keep(e.prefs, candidates, reads, reading, keep)
	if !ok {
		rng := e.rng
		if rng == nil {
			rng = rand.New(rand.NewSource(e.now().UnixNano()))
		}
		rec, ok = picker.Pick(rng, e.prefs, candidates, reads, reading)
	}
	out := Recommendation{Rec: rec, OK: ok}
	if g, found := groupFor(rec.Entry, v.Groups); found {
		out.Decidable, out.Group = true, g.Name
	}
	return out, v, nil
}

func withoutDropped(toRead []library.Entry, groups []Group) []library.Entry {
	dropped := map[string]bool{}
	for _, g := range groups {
		if g.Decision != Dropped {
			continue
		}
		for _, m := range g.memberships {
			dropped[groupKey(m)] = true
		}
		dropped[groupKey(library.Series{Source: g.Source, Name: g.Name})] = true
	}
	if len(dropped) == 0 {
		return toRead
	}
	kept := make([]library.Entry, 0, len(toRead))
	for _, e := range toRead {
		excluded := false
		for _, m := range memberships(e.Book) {
			if dropped[groupKey(m)] {
				excluded = true
			}
		}
		if !excluded {
			kept = append(kept, e)
		}
	}
	return kept
}

// groupFor finds the group a book belongs to, if the reader has read into it.
func groupFor(entry library.Entry, groups []Group) (Group, bool) {
	k := library.BookKey(entry)
	for _, g := range groups {
		for _, bk := range g.BookKeys {
			if bk == k {
				return g, true
			}
		}
		for _, m := range memberships(entry.Book) {
			for _, gm := range g.memberships {
				if m.Source == gm.Source && key(m.Name) == key(gm.Name) {
					return g, true
				}
			}
		}
	}
	return Group{}, false
}

// Decide records a statement about the named series. For "switch", to names
// the alternative the reader wants the series tracked under. The returned
// uncached flag says the decision left the group with no cached next-in-series
// answer — a switch keys the cache under a new name, and a cleared drop
// revives a group enrich and Warm have been skipping — so re-rendering it
// needs a lookup budget.
//
// It works from the unenriched view: recording a statement needs the group
// and its anchors, never the catalogue, so a slow backend cannot stretch the
// POST the reader is waiting on.
func (e *Engine) Decide(ctx context.Context, action, name, to string) (uncached bool, err error) {
	in, err := e.input(ctx)
	if err != nil {
		return false, err
	}
	v := Compute(in)
	var group *Group
	for i := range v.Groups {
		if key(v.Groups[i].Name) == key(name) {
			group = &v.Groups[i]
			break
		}
	}
	if group == nil {
		return false, fmt.Errorf("%w: no tracked series named %q", ErrUnknownSeries, name)
	}

	st := Statement{MadeAt: e.now(), Name: group.Name, Anchors: anchors(group)}
	switch action {
	case "park":
		st.Kind, st.ParkCount = KindPark, v.FinishedCount
	case "drop":
		st.Kind = KindDrop
	case "pin":
		st.Kind, st.PinnedBook = KindPin, group.NextKey
	case "keep":
		st.Kind = KindKeep
	case "unkeep":
		st.Kind, uncached = KindUnkeep, true
	case "clear":
		st.Kind = KindClear
		uncached = group.Decision == Dropped
	case "switch":
		alt, ok := alternativeNamed(group, to)
		if !ok {
			return false, fmt.Errorf("%w: %q is not an alternative of %q", ErrNotAnAlternative, to, name)
		}
		st.Kind, st.PrefSource, st.PrefName, st.Name = KindPrefer, alt.Source, alt.Name, alt.Name
		uncached = true
	default:
		return false, fmt.Errorf("%w: %q", ErrUnknownAction, action)
	}
	if err := e.store.Append(ctx, st); err != nil {
		return false, err
	}
	// Whoever is listening — this tab's drawer, or another tab's — hears of
	// it at once, and a render made just before it is superseded.
	e.bump()
	return uncached, nil
}

// Sentinel errors let the web layer map refusals to the right status codes.
var (
	ErrUnknownSeries    = fmt.Errorf("unknown series")
	ErrNotAnAlternative = fmt.Errorf("not an alternative")
	ErrUnknownAction    = fmt.Errorf("unknown action")
)

// alternativeNamed finds the alternative the reader named, case-insensitively.
func alternativeNamed(g *Group, name string) (Alternative, bool) {
	for _, alt := range g.Alternatives {
		if key(alt.Name) == key(name) {
			return alt, true
		}
	}
	return Alternative{}, false
}

// anchors picks the book keys a statement is pinned to, favouring read books:
// they are the durable evidence of which series the reader meant.
func anchors(g *Group) []string {
	out := make([]string, 0, anchorCap)
	for _, k := range g.BookKeys {
		if g.readKeys[k] && len(out) < anchorCap {
			out = append(out, k)
		}
	}
	for _, k := range g.BookKeys {
		if !g.readKeys[k] && len(out) < anchorCap {
			out = append(out, k)
		}
	}
	return out
}
