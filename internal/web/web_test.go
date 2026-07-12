package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nextleaf/internal/library"
)

// stubSource is a library.Source with canned results for handler tests.
type stubSource struct {
	reading   []library.Entry
	reads     []library.Entry
	toRead    []library.Entry
	readsErr  error
	toReadErr error
}

func (s stubSource) Name() string { return "stub" }
func (s stubSource) CurrentlyReading(_ context.Context) ([]library.Entry, error) {
	return s.reading, nil
}
func (s stubSource) RecentReads(_ context.Context, _ int) ([]library.Entry, error) {
	return s.reads, s.readsErr
}
func (s stubSource) ToRead(_ context.Context) ([]library.Entry, error) {
	return s.toRead, s.toReadErr
}

func TestHome(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, appName) {
		t.Errorf("body does not contain %q:\n%s", appName, body)
	}
}

func TestHealthcheck(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthcheck", nil)
	rec := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (root pattern must not be a catch-all)", rec.Code, http.StatusNotFound)
	}
}

func TestLibraryRendersEntries(t *testing.T) {
	src := stubSource{
		reading: []library.Entry{{Book: library.Book{Title: "Deep Work"}, Status: library.StatusCurrentlyRead}},
		reads:   []library.Entry{{Book: library.Book{Title: "Piranesi", Authors: []string{"Susanna Clarke"}}, Status: library.StatusRead}},
		toRead:  []library.Entry{{Book: library.Book{Title: "Babel"}, Status: library.StatusWantToRead}},
	}
	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	rec := httptest.NewRecorder()

	NewHandler(src).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{"Deep Work", "Piranesi", "Susanna Clarke", "Babel"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestLibraryUnconfigured(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	rec := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, "HARDCOVER_TOKEN") {
		t.Errorf("unconfigured page should mention HARDCOVER_TOKEN:\n%s", body)
	}
}

func TestLibrarySourceError(t *testing.T) {
	src := stubSource{readsErr: errors.New("boom")}
	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	rec := httptest.NewRecorder()

	NewHandler(src).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, "boom") {
		t.Errorf("error page should surface the failure:\n%s", body)
	}
}
