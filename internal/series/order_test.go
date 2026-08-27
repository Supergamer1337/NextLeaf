package series

import (
	"testing"

	"nextleaf/internal/library"
)

func names(tracked []Tracked) []string {
	out := make([]string, len(tracked))
	for i, t := range tracked {
		out[i] = t.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOrderPutsMostRecentlyFinishedSeriesFirst(t *testing.T) {
	tracked := []Tracked{{Name: "Mistborn", Position: 3}, {Name: "Wheel of Time", Position: 1}}
	reads := []library.Entry{finished("Wheel of Time", 1, day2), finished("Mistborn", 3, day0)}

	got := names(Order(tracked, reads, nil))
	if want := []string{"Wheel of Time", "Mistborn"}; !equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestOrderPutsAPinnedSeriesAboveEverything(t *testing.T) {
	tracked := []Tracked{
		{Name: "Mistborn", Position: 3, Decision: Pinned, PinnedPosition: 4},
		{Name: "Wheel of Time", Position: 1},
	}
	// Wheel of Time was finished more recently, but the pin outranks that.
	reads := []library.Entry{finished("Wheel of Time", 1, day2), finished("Mistborn", 3, day0)}

	got := names(Order(tracked, reads, nil))
	if want := []string{"Mistborn", "Wheel of Time"}; !equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestOrderExcludesParkedAndDroppedSeries(t *testing.T) {
	tracked := []Tracked{
		{Name: "Dune", Position: 1, Decision: Dropped},
		{Name: "Mistborn", Position: 3, Decision: Parked},
		{Name: "Wheel of Time", Position: 1},
	}
	reads := []library.Entry{finished("Mistborn", 3, day2), finished("Dune", 1, day1), finished("Wheel of Time", 1, day0)}

	got := names(Order(tracked, reads, nil))
	if want := []string{"Wheel of Time"}; !equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestOrderKeepsSeriesOutsideTheRecentWindowAsNewReleaseCandidates(t *testing.T) {
	// Stormlight was finished years ago and is nowhere in the recent reads, but
	// a new book could still have come out; it ranks below anything current.
	tracked := []Tracked{{Name: "Stormlight", Position: 4}, {Name: "Mistborn", Position: 3}}
	reads := []library.Entry{finished("Mistborn", 3, day0)}

	got := names(Order(tracked, reads, nil))
	if want := []string{"Mistborn", "Stormlight"}; !equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestOrderRanksAnInProgressSeriesBelowAFinishedOne(t *testing.T) {
	// A book you are already holding needs no recommendation, so a series you
	// merely have open ranks under one you just finished.
	tracked := []Tracked{{Name: "Mistborn", Position: 3}, {Name: "Wheel of Time", Position: 1}}
	reads := []library.Entry{finished("Wheel of Time", 1, day0)}
	reading := []library.Entry{readEntry("Mistborn", 4)}

	got := names(Order(tracked, reads, reading))
	if want := []string{"Wheel of Time", "Mistborn"}; !equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}
