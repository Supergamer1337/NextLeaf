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
	Position  *float64 // furthest slot reached in the display ordering; nil if unplaced
	// PositionReading marks Position as a book still in progress rather than
	// one behind the reader.
	PositionReading bool
	// Length is how many books the series holds, 0 when no backend counts.
	Length   int
	CoverURL string   // cover of the furthest book read
	Decision Decision // standing decision after spent statements expire

	// Next describes the next book: from the shelf when NextFromShelf, else
	// filled by the engine from a catalogue lookup.
	NextTitle     string
	NextCoverURL  string
	NextURL       string
	NextPosition  *float64 // nil when the offer has no slot; 0 is a real slot
	NextKey       string   // book key of the next book, for pinning
	NextEntry     *library.Entry
	NextFromShelf bool
	// CaughtUp is set by the engine when a lookup says nothing is left.
	CaughtUp bool
	// NextPending marks a row whose next book is still to be looked up, so it
	// does not read as a row with nothing in it.
	NextPending bool
	// ContinueOn is set by the engine on a row whose shelf has run out and
	// whose provider has no catalogue to ask: the same series on a provider
	// that has one, once that catalogue has confirmed it holds a book past
	// where the reader stands. Taking it is an ordinary switch.
	ContinueOn *Alternative

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
	claims      []library.Series // each book's primary claim, for naming the row
	finding     bool             // an ISBN lookup that could find its series is still to come
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
	// PositionReading marks Position as a book still in progress. Only a twin
	// carries it: an alternative ordering is placed by finished books alone.
	PositionReading bool
	// CoverURL is the face the row would wear if tracked under this
	// identity: the cover of the furthest book read in that ordering.
	CoverURL string

	// NextTitle and NextPosition are what this identity's provider holds past
	// the reader's place in it, once Checked.
	NextTitle    string
	NextPosition *float64
	Checked      bool
	// Pending marks an answer still to come. An identity nobody can ask is
	// neither checked nor pending, and says nothing.
	Pending bool
}

// NextLabel says what this identity holds past the reader's place in it.
func (a Alternative) NextLabel() string {
	return nextLabel(a.Checked, a.Pending, a.NextTitle, a.NextPosition)
}

// Pending reports whether any of the row's answers is still to come: its own
// next book, what one of its other series holds, or which series those are.
func (g Group) Pending() bool {
	if g.NextPending || g.finding {
		return true
	}
	for _, alt := range g.Alternatives {
		if alt.Pending {
			return true
		}
	}
	return false
}

// NextLabel says what the row offers next, in the wheel's words.
func (g Group) NextLabel() string {
	return nextLabel(g.CaughtUp || g.NextTitle != "", g.NextPending, g.NextTitle, g.NextPosition)
}

// NextBook names what this identity holds next: its title and, when known,
// its slot.
func (a Alternative) NextBook() string { return nextBook(a.NextTitle, a.NextPosition) }

// Kept reports whether the reader keeps to this series as tracked.
func (g Group) Kept() bool { return g.Decision == Kept }

func nextLabel(checked, pending bool, title string, pos *float64) string {
	switch {
	case !checked && pending:
		return "Checking…"
	case !checked:
		return ""
	case title == "":
		return "Nothing left to read"
	default:
		return "Next: " + nextBook(title, pos)
	}
}

func nextBook(title string, pos *float64) string {
	if pos == nil {
		return title
	}
	return title + ", book " + formatPos(*pos)
}

// PositionLabel states where the reader stands in the series: a slot they have
// finished, or the one they are in the middle of. Empty when unplaced.
func (g Group) PositionLabel() string {
	if g.Position == nil {
		return ""
	}
	if g.PositionReading {
		return "reading book " + formatPos(*g.Position)
	}
	return "read to book " + formatPos(*g.Position)
}

