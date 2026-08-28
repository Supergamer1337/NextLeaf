// Package series turns the reader's library into a live view of the series
// they are in, and records their statements about those series. The view is
// computed fresh from the sources on every look — nothing derived is stored —
// and statements are the only owned state.
package series

import (
	"fmt"
	"strings"
)

// Decision is a reader's standing decision about a tracked series, as the
// computed view reports it.
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

// Statement kinds: what the reader can say about a series. A statement is
// appended, never edited; it stops applying when its predicate says so.
const (
	KindPark   = "parked"
	KindDrop   = "dropped"
	KindPin    = "pinned"
	KindClear  = "clear"
	KindPrefer = "prefer"
)

// Key normalises a series name the way the view matches on it: case- and
// whitespace-insensitive. Names are display labels, so this is a matching
// convenience, never an identity.
func Key(name string) string { return key(name) }

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
