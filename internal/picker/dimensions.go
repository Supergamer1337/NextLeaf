package picker

import (
	"fmt"
	"sort"
	"strings"

	"nextleaf/internal/library"
)

// Verdict is one dimension's take on a candidate: a weight multiplier plus, at
// most, a reason for (Pro) or against (Con). The neutral middle of an axis
// returns Weight 1 and no text — only breaking or extending a real pattern
// speaks.
type Verdict struct {
	Weight float64
	Pro    string
	Con    string
}

// A dimension judges one axis of variety. Dimensions read only the neutral
// library model, so they work with any Source; missing data (empty/zero/nil)
// yields a neutral Verdict.
type dimension func(cand library.Entry, p profile) Verdict

// dimensions is the ordered set applied to every candidate.
var dimensions = []dimension{
	genreDim,
	modeDim,
	eraDim,
	authorDim,
	ageDim,
	seriesDim,
	moodDim,
	lengthDim,
}

// score multiplies every dimension's weight and gathers their pros and cons.
func score(cand library.Entry, p profile) (float64, []string, []string) {
	weight := 1.0
	var pros, cons []string
	for _, d := range dimensions {
		v := d(cand, p)
		weight *= v.Weight
		if v.Pro != "" {
			pros = append(pros, v.Pro)
		}
		if v.Con != "" {
			cons = append(cons, v.Con)
		}
	}
	if weight < weightFloor {
		weight = weightFloor
	}
	return weight, pros, cons
}

// Tuning constants. Boosts are >1, penalties <1; thresholds gate when a penalty
// is allowed to speak, so it only fires on a genuine streak or dominance.
const (
	// RecentWindow is how many finished books the thresholds below are calibrated
	// against; callers should fetch about this many recent reads to feed the
	// picker. It lives here so the window and the streak/dominance thresholds
	// that assume it stay together.
	RecentWindow = 6

	weightFloor = 0.05

	genreDominance   = 3   // a genre in >= this many recent reads is "dominant"
	genreNovelBoost  = 0.9 // max extra weight when every genre is new to recent reads
	genreNovelReason = 0.5 // name the fresh genres once at least this fraction is new
	genreStreakDamp  = 0.5

	modeStreakLen = 3
	modeFlipBoost = 1.6
	modeStayDamp  = 0.6

	eraGapYears    = 25
	eraClusterSpan = 12
	eraBoost       = 1.3
	eraSameDamp    = 0.85

	authorRepeat     = 2
	authorStreakDamp = 0.5

	ageOldFrac  = 0.6
	ageBoost    = 0.8
	ageNewFrac  = 0.15
	ageNewBoost = 1.15

	seriesStreakLen = 3
	seriesNewDamp   = 0.6
	standaloneBoost = 1.4

	moodDominance = 3
	moodDiffBoost = 1.2
	moodSameDamp  = 0.7

	lengthLongAvg    = 450
	lengthShort      = 350
	lengthShortBoost = 1.2
	lengthLongDamp   = 0.85

	// SeriesRatingGate is the lowest rating at which a rated in-progress series
	// is still worth continuing; below it, the picker won't push the next book.
	SeriesRatingGate = 3.0
)

// profile summarises recent reading so dimensions can judge each candidate
// against it. Counts span recent reads plus in-progress books; runs are the
// trailing streaks in the (newest-first) recent list; the age range comes from
// the candidates themselves.
type profile struct {
	recentN     int
	genreCount  map[string]int
	authorCount map[string]int
	moodCount   map[string]int
	years       []int
	pages       []int

	modeRun    int
	modeRunNon bool
	seriesRun  int
	seriesName string

	oldestAdd int64
	newestAdd int64
	haveAge   bool
}

