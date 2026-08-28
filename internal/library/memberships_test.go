package library

import "testing"

func TestMergingKeepsBothSourcesSeriesAsAlternatives(t *testing.T) {
	// Hardcover and Grimmory name the same franchise differently, and each
	// only knows its own. Dropping one would hide a series the reader could
	// reasonably want to track under.
	hardcover := Entry{Book: Book{
		Title:   "The Final Empire",
		Authors: []string{"Brandon Sanderson"},
		Series:  &Series{Name: "Mistborn", Position: At(1), Source: "hardcover"},
	}, Sources: []SourceRef{{Name: "hardcover"}}}
	grimmory := Entry{Book: Book{
		Title:   "The Final Empire",
		Authors: []string{"Brandon Sanderson"},
		Series:  &Series{Name: "The Mistborn Saga", Position: At(1), Source: "grimmory"},
	}, Sources: []SourceRef{{Name: "grimmory"}}}

	merged := mergeEntry(hardcover, grimmory)

	if merged.Book.Series == nil || merged.Book.Series.Name != "Mistborn" {
		t.Fatalf("Series = %+v, want the first source's pick kept", merged.Book.Series)
	}
	if len(merged.Book.OtherSeries) != 1 {
		t.Fatalf("OtherSeries = %+v, want the other source's series offered", merged.Book.OtherSeries)
	}
	other := merged.Book.OtherSeries[0]
	if other.Name != "The Mistborn Saga" {
		t.Errorf("alternative = %q, want The Mistborn Saga", other.Name)
	}
	if other.Source != "grimmory" {
		t.Errorf("alternative source = %q, want grimmory so the reader knows who says so", other.Source)
	}
}

func TestMergingDoesNotRepeatASeriesBothSourcesAgreeOn(t *testing.T) {
	a := Entry{Book: Book{Title: "X", Series: &Series{Name: "Saga", Position: At(1), Source: "hardcover"}}}
	b := Entry{Book: Book{Title: "X", Series: &Series{Name: "saga", Position: At(1), Source: "grimmory"}}}

	merged := mergeEntry(a, b)
	if len(merged.Book.OtherSeries) != 0 {
		t.Errorf("OtherSeries = %+v, want none: both sources named the same series", merged.Book.OtherSeries)
	}
}

func TestMergingTakesASeriesFromTheOnlySourceThatKnowsOne(t *testing.T) {
	a := Entry{Book: Book{Title: "X"}}
	b := Entry{Book: Book{Title: "X", Series: &Series{Name: "Saga", Position: At(2), Source: "grimmory"}}}

	merged := mergeEntry(a, b)
	if merged.Book.Series == nil || merged.Book.Series.Name != "Saga" {
		t.Errorf("Series = %+v, want the series from the source that has one", merged.Book.Series)
	}
	if len(merged.Book.OtherSeries) != 0 {
		t.Errorf("OtherSeries = %+v, want none", merged.Book.OtherSeries)
	}
}
