package hardcover

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
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
