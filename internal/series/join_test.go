package series

import (
	"fmt"
	"slices"
	"testing"

	"nextleaf/internal/library"
)

// The Hobbit as two sources really describe it.
func hobbits(isbn bool) (hc, gm library.Entry) {
	hc = read("The Hobbit, or There and Back Again", "Middle Earth", 1, day0)
	hc.Book.Authors = []string{"J.R.R. Tolkien"}
	gm = entry("The Hobbit", "The Lord of the Rings", 0, "grimmory")
	gm.Book.Authors = []string{"J.R.R. Tolkien", "Alan Lee"}
	gm.Status, gm.FinishedAt = library.StatusRead, day0
	if isbn {
		hc.Book.ISBNs = []string{"9780007487318", "9780261102217"}
		gm.Book.ISBNs = []string{"9780261102217"}
	}
	return hc, gm
}

func hobbitInput(isbn bool, statements ...Statement) Input {
	hc, gm := hobbits(isbn)
	return Input{Reads: []library.Entry{hc, gm}, Statements: statements, SourceOrder: []string{"hardcover", "grimmory"}}
}

func TestABookJoinedByISBNIsOneGroup(t *testing.T) {
	v := Compute(hobbitInput(true))
	if len(v.Groups) != 1 {
		t.Fatalf("groups = %v, want one book to be one series", groupNames(v))
	}
	g := v.Groups[0]
	if g.Name != "Middle Earth" {
		t.Errorf("Name = %q, want the first source's claim", g.Name)
	}
	if len(g.Alternatives) != 1 || g.Alternatives[0].Name != "The Lord of the Rings" {
		t.Errorf("Alternatives = %+v, want the library's series offered", g.Alternatives)
	}
}

func TestALikenessAloneDoesNotJoinBooks(t *testing.T) {
	v := Compute(hobbitInput(false))
	if len(v.Groups) != 2 {
		t.Errorf("groups = %v, want the books kept apart without an ISBN in common", groupNames(v))
	}
}

func TestADecisionFollowsItsBookIntoAJoin(t *testing.T) {
	// Production statements are anchored to each copy's plain key. Joining
	// the copies renames the book; the drop must still find it.
	_, gm := hobbits(true)
	drop := Statement{Kind: KindDrop, Name: "The Lord of the Rings", Anchors: []string{library.BookKey(gm)}}
	v := Compute(hobbitInput(true, drop))
	if len(v.Groups) != 1 || v.Groups[0].Decision != Dropped {
		t.Errorf("groups = %v, decision = %v; want the joined series dropped", groupNames(v), v.Groups[0].Decision)
	}
}

func TestAJoinedBookKeepsEachISBNOnce(t *testing.T) {
	hc, gm := hobbits(true)
	hc.Book.ISBNs = append(hc.Book.ISBNs, "9780007487318") // a catalogue repeating itself
	books := mergeBooks(Input{Reads: []library.Entry{hc, gm}}, library.NewKeyIndex([]library.Entry{hc, gm}))
	if len(books) != 1 {
		t.Fatalf("books = %d, want the two copies joined", len(books))
	}
	want := []string{"9780007487318", "9780261102217"}
	if got := books[0].isbns; !slices.Equal(got, want) {
		t.Errorf("isbns = %v, want %v: each once, in the order first seen", got, want)
	}
}

// BenchmarkComputeWithManyEditions computes a view over a library shaped like
// a real one: Hardcover lists every edition's ISBN, and a classic can have
// two thousand of them.
func BenchmarkComputeWithManyEditions(b *testing.B) {
	var in Input
	for i := range 40 {
		e := read(fmt.Sprintf("Book %d", i), fmt.Sprintf("Series %d", i%10), float64(i/10+1), day0)
		e.Book.Authors = []string{"An Author"}
		editions := 12
		if i%10 == 0 {
			editions = 2000
		}
		for j := range editions {
			e.Book.ISBNs = append(e.Book.ISBNs, fmt.Sprintf("978%03d%07d", i, j))
		}
		in.Reads = append(in.Reads, e)
	}
	for b.Loop() {
		Compute(in)
	}
}