func buildProfile(candidates, recent, reading []library.Entry) profile {
	p := profile{
		genreCount:  map[string]int{},
		authorCount: map[string]int{},
		moodCount:   map[string]int{},
	}

	ctx := make([]library.Entry, 0, len(recent)+len(reading))
	ctx = append(ctx, recent...)
	ctx = append(ctx, reading...)
	p.recentN = len(ctx)
	for _, e := range ctx {
		for _, g := range e.Book.Genres {
			p.genreCount[strings.ToLower(g)]++
		}
		for _, a := range e.Book.Authors {
			p.authorCount[strings.ToLower(a)]++
		}
		for _, m := range e.Book.Moods {
			p.moodCount[strings.ToLower(m)]++
		}
		if e.Book.ReleaseYear != 0 {
			p.years = append(p.years, e.Book.ReleaseYear)
		}
		if e.Book.PageCount != 0 {
			p.pages = append(p.pages, e.Book.PageCount)
		}
	}

	p.modeRun, p.modeRunNon = trailingModeRun(recent)
	p.seriesRun, p.seriesName = trailingSeriesRun(recent)

	for _, c := range candidates {
		if c.DateAdded.IsZero() {
			continue
		}
		s := c.DateAdded.Unix()
		if !p.haveAge || s < p.oldestAdd {
			p.oldestAdd = s
		}
		if !p.haveAge || s > p.newestAdd {
			p.newestAdd = s
		}
		p.haveAge = true
	}
	return p
}

// trailingModeRun counts the leading run of same fiction/nonfiction reads.
func trailingModeRun(recent []library.Entry) (int, bool) {
	run := 0
	var mode bool
	for i, e := range recent {
		if e.Book.Nonfiction == nil {
			break
		}
		if i == 0 {
			mode, run = *e.Book.Nonfiction, 1
			continue
		}
		if *e.Book.Nonfiction != mode {
			break
		}
		run++
	}
	return run, mode
}

// trailingSeriesRun counts the leading run of reads from the same series.
func trailingSeriesRun(recent []library.Entry) (int, string) {
	run := 0
	name := ""
	for i, e := range recent {
		s := e.Book.Series
		if s == nil || s.Name == "" {
			break
		}
		if i == 0 {
			name, run = s.Name, 1
			continue
		}
		if !strings.EqualFold(s.Name, name) {
			break
		}
		run++
	}
	return run, name
}

// genreDim rewards how much of a book's genres are new to recent reading, and
// flags one that already dominates it. Overlap discounts the boost rather than
// disqualifying it, so a book that shares one genre but introduces others still
// counts as variety.
func genreDim(cand library.Entry, p profile) Verdict {
	g := cand.Book.Genres
	if len(g) == 0 || p.recentN == 0 {
		return Verdict{Weight: 1}
	}
	for _, name := range g {
		if n := p.genreCount[strings.ToLower(name)]; n >= genreDominance {
			return Verdict{Weight: genreStreakDamp, Con: fmt.Sprintf("Leans into %s again — %d of your recent reads", name, n)}
		}
	}
	var fresh []string
	for _, name := range g {
		if p.genreCount[strings.ToLower(name)] == 0 {
			fresh = append(fresh, name)
		}
	}
	if len(fresh) == 0 {
		return Verdict{Weight: 1}
	}
	frac := float64(len(fresh)) / float64(len(g))
	v := Verdict{Weight: 1 + genreNovelBoost*frac}
	if frac >= genreNovelReason {
		v.Pro = "Brings in " + joinGenres(fresh) + ", new to your recent reading"
	}
	return v
}

// joinGenres renders up to two fresh genre names, summarising any beyond that.
func joinGenres(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " & " + names[1]
	default:
		return names[0] + ", " + names[1] + " & more"
	}
}

// modeDim rewards a fiction/nonfiction flip after a run and flags staying put.
func modeDim(cand library.Entry, p profile) Verdict {
	if cand.Book.Nonfiction == nil || p.modeRun < modeStreakLen {
		return Verdict{Weight: 1}
	}
	if *cand.Book.Nonfiction != p.modeRunNon {
		return Verdict{Weight: modeFlipBoost, Pro: fmt.Sprintf("Switches from %s to %s after a run", modeName(p.modeRunNon), modeName(*cand.Book.Nonfiction))}
	}
	return Verdict{Weight: modeStayDamp, Con: fmt.Sprintf("Stays in %s, like your recent reads", modeName(p.modeRunNon))}
}

// eraDim rewards a different publication era and flags matching a tight cluster.
func eraDim(cand library.Entry, p profile) Verdict {
	y := cand.Book.ReleaseYear
	if y == 0 || len(p.years) < 3 {
		return Verdict{Weight: 1}
	}
	med := median(p.years)
	diff := abs(y - med)
	if diff >= eraGapYears {
		return Verdict{Weight: eraBoost, Pro: fmt.Sprintf("From %s — a different era than your recent reads", formatEra(y))}
	}
	if span(p.years) <= eraClusterSpan && diff <= 5 {
		return Verdict{Weight: eraSameDamp, Con: "From the same era as your recent reads"}
	}
	return Verdict{Weight: 1}
}

