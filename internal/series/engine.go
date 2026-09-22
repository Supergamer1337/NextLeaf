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
}

type foundClaims struct {
	claims []library.Series
	at     time.Time
}

// NewEngine wires the engine to its collaborators. src's optional
// SeriesResolver capability, when present, powers new-release lookups.
func NewEngine(store *Store, src library.Source, prefs picker.Prefs) *Engine {
	e := &Engine{src: src, store: store, prefs: prefs, now: time.Now, found: map[string]foundClaims{}}
	e.finders = library.AsSeriesFinders(src)
	if resolver, ok := library.AsSeriesResolver(src); ok {
		e.lookahead = NewLookahead(resolver, lookaheadTTL)
	}
	return e
}

// View computes the current series view and enriches it with cached catalogue
// answers, spending at most maxRequestLookups fresh queries.
func (e *Engine) View(ctx context.Context) (View, error) {
	return e.viewWithin(ctx, maxRequestLookups)
}

// viewWithin is View with the lookup budget named. A budget of zero still uses
// every cached answer, so it costs nothing beyond the sources View reads anyway.
func (e *Engine) viewWithin(ctx context.Context, budget int) (View, error) {
	v, err := e.compute(ctx, budget, 0, false)
	return v, err
}

// compute builds the view and spends the lookup budget on it: first finding
// series for rows whose provider cannot look ahead, which changes what Compute
// sees, then asking what comes next.
//
// thorough widens both passes from "what this render needs" to "everything a
// reader might open": every row's books looked up on every provider, and every
// identity they turn up asked what it holds next. It belongs to the warm pass,
// which is paced and has no reader waiting on it.
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

// candidate is a series the row could be continued or tracked under: the
// alternative the reader sees, with the claim behind it, since only the claim
// carries the identifier that provider's catalogue answers to.
type candidate struct {
	claim library.Series
	alt   *Alternative
}

// candidates are the row's continuation candidates in preference order: the
// claim sharing the row's name first, then the order its provider ranks them.
// offer, check and hasContinuation all read this one order, so a request never
// spends its budget on a candidate the offer would not have taken first —
// the alternatives themselves are sorted by name, for the wheel to read.
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

// hasContinuation reports whether a candidate exists at all, which is what
// decides whether a row still needs looking up by ISBN. Whether the candidate
// leads anywhere is offer's question, and costs a catalogue round trip.
func (e *Engine) hasContinuation(g *Group) bool { return len(e.candidates(g)) > 0 }

// offer is the continuation to show on a row that has run out: the first
// candidate whose provider has said it really holds a book past where the
// reader stands. An unchecked candidate is never offered — every series has a
// counterpart somewhere, finished ones included, and following one only
// renames the row that already said "nothing left". A candidate that leads
// nowhere is skipped rather than ending the search: the next one may be the
// sub-series the reader is actually following.
func (e *Engine) offer(g *Group) *Alternative {
	for _, c := range e.candidates(g) {
		if c.alt.NextTitle != "" {
			return c.alt
		}
	}
	return nil
}

// checkMode says how far a render may go to find out what each identity
// offers next.
type checkMode int

const (
	// cachedOnly uses answers already held and asks for nothing. What has not
	// been looked up stays unspoken.
	cachedOnly checkMode = iota
	// untilOffered asks in preference order and stops at the first identity
	// that leads somewhere — that is the one the arrow takes, and the reader
	// is looking at the row right now.
	untilOffered
	// everyIdentity asks about all of them, filling the wheel. It belongs to
	// the warm pass, which has nobody waiting on it.
	everyIdentity
)

// check fills each candidate with what that identity offers next, so the wheel
// names both destinations rather than making the reader switch to find out.
// Cached answers are always used; mode decides how much may be asked for.
func (e *Engine) check(ctx context.Context, g *Group, spent *int, budget int, pause time.Duration, mode checkMode) {
	if e.lookahead == nil {
		return
	}
	for _, c := range e.candidates(g) {
		if mode == untilOffered && e.offer(g) != nil {
			return
		}
		pos := furthestIn(g, c.claim.Source, c.claim.Name)
		if pos == nil {
			// Nothing to ask after: the row would switch into silence.
			continue
		}
		q := library.SeriesQuery{
			Series:          library.Series{Name: c.claim.Name, Position: pos, Slug: c.claim.Slug, Source: c.claim.Source},
			IncludeNovellas: e.prefs.IncludeNovellas,
		}
		if fresh := !e.lookahead.Cached(q); fresh {
			if mode == cachedOnly || *spent >= budget {
				continue
			}
			if pause > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(pause):
				}
			}
			// Charged whatever the answer: a failure is a round trip like any
			// other, and errors are never cached, so an uncharged one would be
			// retried on every render for as long as the backend stays down.
			*spent++
		}
		entry, found, err := e.lookahead.Next(ctx, q)
		if err != nil {
			log.Printf("series: checking what %q offers beyond %q: %v", c.claim.Source, g.Name, err)
			continue
		}
		c.alt.Checked = true
		if found {
			c.alt.NextTitle = entry.Book.Title
			if entry.Book.Series != nil {
				c.alt.NextPosition = entry.Book.Series.Position
			}
		}
	}
}

