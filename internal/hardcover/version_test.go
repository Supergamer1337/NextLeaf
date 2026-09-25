package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// versionAPI answers `me` and the version query, replying to the latter with
// whatever answer holds at the time.
type versionAPI struct {
	mu      sync.Mutex
	answer  string
	status  int
	queries []string
	vars    []map[string]any
}

func (f *versionAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		if strings.Contains(req.Query, "me {") {
			_, _ = io.WriteString(w, `{"data":{"me":[{"id":42}]}}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.queries = append(f.queries, req.Query)
		f.vars = append(f.vars, req.Variables)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		_, _ = io.WriteString(w, f.answer)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func aggregates(toReadCount int, toReadUpdated string) string {
	list := func(count int, updated string) string {
		return `{"aggregate":{"count":` + strconv.Itoa(count) + `,"max":{"updated_at":"` + updated + `","date_added":"2026-09-16","last_read_date":null},"sum":{"rating":null}}}`
	}
	return `{"data":{"reading":` + list(2, "2026-09-24T14:55:51Z") + `,"read":` + list(39, "2026-07-23T14:22:46Z") + `,"toRead":` + list(toReadCount, toReadUpdated) + `}}`
}

func TestVersionAsksAboutEveryListInOneQuery(t *testing.T) {
	api := &versionAPI{answer: aggregates(69, "2026-09-17T11:36:24Z")}
	c := New("tok", WithEndpoint(api.server(t).URL))
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v == "" {
		t.Fatal("an answered version is empty, which vouches for nothing")
	}
	if len(api.queries) != 1 {
		t.Fatalf("asked %d times, want one query for the three lists", len(api.queries))
	}
	q := api.queries[0]
	for _, status := range []string{"status_id: {_eq: 1}", "status_id: {_eq: 2}", "status_id: {_eq: 3}"} {
		if !strings.Contains(q, status) {
			t.Errorf("the version query does not cover %s:\n%s", status, q)
		}
	}
	// What a list is made of: its books, and when each last moved.
	for _, field := range []string{"user_books_aggregate", "count", "updated_at", "last_read_date", "rating"} {
		if !strings.Contains(q, field) {
			t.Errorf("the version query does not ask for %s:\n%s", field, q)
		}
	}
	if api.vars[0]["userID"] != float64(42) {
		t.Errorf("variables = %v, want the reader's own user id", api.vars[0])
	}
}

func TestVersionChangesWhenAListDoes(t *testing.T) {
	api := &versionAPI{answer: aggregates(69, "2026-09-17T11:36:24Z")}
	c := New("tok", WithEndpoint(api.server(t).URL))
	ctx := context.Background()
	first, _ := c.Version(ctx)
	same, _ := c.Version(ctx)
	if first != same {
		t.Errorf("the version moved with nothing changed:\n  %s\n  %s", first, same)
	}

	api.mu.Lock()
	api.answer = aggregates(70, "2026-09-25T09:00:00Z") // a book added
	api.mu.Unlock()
	if added, _ := c.Version(ctx); added == first {
		t.Error("a book added to the list left the version as it was")
	}
}

func TestVersionFailsWhenTheAPIDoes(t *testing.T) {
	api := &versionAPI{status: http.StatusBadGateway}
	c := New("tok", WithEndpoint(api.server(t).URL))
	if v, err := c.Version(context.Background()); err == nil {
		t.Errorf("Version = %q, nil; want the failure, so the lists are fetched instead", v)
	}
}
