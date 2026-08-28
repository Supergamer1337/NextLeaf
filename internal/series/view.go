package series

import (
	"sort"
	"strings"
	"time"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
)

// Group is one series the reader is in, as computed from the sources right
// now. Its field names mirror what the drawer template renders.
type Group struct {
	Name      string   // display membership's name
	Source    string   // backend asserting the display membership
	Slug      string   // display membership's own identifier, if any
	Completed bool     // the author has ended the series
	Position  *float64 // furthest slot read in the display ordering; nil if unplaced
	CoverURL  string   // cover of the furthest book read
	Decision  Decision // standing decision after spent statements expire

	// Next describes the next book: from the shelf when NextFromShelf, else
	// filled by the engine from a catalogue lookup.
	NextTitle     string
	NextCoverURL  string
	NextURL       string
	NextPosition  float64
	NextKey       string // book key of the next book, for pinning
	NextEntry     *library.Entry
	NextFromShelf bool
	// CaughtUp is set by the engine when a lookup says nothing is left.
	CaughtUp bool

	Alternatives []Alternative

	// BookKeys identifies the books in this group; statements anchor to them.
	BookKeys []string
	// LastFinished orders groups by recency; zero when only in progress.
	LastFinished time.Time
	// Reading marks a group with an in-progress book.
	Reading bool
	// LastRating is how the reader rated the most recent finish, 0 unrated.
	LastRating float64

	readKeys    map[string]bool
	readingKeys map[string]bool
	memberships []library.Series
	books       []*book
	pinnedBook  string
	pinMadeAt   time.Time
}

// Alternative is another series the same books belong to, offered for
// switching. Position is where the reader sits in that ordering.
type Alternative struct {
	Name        string
	Source      string
	Description string
	Position    *float64
	// CoverURL is the face the row would wear if tracked under this
	// identity: the cover of the furthest book read in that ordering.
	CoverURL string
}

// View is the computed state of every series the reader has read into.
type View struct {
	Groups []Group
	// FinishedCount anchors new parks: finishing one more book spends them.
	FinishedCount int
}

// Input is everything the pure computation needs.
type Input struct {
	Reads      []library.Entry
	Reading    []library.Entry
	ToRead     []library.Entry
	Statements []Statement
	Prefs      picker.Prefs
	// SourceOrder is the configured source order, the stable tiebreak for
	// which backend's series claim a book is filed under. Fetch order must
	// never decide identity: it varies per book and would split one series
	// into per-source groups.
	SourceOrder []string
}

// book is one logical book after cross-source merging: the same title read on
// two backends is one book with both backends' series claims.
type book struct {
	key         string
	memberships []library.Series
	read        bool
	reading     bool
	toRead      bool
	finishedAt  time.Time
	addedAt     time.Time
	rating      float64
	cover       string
	entry       library.Entry
	tbrEntry    library.Entry
}

// Compute derives the series view. It is a pure function: same inputs, same
// view, nothing written anywhere.
func Compute(in Input) View {
	books := mergeBooks(in)

	finished := 0
	for _, b := range books {
		if b.read {
			finished++
		}
	}

	groups := buildGroups(books, in.Statements)
	applyStatements(groups, books, in.Statements, finished)

	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		finish(g, books, in.Prefs)
		out = append(out, *g)
	}

	// Two backends using the same name are probably one series, but names
	// never fuse identities here — only the reader can. Offer each group as
	// the other's switch target, so one click folds them.
	for i := range out {
		for j := range out {
			if i == j || key(out[i].Name) != key(out[j].Name) || out[i].Source == out[j].Source {
				continue
			}
			twin := Alternative{
				Name:        out[j].Name,
				Source:      out[j].Source,
				Description: "Tracked separately by " + out[j].Source + ". Switching folds the two rows into one.",
				Position:    out[j].Position,
				CoverURL:    out[j].CoverURL,
			}
			exists := false
			for _, alt := range out[i].Alternatives {
				if alt.Source == twin.Source && key(alt.Name) == key(twin.Name) {
					exists = true
				}
			}
			if !exists {
				out[i].Alternatives = append(out[i].Alternatives, twin)
			}
		}
	}

	sortGroups(out)
	return View{Groups: out, FinishedCount: finished}
}

