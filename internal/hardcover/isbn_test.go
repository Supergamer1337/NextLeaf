package hardcover

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"nextleaf/internal/library"
)

const isbnResponse = `{"data":{"user_books":[{
  "status_id": 1,
  "book": {
    "id": 7, "title": "The Hobbit, or There and Back Again",
    "isbn_editions": [
      {"isbn_13": "9780007487318", "isbn_10": "0007487312"},
      {"isbn_13": null, "isbn_10": "0261102214"},
      {"isbn_13": "9780007487318", "isbn_10": null}
    ]
  }
}]}}`

func TestShelfEntriesCarryEveryEditionsISBNs(t *testing.T) {
	// The reader's copy elsewhere is whichever edition they happened to buy,
	// so only the full list can match it.
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "me { id }") {
			_, _ = io.WriteString(w, `{"data":{"me":[{"id":42}]}}`)
			return
		}
		query = string(body)
		_, _ = io.WriteString(w, isbnResponse)
	}))
	defer srv.Close()

	got, err := New("tok", WithEndpoint(srv.URL)).ToRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "isbn_editions: editions") {
		t.Errorf("shelf query does not ask for edition ISBNs:\n%s", query)
	}
	want := []string{"9780007487318", "0007487312", "0261102214"}
	if len(got) != 1 || !slices.Equal(got[0].Book.ISBNs, want) {
		t.Errorf("ISBNs = %v, want %v (each once, nulls skipped)", got[0].Book.ISBNs, want)
	}
}

func TestSeriesLookupDoesNotFetchISBNs(t *testing.T) {
	// A catalogue answer is never joined against a shelf, and popular books
	// have hundreds of editions.
	if strings.Contains(seriesBookFields, "isbn") {
		t.Error("the series lookup should not pay for ISBNs")
	}
}

func TestNextInSeriesMatchesOnHardcoversOwnIdentifier(t *testing.T) {
	// Two Hardcover series can share a name; the slug cannot be shared.
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		_, _ = io.WriteString(w, `{"data":{"book_series":[]}}`)
	}))
	defer srv.Close()
	c := New("tok", WithEndpoint(srv.URL))

	q := library.SeriesQuery{Series: library.Series{Name: "The Witcher", Slug: "the-witcher", Position: library.At(1)}}
	if _, _, err := c.NextInSeries(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	q.Series.Slug = ""
	if _, _, err := c.NextInSeries(context.Background(), q); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(bodies[0], "slug: {_eq: $series}") || !strings.Contains(bodies[0], `"series":"the-witcher"`) {
		t.Errorf("with a slug, the query should match on it:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[1], "name: {_eq: $series}") || !strings.Contains(bodies[1], `"series":"The Witcher"`) {
		t.Errorf("without one, the name is all there is:\n%s", bodies[1])
	}
}

func TestSeriesByISBNReportsEachBooksSeries(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `{"data":{"editions":[
		  {"isbn_13": "9780765377104", "isbn_10": "0765377101", "book": {"id": 1, "title": "Death's End", "book_series": [
		    {"position": 3, "featured": true, "series": {"name": "Remembrance of Earth's Past", "slug": "remembrance", "books_count": 4}}
		  ]}},
		  {"isbn_13": "9780000000002", "isbn_10": null, "book": {"id": 2, "title": "Standalone", "book_series": []}}
		]}}`)
	}))
	defer srv.Close()

	got, err := New("tok", WithEndpoint(srv.URL)).SeriesByISBN(context.Background(),
		[]string{"0-7653-7710-1", "9780000000002", "9789999999999"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"0765377101"`) {
		t.Errorf("ISBNs should be sent without punctuation:\n%s", body)
	}
	claims := got["0-7653-7710-1"]
	if len(claims) != 1 || claims[0].Slug != "remembrance" || claims[0].Source != "hardcover" {
		t.Errorf("claims = %+v, want the series keyed by the ISBN as it was given", claims)
	}
	if pos, ok := claims[0].Slot(); !ok || pos != 3 {
		t.Errorf("position = %v, %v; want 3", pos, ok)
	}
	if len(got) != 1 {
		t.Errorf("got = %+v, want a standalone and an unknown ISBN both absent", got)
	}
}

func TestSeriesByISBNAsksNothingForNoISBNs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no ISBNs, no question")
	}))
	defer srv.Close()
	if got, err := New("tok", WithEndpoint(srv.URL)).SeriesByISBN(context.Background(), []string{"", "n/a"}); err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}
