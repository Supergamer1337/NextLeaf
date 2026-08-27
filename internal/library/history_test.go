package library

import (
	"context"
	"testing"
	"time"
)

// historySource is a listSource that can also hand back a full read history.
type historySource struct {
	listSource
	history []Entry
}

func (s historySource) ReadHistory(_ context.Context) ([]Entry, error) { return s.history, nil }

func withHistory(name string, titles ...string) historySource {
	entries := make([]Entry, len(titles))
	for i, title := range titles {
		entries[i] = Entry{Book: Book{Title: title}}
	}
	return historySource{listSource: listSource{name: name}, history: entries}
}

func TestAsHistoryProvidersSkipsIncapableSources(t *testing.T) {
	if got := AsHistoryProviders(listSource{name: "plain"}); len(got) != 0 {
		t.Errorf("got %d providers from a plain source, want 0", len(got))
	}
}

func TestAsHistoryProvidersSeesThroughCache(t *testing.T) {
	cached := NewCached(withHistory("cap", "Book A"), time.Minute)

	providers := AsHistoryProviders(cached)
	if len(providers) != 1 {
		t.Fatalf("got %d providers behind the cache, want 1", len(providers))
	}
	history, err := providers[0].ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history) != 1 || history[0].Book.Title != "Book A" {
		t.Errorf("ReadHistory = %+v, want one entry titled Book A", history)
	}
}

func TestAsHistoryProvidersReturnsEachCapableSourceOfAMulti(t *testing.T) {
	// The backfill is best effort per source, so it needs them individually
	// rather than merged: one failing must not hide the other's history.
	m := Combine(withHistory("a", "Book A"), listSource{name: "plain"}, withHistory("b", "Book B"))

	providers := AsHistoryProviders(m)
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2 capable of history", len(providers))
	}
	names := []string{providers[0].Name(), providers[1].Name()}
	if names[0] != "a" || names[1] != "b" {
		t.Errorf("provider names = %v, want [a b]", names)
	}
}
