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

// Series locates a book within a series.
type Series struct {
	Name     string
	Position float64
}

// Book is the provider-neutral description of a single title. Fields a given
// source can't supply stay zero/nil, and the picker's dimensions no-op on them,
// so every field is optional context rather than a requirement.
type Book struct {
	Title       string
	Subtitle    string
	Authors     []string
	Genres      []string
	Moods       []string // tone tags, e.g. "dark", "hopeful"; nil if unknown
	Series      *Series
	ReleaseYear int
	PageCount   int   // 0 if unknown
	Nonfiction  *bool // nil if the source can't classify fiction vs nonfiction
	CoverURL    string
	URL         string
}

// Entry is a book together with the user's relationship to it: where it sits in
// their reading life and the dates that matter for recommendation.
type Entry struct {
	Book       Book
	Status     Status
	Rating     float64   // user rating, 0 if unrated
	DateAdded  time.Time // when the book entered the user's library
	FinishedAt time.Time // last completion date; zero unless read
}