// authorDim flags an author who already dominates recent reads (no boost side).
func authorDim(cand library.Entry, p profile) Verdict {
	for _, a := range cand.Book.Authors {
		if n := p.authorCount[strings.ToLower(a)]; n >= authorRepeat {
			return Verdict{Weight: authorStreakDamp, Con: fmt.Sprintf("Leans on %s again — %d recent reads", a, n)}
		}
	}
	return Verdict{Weight: 1}
}

// ageDim boosts the longest-waiting books and, mildly, the freshest — never a
// penalty, so newness is welcome.
func ageDim(cand library.Entry, p profile) Verdict {
	if !p.haveAge || p.newestAdd <= p.oldestAdd || cand.DateAdded.IsZero() {
		return Verdict{Weight: 1}
	}
	frac := float64(p.newestAdd-cand.DateAdded.Unix()) / float64(p.newestAdd-p.oldestAdd)
	if frac >= ageOldFrac {
		return Verdict{Weight: 1 + ageBoost*frac, Pro: "One of the longest-waiting books on your list"}
	}
	if frac <= ageNewFrac {
		return Verdict{Weight: ageNewBoost, Pro: "A recent addition — worth a look while it's fresh"}
	}
	return Verdict{Weight: 1}
}

// seriesDim, after a long single-series run, rewards a standalone and flags
// starting yet another new series.
func seriesDim(cand library.Entry, p profile) Verdict {
	if p.seriesRun < seriesStreakLen {
		return Verdict{Weight: 1}
	}
	s := cand.Book.Series
	if s == nil {
		return Verdict{Weight: standaloneBoost, Pro: "A standalone, after a long series run"}
	}
	if !strings.EqualFold(s.Name, p.seriesName) && s.Position != 0 && s.Position <= 1 {
		return Verdict{Weight: seriesNewDamp, Con: "Starts a new series right after a long run"}
	}
	return Verdict{Weight: 1}
}

// moodDim rewards a fresh mood and flags extending a dominant one.
func moodDim(cand library.Entry, p profile) Verdict {
	m := cand.Book.Moods
	if len(m) == 0 || p.recentN == 0 {
		return Verdict{Weight: 1}
	}
	for _, name := range m {
		if n := p.moodCount[strings.ToLower(name)]; n >= moodDominance {
			return Verdict{Weight: moodSameDamp, Con: fmt.Sprintf("More %s, like your recent reads", name)}
		}
	}
	if novelToSet(m, p.moodCount) {
		return Verdict{Weight: moodDiffBoost, Pro: "A different mood from your recent reads"}
	}
	return Verdict{Weight: 1}
}

// lengthDim, when recent reads run long, rewards a shorter book and flags
// another long one.
func lengthDim(cand library.Entry, p profile) Verdict {
	pc := cand.Book.PageCount
	if pc == 0 || len(p.pages) < 3 {
		return Verdict{Weight: 1}
	}
	if mean(p.pages) < lengthLongAvg {
		return Verdict{Weight: 1}
	}
	if pc <= lengthShort {
		return Verdict{Weight: lengthShortBoost, Pro: "A shorter read after a run of long ones"}
	}
	if pc >= lengthLongAvg {
		return Verdict{Weight: lengthLongDamp, Con: "Another long one, like your last few"}
	}
	return Verdict{Weight: 1}
}

// novelToSet reports whether none of values appears in the count set.
func novelToSet(values []string, set map[string]int) bool {
	for _, v := range values {
		if set[strings.ToLower(v)] > 0 {
			return false
		}
	}
	return true
}

// formatEra renders a release year as a readable era: the decade for anything
// since ~1000 ("the 1960s"), and a vaguer label for older works, avoiding a bare
// "180" and the false precision of an exact ancient date.
func formatEra(year int) string {
	if year < 1000 {
		return "antiquity"
	}
	return fmt.Sprintf("the %d0s", year/10)
}

func modeName(nonfiction bool) string {
	if nonfiction {
		return "nonfiction"
	}
	return "fiction"
}

func median(xs []int) int {
	s := append([]int(nil), xs...)
	sort.Ints(s)
	return s[len(s)/2]
}

func mean(xs []int) int {
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return sum / len(xs)
}

func span(xs []int) int {
	lo, hi := xs[0], xs[0]
	for _, x := range xs {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	return hi - lo
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