// PositionLabel states where the reader stands under this identity.
func (a Alternative) PositionLabel() string {
	if a.Position == nil {
		return ""
	}
	if a.PositionReading {
		return "reading book " + formatPos(*a.Position)
	}
	return "read to book " + formatPos(*a.Position)
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
	plainKeys   []string // every description's own BookKey
	isbns       []string
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
	ix := library.NewKeyIndex(in.Reads, in.Reading, in.ToRead)
	in.Statements = followJoins(in.Statements, ix)
	books := mergeBooks(in, ix)

	finished := 0
	for _, b := range books {
		if b.read {
			finished++
		}
	}

	groups := buildGroups(books, in.Statements, sourceRank(in.SourceOrder))
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
				Name:            out[j].Name,
				Source:          out[j].Source,
				Description:     "Tracked separately by " + out[j].Source + ". Switching folds the two rows into one.",
				Position:        out[j].Position,
				PositionReading: out[j].PositionReading,
				CoverURL:        out[j].CoverURL,
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

// followJoins re-anchors statements to the shared keys of joined books. A
// statement is recorded against a copy's plain key, and must keep applying
// once that copy is folded into another.
func followJoins(statements []Statement, ix *library.KeyIndex) []Statement {
	out := make([]Statement, len(statements))
	for i, st := range statements {
		anchors := make([]string, len(st.Anchors))
		for j, a := range st.Anchors {
			anchors[j] = ix.Canonical(a)
		}
		st.Anchors = anchors
		st.PinnedBook = ix.Canonical(st.PinnedBook)
		out[i] = st
	}
	return out
}

// mergeBooks folds the three lists into logical books keyed by ix, unioning
// series claims across sources.
func mergeBooks(in Input, ix *library.KeyIndex) []*book {
	var order []*book
	byKey := map[string]*book{}

	add := func(e library.Entry, role string) {
		k := ix.Key(e)
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
		if plain := library.BookKey(e); !contains(b.plainKeys, plain) {
			b.plainKeys = append(b.plainKeys, plain)
		}
		for _, isbn := range e.Book.ISBNs {
			if !contains(b.isbns, isbn) {
				b.isbns = append(b.isbns, isbn)
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

	rank := sourceRank(in.SourceOrder)
	for _, b := range order {
		// Stable, so each source's own ranking of its claims is kept. A claim
		// the reader's shelf never made goes last: it is an offer, and must not
		// become the book's series by outranking the one they are following.
		sort.SliceStable(b.memberships, func(i, j int) bool {
			if b.memberships[i].Inferred != b.memberships[j].Inferred {
				return !b.memberships[i].Inferred
			}
			ri, rj := rank(b.memberships[i].Source), rank(b.memberships[j].Source)
			if ri != rj {
				return ri < rj
			}
			return false
		})
	}
	return order
}

// sourceRank orders sources as configured, unknown ones last.
func sourceRank(order []string) func(string) int {
	return func(source string) int {
		for i, name := range order {
			if name == source {
				return i
			}
		}
		return len(order)
	}
}

func contains(list []string, s string) bool {
	for _, have := range list {
		if have == s {
			return true
		}
	}
	return false
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
func buildGroups(books []*book, statements []Statement, rank func(string) int) map[string]*Group {
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
		g.claims = append(g.claims, prim)
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

	// A class is named by rule, never by which of its books was processed
	// first: that follows fetch order, and would rename a row as the reader
	// finishes books and claims arrive.
	for _, g := range groups {
		m := displayClaim(g.claims, rank)
		g.Name, g.Source, g.Slug, g.Completed = m.Name, m.Source, m.Slug, m.Completed
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

// displayClaim picks the name a class wears from its books' own claims: one
// the reader's shelf made over one found by ISBN, then the source ranked
// first, then the claim most of its books share, then by name.
func displayClaim(claims []library.Series, rank func(string) int) library.Series {
	count := map[string]int{}
	for _, m := range claims {
		count[groupKey(m)]++
	}
	best := claims[0]
	for _, m := range claims[1:] {
		switch {
		case m.Inferred != best.Inferred:
			if !m.Inferred {
				best = m
			}
		case rank(m.Source) != rank(best.Source):
			if rank(m.Source) < rank(best.Source) {
				best = m
			}
		case count[groupKey(m)] != count[groupKey(best)]:
			if count[groupKey(m)] > count[groupKey(best)] {
				best = m
			}
		case key(m.Name) < key(best.Name):
			best = m
		}
	}
	return best
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
			case KindKeep:
				g.Decision = Kept
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

// slotIn returns a book's slot in the given series identity. exact is false
// when the slot is borrowed from the book's primary claim instead.
func slotIn(b *book, source, name string) (pos float64, exact, placed bool) {
	for _, m := range b.memberships {
		if m.Source == source && key(m.Name) == key(name) {
			if m.Position == nil {
				return 0, true, false
			}
			return *m.Position, true, true
		}
	}
	if len(b.memberships) > 0 && b.memberships[0].Position != nil {
		return *b.memberships[0].Position, false, true
	}
	return 0, false, false
}

// posIn is slotIn for callers where a borrowed slot will do.
func posIn(b *book, source, name string) (float64, bool) {
	pos, _, placed := slotIn(b, source, name)
	return pos, placed
}

// reachIn finds how far the group's read and in-progress books reach in an
// ordering, and which book stands there. exactOnly ignores borrowed slots.
func reachIn(g *Group, source, name string, exactOnly bool) (*book, *float64) {
	var at *float64
	var who *book
	for _, b := range g.books {
		if !b.read && !b.reading {
			continue
		}
		pos, exact, placed := slotIn(b, source, name)
		if !placed || exactOnly && !exact {
			continue
		}
		// A finished book owns a slot it shares with an in-progress one.
		tie := at != nil && pos == *at && b.read && !who.read
		if at == nil || pos > *at || tie {
			v := pos
			at, who = &v, b
		}
	}
	return who, at
}

// furthestBookIn is where the row sits under an identity. The identity's own
// numbers decide: an umbrella's book 15 must not stand in for a sub-series'
// book 3. A borrowed slot is used only when it numbers none of the books.
func furthestBookIn(g *Group, source, name string) (*book, *float64) {
	if who, at := reachIn(g, source, name, true); at != nil {
		return who, at
	}
	return reachIn(g, source, name, false)
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
	var latestBook *book
	var latestFinish time.Time
	for _, b := range g.books {
		if !b.read && !b.reading {
			continue
		}
		if b.read && b.finishedAt.After(latestFinish) {
			latestFinish, latestBook = b.finishedAt, b
		}
		if pos, placed := posIn(b, g.Source, g.Name); placed {
			readSlots[pos] = true
		}
	}
	furthestBook, furthest := furthestBookIn(g, g.Source, g.Name)
	g.Position = furthest
	g.PositionReading = furthestBook != nil && !furthestBook.read
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
	// On a shared slot, a book the display series itself places beats one
	// whose slot is borrowed from another ordering.
	var next *book
	var nextPos float64
	var nextExact bool
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
		exact := hasMembership(b.memberships, library.Series{Source: g.Source, Name: g.Name})
		if next == nil || pos < nextPos || (pos == nextPos && exact && !nextExact) {
			next, nextPos, nextExact = b, pos, exact
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
		g.NextPosition = &nextPos
	}

	// Every other series these books belong to is an alternative home. That
	// includes another backend's claim under the same name: the provider a
	// series follows decides whose catalogue says what comes next.
	seen := map[string]bool{groupKey(library.Series{Source: g.Source, Name: g.Name}): true}
	for _, m := range g.memberships {
		k := groupKey(m)
		if seen[k] {
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
						// The cover mirrors the furthest book exactly, even to
						// empty: a lower volume's face would mislabel the preview.
						at, alt.CoverURL = bm.Position, b.cover
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