// mergeBooks folds the three lists into logical books keyed by BookKey,
// unioning series claims across sources.
func mergeBooks(in Input) []*book {
	var order []*book
	byKey := map[string]*book{}

	add := func(e library.Entry, role string) {
		k := library.BookKey(e)
		if k == "" {
			return
		}
		b, ok := byKey[k]
		if !ok {
			b = &book{key: k, entry: e}
			byKey[k] = b
			order = append(order, b)
		}
		for _, m := range memberships(e.Book) {
			if !hasMembership(b.memberships, m) {
				b.memberships = append(b.memberships, m)
			}
		}
		switch role {
		case "read":
			b.read = true
			if e.FinishedAt.After(b.finishedAt) {
				b.finishedAt = e.FinishedAt
			}
			if b.rating == 0 {
				b.rating = e.Rating
			}
		case "reading":
			b.reading = true
		case "toread":
			b.toRead = true
			b.tbrEntry = e
			if b.addedAt.IsZero() || (!e.DateAdded.IsZero() && e.DateAdded.Before(b.addedAt)) {
				b.addedAt = e.DateAdded
			}
		}
		if b.cover == "" {
			b.cover = e.Book.CoverURL
		}
	}

	for _, e := range in.Reads {
		add(e, "read")
	}
	for _, e := range in.Reading {
		add(e, "reading")
	}
	for _, e := range in.ToRead {
		add(e, "toread")
	}

	rank := func(source string) int {
		for i, name := range in.SourceOrder {
			if name == source {
				return i
			}
		}
		return len(in.SourceOrder)
	}
	for _, b := range order {
		// Stable, so each source's own ranking of its claims is kept.
		sort.SliceStable(b.memberships, func(i, j int) bool {
			ri, rj := rank(b.memberships[i].Source), rank(b.memberships[j].Source)
			if ri != rj {
				return ri < rj
			}
			return false
		})
	}
	return order
}

func memberships(b library.Book) []library.Series {
	if b.Series == nil {
		return nil
	}
	out := make([]library.Series, 0, 1+len(b.OtherSeries))
	out = append(out, *b.Series)
	return append(out, b.OtherSeries...)
}

func hasMembership(list []library.Series, m library.Series) bool {
	for _, have := range list {
		if have.Source == m.Source && key(have.Name) == key(m.Name) {
			return true
		}
	}
	return false
}

func groupKey(m library.Series) string { return m.Source + "\x00" + key(m.Name) }

// buildGroups assigns each read or in-progress book to the group of its
// primary series claim, honouring prefer statements, and unions groups the
// reader has said are the same.
func buildGroups(books []*book, statements []Statement) map[string]*Group {
	// The latest prefer statement covering a book decides its primary claim.
	prefer := map[string]library.Series{} // book key -> preferred membership
	uf := newUnionFind()
	for _, st := range statements {
		if st.Kind != KindPrefer {
			continue
		}
		for _, anchor := range st.Anchors {
			prefer[anchor] = library.Series{Source: st.PrefSource, Name: st.PrefName}
		}
	}

	groups := map[string]*Group{}
	ensure := func(m library.Series) *Group {
		k := uf.find(groupKey(m))
		g, ok := groups[k]
		if !ok {
			g = &Group{
				Name: m.Name, Source: m.Source, Slug: m.Slug, Completed: m.Completed,
				readKeys: map[string]bool{}, readingKeys: map[string]bool{},
			}
			groups[k] = g
		}
		return g
	}

	// A shared book carrying the same series name from two backends, at the
	// same slot, is positive evidence the two identities are one series —
	// this is the one cross-source join made without the reader saying so.
	// Same-source claims never join this way (a franchise and its sub-series
	// are genuinely distinct), and a slot disagreement blocks it: differing
	// numbering schemes fused would corrupt the read-set.
	for _, b := range books {
		for x := 0; x < len(b.memberships); x++ {
			for y := x + 1; y < len(b.memberships); y++ {
				mx, my := b.memberships[x], b.memberships[y]
				if mx.Source == my.Source || key(mx.Name) != key(my.Name) {
					continue
				}
				px, okx := mx.Slot()
				py, oky := my.Slot()
				if okx && oky && px != py {
					continue
				}
				uf.union(groupKey(mx), groupKey(my))
			}
		}
	}

	// Union first, so books land in already-joined classes.
	for _, st := range statements {
		if st.Kind != KindPrefer {
			continue
		}
		target := groupKey(library.Series{Source: st.PrefSource, Name: st.PrefName})
		for _, b := range books {
			for _, anchor := range st.Anchors {
				if b.key != anchor {
					continue
				}
				for _, m := range b.memberships {
					uf.union(groupKey(m), target)
				}
			}
		}
	}

	for _, b := range books {
		if len(b.memberships) == 0 || (!b.read && !b.reading) {
			continue
		}
		prim := b.memberships[0]
		if want, ok := prefer[b.key]; ok {
			for _, m := range b.memberships {
				if m.Source == want.Source && key(m.Name) == key(want.Name) {
					prim = m
					break
				}
			}
		}
		g := ensure(prim)
		g.books = append(g.books, b)
		g.BookKeys = append(g.BookKeys, b.key)
		g.memberships = appendMemberships(g.memberships, b.memberships)
		if b.read {
			g.readKeys[b.key] = true
			if b.finishedAt.After(g.LastFinished) {
				g.LastFinished = b.finishedAt
				g.LastRating = b.rating
			}
		}
		if b.reading {
			g.Reading = true
			g.readingKeys[b.key] = true
		}
	}

	// The latest prefer naming a class decides how it is displayed.
	for _, st := range statements {
		if st.Kind != KindPrefer {
			continue
		}
		k := uf.find(groupKey(library.Series{Source: st.PrefSource, Name: st.PrefName}))
		if g, ok := groups[k]; ok {
			g.Name, g.Source = st.PrefName, st.PrefSource
			for _, m := range g.memberships {
				if m.Source == st.PrefSource && key(m.Name) == key(st.PrefName) {
					g.Slug, g.Completed = m.Slug, m.Completed
				}
			}
		}
	}
	return groups
}