// alternativeFor finds the offered alternative matching a claim.
func alternativeFor(g *Group, m library.Series) *Alternative {
	for i, alt := range g.Alternatives {
		if alt.Source == m.Source && key(alt.Name) == key(m.Name) {
			return &g.Alternatives[i]
		}
	}
	return nil
}

// discover looks up, by ISBN, the series a catalogue files a row's books
// under. One lookup covers a row; answers, including empty ones, are kept for
// a day, and it returns how many it spent.
//
// A request only looks up what it needs to answer: rows that have run out of
// shelf with nowhere to continue, and rows following a series found this way
// before. The warm pass looks up every row, so that opening any row's switcher
// shows every identity every provider files those books under — the reader's
// choice is not limited to the provider they happen to be tracked on.
func (e *Engine) discover(ctx context.Context, v *View, budget int, pause time.Duration, thorough bool) int {
	if len(e.finders) == 0 {
		return 0
	}
	spent := 0
	for i := range v.Groups {
		g := &v.Groups[i]
		needed := e.unclaimed(g) || (e.shelfOnly(g) && !e.hasContinuation(g))
		if !needed && (!thorough || g.Decision == Dropped) {
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
			}
		}
		e.mu.Unlock()
	}
	return spent
}

// enrich fills in what only a catalogue can know: the next book beyond the
// shelf, and whether the reader is caught up. A row is only ever looked up in
// its own provider's catalogue. Failed lookups leave a group unknown rather
// than wrongly finished.
//
// The budget is spent in the order a reader needs the answers, not in the
// order the rows happen to sit. What each row holds next comes first, so one
// row's switcher is never filled at the cost of another row's content; then
// where a row that has run out can be continued, which is the one question
// such a row exists to answer; and last the rest of the wheel, which only the
// warm pass goes and fetches.
func (e *Engine) enrich(ctx context.Context, v *View, budget int, pause time.Duration, thorough bool) {
	spent := 0
	rows := func(want func(*Group) bool, do func(*Group)) {
		for i := range v.Groups {
			g := &v.Groups[i]
			// A dropped series offers nothing, so nothing is looked up for it.
			if g.Decision != Dropped && want(g) {
				do(g)
			}
		}
	}

	rows(func(g *Group) bool { return true }, func(g *Group) {
		if e.shelfOnly(g) {
			// As far as this provider can say, the reader is caught up.
			g.CaughtUp = true
			return
		}
		e.next(ctx, g, &spent, budget, pause)
	})

	mode := untilOffered
	if thorough {
		mode = everyIdentity
	}
	rows(e.shelfOnly, func(g *Group) {
		e.check(ctx, g, &spent, budget, pause, mode)
		g.ContinueOn = e.offer(g)
	})

	mode = cachedOnly
	if thorough {
		mode = everyIdentity
	}
	rows(func(g *Group) bool { return !e.shelfOnly(g) }, func(g *Group) {
		e.check(ctx, g, &spent, budget, pause, mode)
	})

	e.fillTwins(v)
}

// next fills the row's own next book from its provider's catalogue. A failed
// lookup leaves the row unknown rather than wrongly finished.
func (e *Engine) next(ctx context.Context, g *Group, spent *int, budget int, pause time.Duration) {
	if e.lookahead == nil || g.NextFromShelf || g.Decision == Dropped || g.Position == nil {
		return
	}
	q := library.SeriesQuery{
		Series:          library.Series{Name: g.Name, Position: g.Position, Slug: g.Slug, Source: g.Source},
		IncludeNovellas: e.prefs.IncludeNovellas,
	}
	fresh := !e.lookahead.Cached(q)
	if fresh && *spent >= budget {
		return
	}
	if fresh && pause > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
	}
	if fresh {
		// Charged whatever the answer; see check.
		*spent++
	}
	entry, found, err := e.lookahead.Next(ctx, q)
	if err != nil {
		log.Printf("series: looking up the next book in %q: %v", g.Name, err)
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

// fillTwins gives a twin alternative — the same series name tracked as its own
// row on another provider — the answer that row already has, rather than
// asking the same question twice under another key.
//
// Only a row that was actually answered may speak: a row on a provider with no
// catalogue is "caught up" merely because nobody could ask it, and passing
// that on would label a switch a dead end when it leads to a book.
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
				if answered && (twin.CaughtUp || twin.NextTitle != "") {
					alt.Checked = true
					alt.NextTitle, alt.NextPosition = twin.NextTitle, twin.NextPosition
				}
			}
		}
	}
}

// Warm walks every group and fills the lookup cache, paced so it cannot trip
// the source's rate limit. Run at startup and daily.
//
// It is tempting to let the first pass run flat out, since nothing is cached
// and a reader may be about to open the page. Measured against Hardcover's
// live API, that trips its limit within seconds: the lookups fail, the rows
// they were for lose their offers, and the retries land in the reader's page
// loads. A slow fill the reader never sees beats a fast one that breaks.
func (e *Engine) Warm(ctx context.Context) {
	// Generous rather than exact: the budget only has to outlast the rows.
	if _, err := e.compute(ctx, 1<<20, warmPause, true); err != nil {
		log.Printf("series warm: %v", err)
	}
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
	rng := e.rng
	if rng == nil {
		rng = rand.New(rand.NewSource(e.now().UnixNano()))
	}
	rec, ok := picker.Pick(rng, e.prefs, candidates, reads, reading)
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
	return uncached, e.store.Append(ctx, st)
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
