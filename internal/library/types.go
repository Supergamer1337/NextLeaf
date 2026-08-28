// Package library defines Nextleaf's source-agnostic reading-data model and the
// Source interface that connectors (Hardcover, and future alternatives)
// implement. Nothing here depends on a specific provider.
package library

import "time"

// Status is a book's place in a user's reading life. The numeric values match
// Hardcover's status_id so connectors can map without a lookup table, but the
// type is meant to be provider-neutral.
type Status int

const (
	StatusWantToRead    Status = 1
	StatusCurrentlyRead Status = 2
	StatusRead          Status = 3
	StatusPaused        Status = 4
	StatusDNF           Status = 5
	StatusIgnored       Status = 6
)

// Series locates a book within a series. Name is the only identifier every
// source can supply, so it is what NextLeaf matches on; Slug and Completed are
// hints a richer source may add.
type Series struct {
	Name string
	// Position is the book's numbered slot, or nil when the book is unplaced:
	// it belongs to the series without occupying a slot. That is a different
	// thing from a position of 0, since series really do number prequels 0 or
	// below, and it is also what a source reports when it simply does not know.
	Position  *float64
	Slug      string // source-specific series identifier; "" if unknown
	Completed bool   // the series is finished, so no later book can appear
	// Description is the catalogue's blurb, which helps tell two orderings of
	// one franchise apart. Empty when the source has none.
	Description string
	// Source names the backend that reported this membership, so a reader
	// choosing between them can see who says what.
	Source string
}

// At returns a position for a book that occupies a numbered slot. Any number is
// valid, including zero and negatives.
func At(pos float64) *float64 { return &pos }

// Placed reports whether the book occupies a numbered slot in its series.
func (s Series) Placed() bool { return s.Position != nil }

// Slot returns the book's numbered position, and whether it has one. Callers
// that order books must respect ok: an unplaced book sorts nowhere.
func (s Series) Slot() (float64, bool) {
	if s.Position == nil {
		return 0, false
	}
	return *s.Position, true
}

// SeriesQuery asks a SeriesResolver for the book following a position, carrying
// the reader's preferences so the resolver can honour them while it searches.
type SeriesQuery struct {
	Series Series
	// IncludeNovellas allows books at fractional positions (3.5) to be
	// offered as the next read.
	IncludeNovellas bool
}

// Book is the provider-neutral description of a single title. Fields a given
// source can't supply stay zero/nil, and the picker's dimensions no-op on them,
// so every field is optional context rather than a requirement.
type Book struct {
	Title       string
	Subtitle    string
	Description string
	Authors     []string
	Genres      []string
	Moods       []string // tone tags, e.g. "dark", "hopeful"; nil if unknown
	// Series is the one series the book is tracked under. OtherSeries holds
	// the alternatives it could be tracked under instead, best first — a
	// franchise reordered chronologically, or a sub-series.
	Series      *Series
	OtherSeries []Series
	ReleaseYear int
	ReleaseDate time.Time // zero when the source gives only a year, or nothing
	PageCount   int       // 0 if unknown
	Nonfiction  *bool     // nil if the source can't classify fiction vs nonfiction
	CoverURL    string
	URL         string
	// ISBNs are the neutral identifiers this book is known by, when the
	// source can supply them. They are what lets two sources' copies of one
	// book be joined without guessing from titles.
	ISBNs []string
}

// SourceRef names a backend holding an entry and links to the book's page
// there. URL is empty when the source has no page for it, and ID is the
// backend's own identifier for the book, "" when it has none.
type SourceRef struct {
	Name string
	URL  string
	ID   string
}

// Entry is a book together with the user's relationship to it: where it sits in
// their reading life and the dates that matter for recommendation.
type Entry struct {
	Book       Book
	Status     Status
	Rating     float64     // user rating, 0 if unrated
	DateAdded  time.Time   // when the book entered the user's library
	FinishedAt time.Time   // last completion date; zero unless read
	Sources    []SourceRef // backends holding this entry, e.g. [{grimmory, …/book/7}]
	Available  bool        // a source already holds a readable copy; nothing to acquire
}
