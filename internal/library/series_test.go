package library

import (
	"context"
	"testing"
	"time"
)

// resolverSource is a listSource that also resolves series — a capable source.
type resolverSource struct {
	listSource
	next  Entry
	found bool
}

func (s resolverSource) NextInSeries(_ context.Context, _ SeriesQuery) (Entry, bool, error) {
	return s.next, s.found, nil
}

func TestAsSeriesResolverDetectsCapability(t *testing.T) {
	// A plain Source is not a resolver.
	if _, ok := AsSeriesResolver(listSource{name: "plain"}); ok {
		t.Error("plain source should not resolve series")
	}

	// A capable source is detected directly.
	capable := resolverSource{listSource: listSource{name: "cap"}, next: Entry{Book: Book{Title: "Book 2"}}, found: true}
	if _, ok := AsSeriesResolver(capable); !ok {
		t.Error("capable source should resolve series")
	}
}

func TestAsSeriesResolverSeesThroughCache(t *testing.T) {
	capable := resolverSource{listSource: listSource{name: "cap"}, next: Entry{Book: Book{Title: "Book 2"}}, found: true}
	cached := NewCached(capable, time.Minute)

	r, ok := AsSeriesResolver(cached)
	if !ok {
		t.Fatal("AsSeriesResolver should see through Cached to a capable source")
	}
	entry, found, err := r.NextInSeries(context.Background(), SeriesQuery{Series: Series{Name: "S", Position: At(1)}})
	if err != nil || !found || entry.Book.Title != "Book 2" {
		t.Errorf("NextInSeries = (%+v, %v, %v), want Book 2/true/nil", entry, found, err)
	}

	// An incapable source behind the cache stays undetected.
	if _, ok := AsSeriesResolver(NewCached(listSource{name: "plain"}, time.Minute)); ok {
		t.Error("cache wrapping a plain source should not resolve series")
	}
}

func TestMultiWithoutCapableSourceReportsUnsupported(t *testing.T) {
	m := Combine(listSource{name: "a"}, listSource{name: "b"})
	if _, ok := AsSeriesResolver(m); ok {
		t.Error("a Multi of incapable sources must not advertise series support")
	}
}

func TestMultiAsksOnlyTheSeriesOwnProvider(t *testing.T) {
	// A series belongs to the backend asserting it. Another backend's
	// catalogue may use the same name for something else, so it is never
	// asked on the first one's behalf.
	m := Combine(
		listSource{name: "plain"},
		resolverSource{listSource: listSource{name: "cap"}, next: Entry{Book: Book{Title: "Next"}}, found: true},
	)
	r, ok := AsSeriesResolver(m)
	if !ok {
		t.Fatal("Multi should resolve series when one source is capable")
	}

	entry, found, err := r.NextInSeries(context.Background(), SeriesQuery{Series: Series{Name: "S", Source: "cap"}})
	if err != nil || !found || entry.Book.Title != "Next" {
		t.Errorf("own provider: NextInSeries = (%+v, %v, %v), want Next/true/nil", entry, found, err)
	}
	for _, provider := range []string{"plain", "stranger", ""} {
		_, found, err := r.NextInSeries(context.Background(), SeriesQuery{Series: Series{Name: "S", Source: provider}})
		if err != nil || found {
			t.Errorf("provider %q: found = %v, err = %v; want nothing from a catalogue that is not its own", provider, found, err)
		}
	}
}

func TestResolvesSeriesSaysWhichProvidersHaveACatalogue(t *testing.T) {
	capable := resolverSource{listSource: listSource{name: "cap"}}
	m := Combine(listSource{name: "plain"}, NewCached(capable, time.Minute))
	if !ResolvesSeries(m, "cap") {
		t.Error("cap has a catalogue, seen through its cache")
	}
	if ResolvesSeries(m, "plain") || ResolvesSeries(m, "stranger") {
		t.Error("a provider without a catalogue must not claim one")
	}
	// A lone source is the only provider there is, whatever its entries call it.
	if !ResolvesSeries(capable, "anything") {
		t.Error("a lone capable source answers for every series it reports")
	}
	if ResolvesSeries(listSource{name: "plain"}, "plain") {
		t.Error("a lone incapable source has no catalogue")
	}
}
