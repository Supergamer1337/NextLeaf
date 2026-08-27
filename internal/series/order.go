package series

import (
	"sort"
	"time"

	"nextleaf/internal/library"
)

// Precedence tiers, best first. Ordering between tiers is absolute; within a
// tier, a more recent finish wins.
const (
	tierPinned = iota
	tierFinished
	tierReading
	tierNewRelease
)

// Order ranks tracked series by which deserves a continuation first: the pinned
// one, then whichever was most recently finished, then one merely in progress,
// and last the series outside the recent window, where only a newly released
// book would have anything to offer. Parked and dropped series are left out.
//
// It returns every eligible series rather than just the winner, so the caller
// can move on to the next when a series turns out to have no next book.
func Order(tracked []Tracked, reads, reading []library.Entry) []Tracked {
	lastFinish := make(map[string]time.Time, len(reads))
	for _, e := range reads {
		if e.Book.Series == nil {
			continue
		}
		k := key(e.Book.Series.Name)
		if e.FinishedAt.After(lastFinish[k]) {
			lastFinish[k] = e.FinishedAt
		}
	}
	inProgress := make(map[string]bool, len(reading))
	for _, e := range reading {
		if e.Book.Series != nil {
			inProgress[key(e.Book.Series.Name)] = true
		}
	}

	type ranked struct {
		Tracked
		tier int
		at   time.Time
	}

	var out []ranked
	for _, t := range tracked {
		if t.Decision == Parked || t.Decision == Dropped {
			continue
		}
		k := key(t.Name)
		r := ranked{Tracked: t, tier: tierNewRelease, at: lastFinish[k]}
		switch {
		case t.Decision == Pinned:
			r.tier = tierPinned
		case !lastFinish[k].IsZero():
			r.tier = tierFinished
		case inProgress[k]:
			r.tier = tierReading
		}
		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tier != out[j].tier {
			return out[i].tier < out[j].tier
		}
		return out[i].at.After(out[j].at)
	})

	result := make([]Tracked, len(out))
	for i, r := range out {
		result[i] = r.Tracked
	}
	return result
}
