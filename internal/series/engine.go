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
	v, err := e.compute(ctx, budget, 0)
	return v, err
}

// compute builds the view and spends the lookup budget on it: first finding
// series for rows whose provider cannot look ahead, which changes what Compute
// sees, then asking what comes next.
func (e *Engine) compute(ctx context.Context, budget int, pause time.Duration) (View, error) {
	in, err := e.input(ctx)
	if err != nil {
		return View{}, err
	}
	v := Compute(in)
	if spent := e.discover(ctx, &v, budget, pause); spent > 0 {
		budget -= spent
		if in, err = e.input(ctx); err != nil {
			return View{}, err
		}
		v = Compute(in)
	}
	e.enrich(ctx, &v, budget, pause)
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

// continuation finds the same books' series on a provider that can look
// ahead: the one sharing the row's name when there is one, else the claim that
// provider ranks first.
func (e *Engine) continuation(g *Group) *Alternative {
	named := func(m library.Series) *Alternative {
		for i, alt := range g.Alternatives {
			if alt.Source == m.Source && key(alt.Name) == key(m.Name) {
				return &g.Alternatives[i]
			}
		}
		return nil
	}
	var first *Alternative
	for _, m := range g.memberships {
		if !library.ResolvesSeries(e.src, m.Source) {
			continue
		}
		alt := named(m)
		if alt == nil {
			continue
		}
		if key(m.Name) == key(g.Name) {
			return alt
		}
		if first == nil {
			first = alt
		}
	}
	return first
}

// discover looks up, by ISBN, the series of rows that have run out of shelf
// and have nowhere to continue, and of rows following a series found that way
// before. One lookup covers a row. It returns how many
// it spent; answers, including empty ones, are kept for a day.
func (e *Engine) discover(ctx context.Context, v *View, budget int, pause time.Duration) int {
	if len(e.finders) == 0 {
		return 0
	}
	spent := 0
	for i := range v.Groups {
		g := &v.Groups[i]
		if !e.unclaimed(g) && (!e.shelfOnly(g) || e.continuation(g) != nil) {
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
func (e *Engine) enrich(ctx context.Context, v *View, budget int, pause time.Duration) {
	spent := 0
	for i := range v.Groups {
		g := &v.Groups[i]
		if e.shelfOnly(g) {
			// As far as this provider can say, the reader is caught up.
			g.CaughtUp, g.ContinueOn = true, e.continuation(g)
			continue
		}
		if e.lookahead == nil || g.NextFromShelf || g.Decision == Dropped || g.Position == nil || g.Completed && g.CaughtUp {
			continue
		}
		q := library.SeriesQuery{
			Series:          library.Series{Name: g.Name, Position: g.Position, Slug: g.Slug, Source: g.Source},
			IncludeNovellas: e.prefs.IncludeNovellas,
		}
		fresh := !e.lookahead.Cached(q)
		if fresh && spent >= budget {
			continue
		}
		if fresh && pause > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
		}
		entry, found, err := e.lookahead.Next(ctx, q)
		if err != nil {
			log.Printf("series: looking up the next book in %q: %v", g.Name, err)
			continue
		}
		if fresh {
			spent++
		}
		if !found {
			g.CaughtUp = true
			continue
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
}

// Warm walks every group without a shelf answer and fills the lookup cache,
// paced so it cannot trip the source's rate limit. Run at startup and daily.
func (e *Engine) Warm(ctx context.Context) {
	// Generous rather than exact: the budget only has to outlast the rows.
	if _, err := e.compute(ctx, 1<<20, warmPause); err != nil {
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
