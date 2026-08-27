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
// hints a richer source may add (see CONTEXT.md).
type Series struct {
	Name      string
	Position  float64
	Slug      string // source-specific series identifier; "" if unknown
	Completed bool   // the series is finished, so no later book can appear
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
	Series      *Series
	ReleaseYear int
	ReleaseDate time.Time // zero when the source gives only a year, or nothing
	PageCount   int       // 0 if unknown
	Nonfiction  *bool     // nil if the source can't classify fiction vs nonfiction
	CoverURL    string
	URL         string
}

// SourceRef names a backend holding an entry and links to the book's page
// there. URL is empty when the source has no page for it.
type SourceRef struct {
	Name string
	URL  string
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
