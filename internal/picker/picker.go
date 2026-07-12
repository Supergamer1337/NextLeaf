// Package picker turns a user's reading data into a single recommendation.
// Two paths: continue an in-progress series (the sensible default), or, on
// reroll, a variety-weighted random pick from the TBR scored across independent
// dimensions (see dimensions.go). All logic here is pure and source-agnostic;
// the optional provider lookup for an off-shelf next book lives in the caller.
package picker

import (
	"fmt"
	"math/rand"
	"strings"

	"nextleaf/internal/library"
)

// Recommendation is a chosen book with the reasons for it (Pros) and the
// trade-offs it carries (Cons), kept apart so the UI can show each plainly.
type Recommendation struct {
	Entry library.Entry
	Pros  []string
	Cons  []string
}

// ActiveSeries returns the entry anchoring the series to continue: the most
// recently finished series book, else one in progress. The caller reads its
// Series and Rating. ok is false when nothing is in a series. recent is
// expected newest-first (as sources provide it).
func ActiveSeries(reading, recent []library.Entry) (library.Entry, bool) {
	for _, e := range recent {
		if e.Book.Series != nil {
			return e, true
		}
	}
	for _, e := range reading {
		if e.Book.Series != nil {
			return e, true
		}
	}
	return library.Entry{}, false
}

// NextOnShelves returns the earliest book in the TBR that continues series past
// its last-read Position, so the caller can continue without hitting the
// provider. ok is false when no later book is on the shelf.
func NextOnShelves(series library.Series, toRead []library.Entry) (library.Entry, bool) {
	var next library.Entry
	found := false
	for _, e := range toRead {
		s := e.Book.Series
		if s == nil || !strings.EqualFold(s.Name, series.Name) {
			continue
		}
		if s.Position <= series.Position {
			continue
		}
		if !found || s.Position < next.Book.Series.Position {
			next, found = e, true
		}
	}
	return next, found
}

// ContinueSeries builds the recommendation for the next series book b, noting
// the rating of the last book read in the series when it's known.
func ContinueSeries(b library.Book, lastRating float64) Recommendation {
	r := Recommendation{Entry: library.Entry{Book: b, Status: library.StatusWantToRead}}
	if b.Series == nil {
		return r
	}
	pro := "Continues " + b.Series.Name
	if b.Series.Position != 0 {
		pro += " — book " + formatPos(b.Series.Position)
	}
	if lastRating > 0 {
		pro += fmt.Sprintf(" (you rated the last one %s★)", formatRating(lastRating))
	}
	r.Pros = []string{pro}
	return r
}

// Pick is the variety path: a weighted-random choice over candidates scored by
// the dimensions. rng is injected so callers and tests control determinism. ok
// is false when there are no candidates.
func Pick(rng *rand.Rand, candidates, recent, reading []library.Entry) (Recommendation, bool) {
	if len(candidates) == 0 {
		return Recommendation{}, false
	}

	p := buildProfile(candidates, recent, reading)

	weights := make([]float64, len(candidates))
	total := 0.0
	for i, c := range candidates {
		w, _, _ := score(c, p)
		weights[i] = w
		total += w
	}

	target := rng.Float64() * total
	chosen := len(candidates) - 1
	for i, w := range weights {
		target -= w
		if target < 0 {
			chosen = i
			break
		}
	}

	_, pros, cons := score(candidates[chosen], p)
	return Recommendation{Entry: candidates[chosen], Pros: pros, Cons: cons}, true
}

// formatPos renders a series position without a trailing ".0" for whole numbers.
func formatPos(pos float64) string {
	if pos == float64(int64(pos)) {
		return fmt.Sprintf("%d", int64(pos))
	}
	return fmt.Sprintf("%g", pos)
}

// formatRating trims a whole-number rating's ".0" (4.0 → "4", 4.5 → "4.5").
func formatRating(r float64) string {
	if r == float64(int64(r)) {
		return fmt.Sprintf("%d", int64(r))
	}
	return fmt.Sprintf("%g", r)
}
