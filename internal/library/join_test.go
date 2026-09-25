package library

import (
	"context"
	"testing"
)

func authored(title string, authors ...string) Entry {
	return Entry{Book: Book{Title: title, Authors: authors}}
}

func withISBNs(e Entry, isbns ...string) Entry {
	e.Book.ISBNs = isbns
	return e
}

func TestKeyIndexJoinsOnASharedISBN(t *testing.T) {
	// The catalogue knows every edition; the library knows the one it holds.
	hc := withISBNs(authored("The Hobbit, or There and Back Again", "J.R.R. Tolkien"), "9780007487318", "9780261102217")
	gm := withISBNs(authored("The Hobbit", "J.R.R. Tolkien", "Alan Lee"), "9780261102217")

	ix := NewKeyIndex([]Entry{hc}, []Entry{gm})
	if ix.Key(hc) != ix.Key(gm) {
		t.Errorf("keys differ despite a shared ISBN:\n  %q\n  %q", ix.Key(hc), ix.Key(gm))
	}
}

func TestKeyIndexMatchesISBN10AgainstISBN13(t *testing.T) {
	a := withISBNs(authored("Animal Farm", "George Orwell"), "0-452-28424-4")
	b := withISBNs(authored("Animal Farm", "C.M. Woodhouse", "George Orwell"), "9780452284241")
	ix := NewKeyIndex([]Entry{a, b})
	if ix.Key(a) != ix.Key(b) {
		t.Error("an ISBN-10 and its ISBN-13 are one number")
	}
}

func TestKeyIndexIgnoresMalformedISBNs(t *testing.T) {
	a := withISBNs(authored("One", "A"), "", "n/a", "123")
	b := withISBNs(authored("Two", "B"), "", "n/a", "123")
	ix := NewKeyIndex([]Entry{a, b})
	if ix.Key(a) == ix.Key(b) {
		t.Error("junk in the ISBN field must never join two books")
	}
}

func TestKeyIndexNeverJoinsOnALikeness(t *testing.T) {
	// Same title and a shared author is what a second copy looks like, and
	// also what an adaptation looks like. Without an ISBN it proves nothing.
	novel := authored("Dune", "Frank Herbert")
	comic := authored("Dune", "Brian Herbert", "Frank Herbert")
	ix := NewKeyIndex([]Entry{novel, comic})
	if ix.Key(novel) == ix.Key(comic) {
		t.Error("two books were joined on a guess")
	}
}

func TestKeyIndexCanonicalKeyIsStable(t *testing.T) {
	a := withISBNs(authored("Animal Farm", "George Orwell"), "9780452284241")
	b := withISBNs(authored("Animal Farm", "C.M. Woodhouse", "George Orwell"), "9780452284241")
	x := NewKeyIndex([]Entry{a, b})
	y := NewKeyIndex([]Entry{b, a})
	if x.Key(a) != y.Key(a) {
		t.Errorf("key depends on encounter order: %q vs %q", x.Key(a), y.Key(a))
	}
	// Statements are anchored to plain book keys, so either copy's own key
	// must lead to the joined book.
	if x.Canonical(BookKey(a)) != x.Key(b) || x.Canonical(BookKey(b)) != x.Key(a) {
		t.Error("a plain key of either copy should resolve to the shared key")
	}
	if x.Canonical("gone\x00nobody") != "gone\x00nobody" {
		t.Error("an unknown key resolves to itself")
	}
}

func TestKeyIndexLeavesUnjoinedBooksOnTheirPlainKey(t *testing.T) {
	e := withISBNs(authored("Dune", "Frank Herbert"), "9780441013593")
	ix := NewKeyIndex([]Entry{e, e})
	if ix.Key(e) != BookKey(e) {
		t.Errorf("Key = %q, want the plain key %q", ix.Key(e), BookKey(e))
	}
	if ix.Key(authored("")) != "" {
		t.Error("a title-less entry keeps the empty never-merge key")
	}
	unseen := authored("Emma", "Jane Austen")
	if ix.Key(unseen) != BookKey(unseen) {
		t.Error("an entry the index never saw keys plainly")
	}
}

func TestMultiToReadJoinsCopiesOnISBN(t *testing.T) {
	m := Combine(
		listSource{name: "a", toRead: []Entry{
			withISBNs(authored("The Hobbit, or There and Back Again", "J.R.R. Tolkien"), "9780261102217"),
			authored("Animal Farm", "George Orwell"),
		}},
		listSource{name: "b", toRead: []Entry{
			withISBNs(authored("The Hobbit", "J.R.R. Tolkien", "Alan Lee"), "9780261102217"),
			authored("Animal Farm", "C.M. Woodhouse", "George Orwell"),
		}},
	)
	got, err := m.ToRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("ToRead = %v, want the ISBN pair folded and the ISBN-less pair left apart", titles(got))
	}
}

func TestMultiToReadSuppressesABookReadUnderAnotherDescription(t *testing.T) {
	m := Combine(
		listSource{name: "a", reads: []Entry{withISBNs(authored("The Hobbit, or There and Back Again", "J.R.R. Tolkien"), "9780261102217")}},
		listSource{name: "b", toRead: []Entry{withISBNs(authored("The Hobbit", "J.R.R. Tolkien", "Alan Lee"), "9780261102217")}},
	)
	got, err := m.ToRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ToRead = %v, want the copy read elsewhere dropped", titles(got))
	}
}

func TestKeyIndexReadsALowercaseCheckDigit(t *testing.T) {
	a := withISBNs(authored("Shadowland", "Peter Straub"), "0-8044-2957-x")
	b := withISBNs(authored("Shadowland", "P. Straub"), "9780804429573")
	ix := NewKeyIndex([]Entry{a, b})
	if ix.Key(a) != ix.Key(b) {
		t.Error("an ISBN-10 ending in a lowercase x is the same number as its ISBN-13")
	}
}

func TestKeyIndexIgnoresOverlongNumbers(t *testing.T) {
	// Two numbers run together are no ISBN, even if one of them is.
	a := withISBNs(authored("One", "A"), "97804522842419780452284241")
	b := withISBNs(authored("Two", "B"), "9780452284241")
	ix := NewKeyIndex([]Entry{a, b})
	if ix.Key(a) == ix.Key(b) {
		t.Error("a run of digits longer than an ISBN joined two books")
	}
}
