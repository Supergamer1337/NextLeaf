package series

import (
	"context"
	"fmt"
	"log"
	"math/rand"
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
}

// NewEngine wires the engine to its collaborators. src's optional
// SeriesResolver capability, when present, powers new-release lookups.
func NewEngine(store *Store, src library.Source, prefs picker.Prefs) *Engine {
	e := &Engine{src: src, store: store, prefs: prefs, now: time.Now}
	if resolver, ok := library.AsSeriesResolver(src); ok {
		e.lookahead = NewLookahead(resolver, lookaheadTTL)
	}
	return e
}

// View computes the current series view and enriches it with cached catalogue
// answers, spending at most maxRequestLookups fresh queries.
func (e *Engine) View(ctx context.Context) (View, error) {
	in, err := e.input(ctx)
	if err != nil {
		return View{}, err
	}
	v := Compute(in)
	e.enrich(ctx, &v, maxRequestLookups, 0)
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
		Reads: reads, Reading: reading, ToRead: toRead,
		Statements: statements, Prefs: e.prefs, SourceOrder: e.SourceOrder,
	}, nil
}

// enrich fills in what only a catalogue can know: the next book beyond the
// shelf, and whether the reader is caught up. Failed lookups leave a group
// unknown rather than wrongly finished.
func (e *Engine) enrich(ctx context.Context, v *View, budget int, pause time.Duration) {
	if e.lookahead == nil {
		return
	}
	spent := 0
	for i := range v.Groups {
		g := &v.Groups[i]
		if g.NextFromShelf || g.Decision == Dropped || g.Position == nil || g.Completed && g.CaughtUp {
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
		if entry.Book.Series != nil && entry.Book.Series.Position != nil {
			g.NextPosition = *entry.Book.Series.Position
		}
	}
}

// Warm walks every group without a shelf answer and fills the lookup cache,
// paced so it cannot trip the source's rate limit. Run at startup and daily.
func (e *Engine) Warm(ctx context.Context) {
	in, err := e.input(ctx)
	if err != nil {
		log.Printf("series warm: %v", err)
		return
	}
	v := Compute(in)
	e.enrich(ctx, &v, len(v.Groups)+1, warmPause)
}

// Recommendation is the engine's pick together with the group a decision on
// the card would apply to, if any.
type Recommendation struct {
	Rec       picker.Recommendation
	OK        bool
	Decidable bool
	// Group is the display name decisions should be recorded against, which
	// is not always the series the book itself is labelled with.
	Group string
}

// Recommend produces one recommendation: continue the series that ranks
// highest (initial load), or a variety-weighted pick (reroll, or when no
// series has anything to offer). It also returns the view it worked from, so
// the caller renders exactly what was decided on.
func (e *Engine) Recommend(ctx context.Context, reroll bool) (Recommendation, View, error) {
	v, err := e.View(ctx)
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
			return Recommendation{Rec: rec, OK: true, Decidable: true, Group: g.Name}, v, nil
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
// the alternative the reader wants the series tracked under.
func (e *Engine) Decide(ctx context.Context, action, name, to string) error {
	v, err := e.View(ctx)
	if err != nil {
		return err
	}
	var group *Group
	for i := range v.Groups {
		if key(v.Groups[i].Name) == key(name) {
			group = &v.Groups[i]
			break
		}
	}
	if group == nil {
		return fmt.Errorf("%w: no tracked series named %q", ErrUnknownSeries, name)
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
	case "switch":
		alt, ok := alternativeNamed(group, to)
		if !ok {
			return fmt.Errorf("%w: %q is not an alternative of %q", ErrNotAnAlternative, to, name)
		}
		st.Kind, st.PrefSource, st.PrefName, st.Name = KindPrefer, alt.Source, alt.Name, alt.Name
	default:
		return fmt.Errorf("%w: %q", ErrUnknownAction, action)
	}
	return e.store.Append(ctx, st)
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
