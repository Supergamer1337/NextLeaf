// Package series tracks the series a reader is partway through, so NextLeaf can
// offer the next book — including one released long after they finished, and
// one they never added to a list. It also holds their standing decisions about
// each series (see CONTEXT.md for the vocabulary).
package series

import (
	"fmt"
	"strings"
	"time"
)

// Decision is a reader's standing decision about a tracked series.
type Decision int

const (
	// Active is the absence of a decision: the series is offered normally.
	Active Decision = iota
	// Parked skips the series for one turn; it clears once any other book is
	// finished.
	Parked
	// Dropped abandons the series; it clears only when one of its books is
	// added to the TBR after the drop.
	Dropped
	// Pinned makes the series the next thing to read; it clears once the
	// pinned book is read or started.
	Pinned
)

func (d Decision) String() string {
	switch d {
	case Parked:
		return "parked"
	case Dropped:
		return "dropped"
	case Pinned:
		return "pinned"
	default:
		return "active"
	}
}

// parseDecision reads a stored decision, defaulting to Active for anything
// unrecognised so a forward-compatible database never breaks a read.
func parseDecision(s string) Decision {
	switch s {
	case "parked":
		return Parked
	case "dropped":
		return Dropped
	case "pinned":
		return Pinned
	default:
		return Active
	}
}

// Tracked is a series the reader has read into, with the furthest position they
// reached and their standing decision about it.
type Tracked struct {
	Name string // as last seen from a source, for display
	// Position is the furthest numbered slot read, or nil when every book read
	// in the series was unplaced (see CONTEXT.md).
	Position *float64
	Decision Decision
	// DecidedAt is when the current decision was made; zero when Active.
	DecidedAt time.Time
	// ParkedAfter is the newest finish date known when the series was parked.
	// Finishing anything later than this clears the park.
	ParkedAfter time.Time
	// PinnedPosition is the position of the book the pin refers to; reading it
	// clears the pin.
	PinnedPosition float64
	// Slug is the richer source's own identifier for the series, kept as a
	// diagnostic and migration hint rather than something matched on.
	Slug string
	// Completed marks a series that can never gain another book, so it is
	// worth no new-release lookup.
	Completed bool

	// CaughtUp means the last lookup found nothing left to read. Distinct from
	// Completed: a series the author is still writing can leave you caught up
	// until the next volume appears. The UI files these under "Finished".
	CaughtUp bool
	// NextTitle, NextCoverURL, NextURL and NextPosition describe the book the
	// last lookup found, so the drawer can show it without asking again.
	NextTitle    string
	NextCoverURL string
	NextURL      string
	NextPosition float64
	// CoverURL is the cover of the furthest book read in the series, which
	// stands as the series' own face when there is no next book to show.
	CoverURL string
	// Alternatives are the other series the same books belong to, best first,
	// which the reader can switch the series to.
	Alternatives []Alternative
	// CheckedAt is when the next book was last looked up.
	CheckedAt time.Time
}

// Key normalises a series name the way the store matches on it, so callers
// comparing names against tracked series agree with the database.
func Key(name string) string { return key(name) }

// Alternative is another series the same books belong to. Description and
// Source are what tell two of them apart: often the same franchise ordered
// differently, or the same franchise as two backends happen to name it.
type Alternative struct {
	Name        string
	Description string
	Source      string
}

// Placed reports whether the reader has read a numbered volume of the series,
// which is what makes "the next one" a question with an answer.
func (t Tracked) Placed() bool { return t.Position != nil }

// Slot returns the furthest position read, and whether there is one.
func (t Tracked) Slot() (float64, bool) {
	if t.Position == nil {
		return 0, false
	}
	return *t.Position, true
}

// key normalises a series name into its storage key. Names are the only
// identifier Hardcover and Grimmory share, so matching is case- and
// whitespace-insensitive (see ADR 0001 and CONTEXT.md).
func key(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

// formatPos renders a series position without a trailing ".0" for whole numbers.
func formatPos(pos float64) string {
	if pos == float64(int64(pos)) {
		return fmt.Sprintf("%d", int64(pos))
	}
	return fmt.Sprintf("%g", pos)
}
