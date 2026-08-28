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

// Prefs are the reader's configurable recommendation settings.
type Prefs struct {
	// IncludeNovellas allows books at fractional series positions (3.5) to be
	// offered, both as the next book in a series and as the volume that
	// represents a series in the variety pool.
	IncludeNovellas bool
}

// isNovella treats a fractional series position as side material rather than
// the main sequence, which is how catalogues file novellas.
func isNovella(pos float64) bool { return pos != float64(int64(pos)) }

// Recommendation is a chosen book with the reasons for it (Pros) and the
// trade-offs it carries (Cons), kept apart so the UI can show each plainly.
type Recommendation struct {
	Entry library.Entry
	Pros  []string
	Cons  []string
}

// NextOnShelves returns the earliest book in the TBR that continues series past
// its last-read Position, so the caller can continue without hitting the
// provider. ok is false when no later book is on the shelf.
func NextOnShelves(series library.Series, toRead []library.Entry, prefs Prefs) (library.Entry, bool) {
	// An unplaced anchor gives nothing to count from, so every shelved volume
	// is still ahead of the reader: the earliest one is the answer.
	anchor, placed := series.Slot()

	var next library.Entry
	var nextPos float64
	found := false
	for _, e := range toRead {
		s := e.Book.Series
		if s == nil || !strings.EqualFold(s.Name, series.Name) {
			continue
		}
		pos, ok := s.Slot()
		if !ok {
			continue // an unplaced volume cannot be shown to come next
		}
		if placed && pos <= anchor {
			continue
		}
		if !prefs.IncludeNovellas && isNovella(pos) {
			continue
		}
		if !found || pos < nextPos {
			next, nextPos, found = e, pos, true
		}
	}
	return next, found
}

// ContinueSeries builds the recommendation for the next series book, noting
// the rating of the last book read in the series when it's known. e keeps its
// provenance (Sources, Available) so the UI can say where the book came from.
func ContinueSeries(e library.Entry, lastRating float64) Recommendation {
	e.Status = library.StatusWantToRead
	r := Recommendation{Entry: e}
	if e.Book.Series == nil {
		return r
	}
	pro := "Continues " + e.Book.Series.Name
	if pos, ok := e.Book.Series.Slot(); ok {
		pro += " — book " + formatPos(pos)
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
func Pick(rng *rand.Rand, prefs Prefs, candidates, recent, reading []library.Entry) (Recommendation, bool) {
	candidates = collapseSeries(candidates, prefs)
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

// collapseSeries reduces each positioned series to its earliest unread volume,
// so a series competes as one candidate — five unread volumes are one thing to
// read next, not five lottery tickets. Entries without a series, or with an
// unknown position, pass through untouched. Order is preserved, with each
// series sitting where its first-seen volume was.
func collapseSeries(candidates []library.Entry, prefs Prefs) []library.Entry {
	out := make([]library.Entry, 0, len(candidates))
	bySeries := make(map[string]int)
	positions := make(map[string]float64)
	for _, e := range candidates {
		s := e.Book.Series
		// An unplaced volume cannot be ordered against its siblings, so it
		// competes on its own rather than standing for the whole series.
		if s == nil || s.Name == "" {
			out = append(out, e)
			continue
		}
		pos, placed := s.Slot()
		if !placed {
			out = append(out, e)
			continue
		}
		if !prefs.IncludeNovellas && isNovella(pos) {
			continue
		}
		key := strings.ToLower(s.Name)
		if i, ok := bySeries[key]; ok {
			if pos < positions[key] {
				out[i], positions[key] = e, pos
			}
			continue
		}
		positions[key] = pos
		bySeries[key] = len(out)
		out = append(out, e)
	}
	return out
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