func appendMemberships(have, more []library.Series) []library.Series {
	for _, m := range more {
		if !hasMembership(have, m) {
			have = append(have, m)
		}
	}
	return have
}

// applyStatements works out each group's standing decision: the latest
// statement whose anchors touch the group (or whose name matches, for
// statements predating anchors), with spent statements expiring by predicate
// rather than by anyone editing them.
func applyStatements(groups map[string]*Group, books []*book, statements []Statement, finished int) {
	// Anchors that exist nowhere any more (books gone, or an older key
	// scheme) cannot veto a statement: its name takes over as the matcher.
	live := map[string]bool{}
	for _, b := range books {
		live[b.key] = true
	}

	var latestPin *Group
	var latestPinAt time.Time
	for _, st := range statements {
		if st.Kind == KindPrefer {
			continue
		}
		for _, g := range groups {
			if !applies(st, g, live) {
				continue
			}
			switch st.Kind {
			case KindClear:
				g.Decision = Active
			case KindPark:
				// The park is spent once anything else has been finished.
				if finished > st.ParkCount {
					g.Decision = Active
				} else {
					g.Decision = Parked
				}
			case KindDrop:
				// Adding one of the series' books back undoes the drop.
				undone := false
				for _, b := range books {
					if b.toRead && b.addedAt.After(st.MadeAt) && inGroup(b, g) {
						undone = true
					}
				}
				if undone {
					g.Decision = Active
				} else {
					g.Decision = Dropped
				}
			case KindPin:
				g.Decision = Pinned
				g.pinnedBook, g.pinMadeAt = st.PinnedBook, st.MadeAt
			}
		}
	}

	for _, g := range groups {
		if g.Decision != Pinned {
			continue
		}
		// Reaching the pinned book spends the pin.
		if g.pinnedBook != "" && (g.readKeys[g.pinnedBook] || g.readingKeys[g.pinnedBook]) {
			g.Decision = Active
			continue
		}
		if g.pinnedBook == "" && g.LastFinished.After(g.pinMadeAt) {
			g.Decision = Active
			continue
		}
		if latestPin == nil || g.pinMadeAt.After(latestPinAt) {
			latestPin, latestPinAt = g, g.pinMadeAt
		}
	}
	// Only one series is pinned at a time: the latest pin wins.
	for _, g := range groups {
		if g.Decision == Pinned && g != latestPin {
			g.Decision = Active
		}
	}
}

// applies reports whether a statement is about this group: shared anchor
// books first, display-name match as the fallback when the statement has no
// anchors — or none of its anchors exist anywhere any more.
func applies(st Statement, g *Group, live map[string]bool) bool {
	anchorsAlive := false
	for _, anchor := range st.Anchors {
		if live[anchor] {
			anchorsAlive = true
		}
		for _, k := range g.BookKeys {
			if anchor == k {
				return true
			}
		}
	}
	if !anchorsAlive && st.Name != "" {
		if key(st.Name) == key(g.Name) {
			return true
		}
		for _, m := range g.memberships {
			if key(st.Name) == key(m.Name) {
				return true
			}
		}
	}
	return false
}

// inGroup reports whether a book claims membership in any of g's series.
func inGroup(b *book, g *Group) bool {
	for _, m := range b.memberships {
		for _, gm := range g.memberships {
			if m.Source == gm.Source && key(m.Name) == key(gm.Name) {
				return true
			}
		}
	}
	return false
}

// posIn returns a book's slot in the given series identity, falling back to
// its primary claim's slot when it has none there — two orderings number the
// same book differently, and an approximate slot beats none.
func posIn(b *book, source, name string) (float64, bool) {
	for _, m := range b.memberships {
		if m.Source == source && key(m.Name) == key(name) {
			if m.Position == nil {
				return 0, false
			}
			return *m.Position, true
		}
	}
	if len(b.memberships) > 0 && b.memberships[0].Position != nil {
		return *b.memberships[0].Position, true
	}
	return 0, false
}

