package series

import (
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
