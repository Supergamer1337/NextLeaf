package picker

import (
	"strings"
	"testing"
	"time"

	"nextleaf/internal/library"
)

func nonfic(b bool) *bool { return &b }

// verdictOf builds a profile from recent reads and runs one dimension.
func verdictOf(d dimension, cand library.Entry, recent []library.Entry) Verdict {
	return d(cand, buildProfile(nil, recent, nil))
}

func wantPro(t *testing.T, v Verdict, sub string) {
	t.Helper()
	if v.Weight <= 1 {
		t.Errorf("expected a boost, got weight %.2f", v.Weight)
	}
	if v.Con != "" || !strings.Contains(v.Pro, sub) {
		t.Errorf("expected pro containing %q, got pro=%q con=%q", sub, v.Pro, v.Con)
	}
}

func wantCon(t *testing.T, v Verdict, sub string) {
	t.Helper()
	if v.Weight >= 1 {
		t.Errorf("expected a penalty, got weight %.2f", v.Weight)
	}
	if v.Pro != "" || !strings.Contains(v.Con, sub) {
		t.Errorf("expected con containing %q, got pro=%q con=%q", sub, v.Pro, v.Con)
	}
}

func wantNeutral(t *testing.T, v Verdict) {
	t.Helper()
	if v.Weight != 1 || v.Pro != "" || v.Con != "" {
		t.Errorf("expected neutral, got %+v", v)
	}
}

func TestGenreDim(t *testing.T) {
	recent := []library.Entry{{Book: book("R", []string{"Fantasy"}, nil)}}
	// A fully new genre → boost, named.
	wantPro(t, verdictOf(genreDim, library.Entry{Book: book("c", []string{"History"}, nil)}, recent), "Brings in History")

	// Partial novelty: shares Fantasy but introduces Science Fiction → still a boost.
	partial := verdictOf(genreDim, library.Entry{Book: book("c", []string{"Fantasy", "Science Fiction"}, nil)}, recent)
	wantPro(t, partial, "Brings in Science Fiction")

	// A dominant recent genre → trade-off.
	dominant := []library.Entry{
		{Book: book("a", []string{"Fantasy"}, nil)},
		{Book: book("b", []string{"Fantasy"}, nil)},
		{Book: book("c", []string{"Fantasy"}, nil)},
	}
	wantCon(t, verdictOf(genreDim, library.Entry{Book: book("x", []string{"Fantasy"}, nil)}, dominant), "Leans into Fantasy")

	// Every genre already seen (none dominant) → neutral.
	seen := []library.Entry{{Book: book("R", []string{"Fantasy", "Adventure"}, nil)}}
	wantNeutral(t, verdictOf(genreDim, library.Entry{Book: book("x", []string{"Fantasy", "Adventure"}, nil)}, seen))
}

func TestModeDim(t *testing.T) {
	fictionRun := []library.Entry{
		{Book: library.Book{Title: "a", Nonfiction: nonfic(false)}},
		{Book: library.Book{Title: "b", Nonfiction: nonfic(false)}},
		{Book: library.Book{Title: "c", Nonfiction: nonfic(false)}},
	}
	wantPro(t, verdictOf(modeDim, library.Entry{Book: library.Book{Nonfiction: nonfic(true)}}, fictionRun), "Switches from fiction to nonfiction")
	wantCon(t, verdictOf(modeDim, library.Entry{Book: library.Book{Nonfiction: nonfic(false)}}, fictionRun), "Stays in fiction")

	// No streak (only two) → neutral.
	short := fictionRun[:2]
	wantNeutral(t, verdictOf(modeDim, library.Entry{Book: library.Book{Nonfiction: nonfic(true)}}, short))
	// Unknown candidate mode → neutral.
	wantNeutral(t, verdictOf(modeDim, library.Entry{Book: library.Book{}}, fictionRun))
}

func TestEraDim(t *testing.T) {
	recent := []library.Entry{
		{Book: library.Book{ReleaseYear: 2015}},
		{Book: library.Book{ReleaseYear: 2016}},
		{Book: library.Book{ReleaseYear: 2018}},
	}
	wantPro(t, verdictOf(eraDim, library.Entry{Book: library.Book{ReleaseYear: 1968}}, recent), "the 1960s")
	wantPro(t, verdictOf(eraDim, library.Entry{Book: library.Book{ReleaseYear: 180}}, recent), "antiquity")
	wantCon(t, verdictOf(eraDim, library.Entry{Book: library.Book{ReleaseYear: 2016}}, recent), "same era")
}

func TestAuthorDim(t *testing.T) {
	recent := []library.Entry{
		{Book: book("a", nil, []string{"Brandon Sanderson"})},
		{Book: book("b", nil, []string{"Brandon Sanderson"})},
	}
	wantCon(t, verdictOf(authorDim, library.Entry{Book: book("x", nil, []string{"Brandon Sanderson"})}, recent), "Leans on Brandon Sanderson")
	wantNeutral(t, verdictOf(authorDim, library.Entry{Book: book("x", nil, []string{"Someone Else"})}, recent))
}

func TestAgeDim(t *testing.T) {
	old := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cands := []library.Entry{
		{Book: book("old", nil, nil), DateAdded: old},
		{Book: book("mid", nil, nil), DateAdded: mid},
		{Book: book("new", nil, nil), DateAdded: recent},
	}
	p := buildProfile(cands, nil, nil)
	wantPro(t, ageDim(cands[0], p), "longest-waiting")
	wantPro(t, ageDim(cands[2], p), "recent addition")
	wantNeutral(t, ageDim(cands[1], p))
}

func TestSeriesDim(t *testing.T) {
	run := []library.Entry{
		{Book: seriesBook("a", "Wheel of Time", 1)},
		{Book: seriesBook("b", "Wheel of Time", 2)},
		{Book: seriesBook("c", "Wheel of Time", 3)},
	}
	wantPro(t, verdictOf(seriesDim, library.Entry{Book: book("standalone", nil, nil)}, run), "standalone")
	wantCon(t, verdictOf(seriesDim, library.Entry{Book: seriesBook("newone", "Mistborn", 1)}, run), "Starts a new series")
	// Continuing the same run → neutral.
	wantNeutral(t, verdictOf(seriesDim, library.Entry{Book: seriesBook("more wheel", "Wheel of Time", 4)}, run))
}

func TestMoodDim(t *testing.T) {
	dark := library.Book{Moods: []string{"dark"}}
	recent := []library.Entry{
		{Book: library.Book{Title: "a", Moods: []string{"dark"}}},
		{Book: library.Book{Title: "b", Moods: []string{"dark"}}},
		{Book: library.Book{Title: "c", Moods: []string{"dark"}}},
	}
	wantCon(t, verdictOf(moodDim, library.Entry{Book: dark}, recent), "More dark")
	wantPro(t, verdictOf(moodDim, library.Entry{Book: library.Book{Moods: []string{"hopeful"}}}, recent), "different mood")
}

func TestLengthDim(t *testing.T) {
	longRun := []library.Entry{
		{Book: library.Book{PageCount: 600}},
		{Book: library.Book{PageCount: 700}},
		{Book: library.Book{PageCount: 500}},
	}
	wantPro(t, verdictOf(lengthDim, library.Entry{Book: library.Book{PageCount: 280}}, longRun), "shorter read")
	wantCon(t, verdictOf(lengthDim, library.Entry{Book: library.Book{PageCount: 720}}, longRun), "Another long one")
	// Middling length → neutral.
	wantNeutral(t, verdictOf(lengthDim, library.Entry{Book: library.Book{PageCount: 400}}, longRun))
}