// isNovella treats a half slot (3.5) as side material between two novels.
func isNovella(pos float64) bool {
	whole := float64(int64(pos))
	return pos-whole == 0.5 || whole-pos == 0.5
}

// finish derives the group's presentation: furthest position and cover in the
// display ordering, the next unread book on the shelf, and the alternatives.
// "Next" is the earliest unread slot, not the slot after the furthest — a
// prequel published behind the reader is simply an unread book, and so is a
// volume the reader skipped.
func finish(g *Group, books []*book, prefs picker.Prefs) {
	readSlots := map[float64]bool{}
	var furthest *float64
	var furthestBook, latestBook *book
	var latestFinish time.Time
	for _, b := range g.books {
		if !b.read && !b.reading {
			continue
		}
		if b.read && b.finishedAt.After(latestFinish) {
			latestFinish, latestBook = b.finishedAt, b
		}
		pos, placed := posIn(b, g.Source, g.Name)
		if !placed {
			continue
		}
		readSlots[pos] = true
		if furthest == nil || pos > *furthest {
			v := pos
			furthest, furthestBook = &v, b
		}
	}
	g.Position = furthest
	switch {
	case furthestBook != nil && furthestBook.cover != "":
		g.CoverURL = furthestBook.cover
	case latestBook != nil:
		g.CoverURL = latestBook.cover
	case len(g.books) > 0:
		g.CoverURL = g.books[0].cover
	}

	// The earliest unread shelved volume is next, wherever it sits. A dropped
	// series offers nothing, so it gets no next book.
	var next *book
	var nextPos float64
	for _, b := range books {
		if g.Decision == Dropped {
			break
		}
		if !b.toRead || !inGroup(b, g) {
			continue
		}
		pos, placed := posIn(b, g.Source, g.Name)
		if !placed || readSlots[pos] {
			continue
		}
		if !prefs.IncludeNovellas && isNovella(pos) {
			continue
		}
		if next == nil || pos < nextPos {
			next, nextPos = b, pos
		}
	}
	if next != nil {
		entry := next.tbrEntry
		g.NextEntry = &entry
		g.NextFromShelf = true
		g.NextKey = next.key
		g.NextTitle = entry.Book.Title
		g.NextCoverURL = entry.Book.CoverURL
		g.NextURL = entry.Book.URL
		g.NextPosition = nextPos
	}

	// Every other series these books belong to is an alternative home. Another
	// backend's claim under the same name is not one: switching to it would
	// change nothing the reader can see. (A same-named series that is still a
	// separate row is different — the fold offer for those is added later.)
	seen := map[string]bool{groupKey(library.Series{Source: g.Source, Name: g.Name}): true}
	for _, m := range g.memberships {
		k := groupKey(m)
		if seen[k] || key(m.Name) == key(g.Name) {
			continue
		}
		seen[k] = true
		alt := Alternative{Name: m.Name, Source: m.Source, Description: m.Description}
		var at *float64
		for _, b := range g.books {
			if !b.read {
				continue
			}
			for _, bm := range b.memberships {
				if bm.Source == m.Source && key(bm.Name) == key(m.Name) && bm.Position != nil {
					if at == nil || *bm.Position > *at {
						at = bm.Position
						if b.cover != "" {
							alt.CoverURL = b.cover
						}
					}
				}
			}
		}
		alt.Position = at
		if alt.CoverURL == "" {
			alt.CoverURL = g.CoverURL
		}
		g.Alternatives = append(g.Alternatives, alt)
	}
	sort.SliceStable(g.Alternatives, func(i, j int) bool {
		return g.Alternatives[i].Name < g.Alternatives[j].Name
	})
}

func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		gi, gj := groups[i], groups[j]
		if (gi.Decision == Pinned) != (gj.Decision == Pinned) {
			return gi.Decision == Pinned
		}
		if !gi.LastFinished.Equal(gj.LastFinished) {
			return gi.LastFinished.After(gj.LastFinished)
		}
		if gi.Reading != gj.Reading {
			return gi.Reading
		}
		return strings.ToLower(gi.Name) < strings.ToLower(gj.Name)
	})
}

// unionFind joins group keys the reader has said mean the same series.
type unionFind struct{ parent map[string]string }

func newUnionFind() *unionFind { return &unionFind{parent: map[string]string{}} }

func (u *unionFind) find(k string) string {
	p, ok := u.parent[k]
	if !ok || p == k {
		return k
	}
	root := u.find(p)
	u.parent[k] = root
	return root
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}
