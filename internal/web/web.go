// Package web contains Nextleaf's HTTP server: routing, handlers, and templates.
package web

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"math/rand"
	"net/http"
	"time"
	"unicode"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
)

//go:embed select.html
var templateFS embed.FS

// selectFuncs are template helpers for the selector page.
var selectFuncs = template.FuncMap{
	// firstN caps a string slice so, e.g., a book's long genre list stays tidy.
	"firstN": func(n int, s []string) []string {
		if n < len(s) {
			return s[:n]
		}
		return s
	},
	// ucfirst capitalises the first letter so reason fragments read as sentences.
	"ucfirst": func(s string) string {
		if s == "" {
			return s
		}
		r := []rune(s)
		r[0] = unicode.ToUpper(r[0])
		return string(r)
	},
}

var selectTmpl = template.Must(
	template.New("select.html").Funcs(selectFuncs).ParseFS(templateFS, "select.html"),
)

// server holds the handler's dependencies.
type server struct {
	src library.Source // reading-data source; nil when unconfigured
}

// NewHandler returns the application's HTTP handler. src may be nil, in which
// case the selector explains that no source is configured.
func NewHandler(src library.Source) http.Handler {
	s := &server{src: src}
	mux := http.NewServeMux()
	// {$} matches "/" exactly, so unknown paths fall through to 404 instead of
	// being swallowed by a catch-all root pattern.
	mux.HandleFunc("GET /{$}", s.handleSelect)
	mux.HandleFunc("GET /healthcheck", handleHealthcheck)
	return mux
}

// selectData is the selector page's view model.
type selectData struct {
	Configured bool
	Error      string
	Rec        picker.Recommendation
	HasRec     bool // false when there's nothing to recommend (empty list)
}

func (s *server) handleSelect(w http.ResponseWriter, r *http.Request) {
	data := selectData{Configured: s.src != nil}

	if s.src != nil {
		// "another" flips from the series continuation to a variety pick.
		rec, ok, err := s.recommend(r.Context(), r.URL.Query().Has("another"))
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Rec, data.HasRec = rec, ok
		}
	}

	// Render into a buffer first so a template error yields a clean 500 rather
	// than a half-written response.
	var buf bytes.Buffer
	if err := selectTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// recommend gathers the reading data and produces one recommendation: continue
// an active series (initial load), or a variety-weighted pick (reroll, or when
// no series is in progress).
func (s *server) recommend(ctx context.Context, reroll bool) (picker.Recommendation, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	toRead, err := s.src.ToRead(ctx)
	if err != nil {
		return picker.Recommendation{}, false, err
	}
	reads, err := s.src.RecentReads(ctx, picker.RecentWindow)
	if err != nil {
		return picker.Recommendation{}, false, err
	}
	reading, err := s.src.CurrentlyReading(ctx)
	if err != nil {
		return picker.Recommendation{}, false, err
	}

	if !reroll {
		rec, ok, err := s.continueSeries(ctx, reading, reads, toRead)
		if err != nil {
			return picker.Recommendation{}, false, err
		}
		if ok {
			return rec, true, nil
		}
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rec, ok := picker.Pick(rng, toRead, reads, reading)
	return rec, ok, nil
}

// continueSeries recommends the next book of the series the user is partway
// through, preferring one already on the TBR and otherwise asking the source's
// optional SeriesResolver. ok is false when no series is active or has a next
// book.
func (s *server) continueSeries(ctx context.Context, reading, reads, toRead []library.Entry) (picker.Recommendation, bool, error) {
	anchor, ok := picker.ActiveSeries(reading, reads)
	if !ok {
		return picker.Recommendation{}, false, nil
	}
	// Only push a series the reader actually liked; unrated is allowed.
	if anchor.Rating > 0 && anchor.Rating < picker.SeriesRatingGate {
		return picker.Recommendation{}, false, nil
	}
	series := *anchor.Book.Series
	// Without a known position we can't tell what "next" is; don't guess.
	if series.Position == 0 {
		return picker.Recommendation{}, false, nil
	}

	if entry, ok := picker.NextOnShelves(series, toRead); ok {
		return picker.ContinueSeries(entry.Book, anchor.Rating), true, nil
	}

	if resolver, ok := library.AsSeriesResolver(s.src); ok {
		book, found, err := resolver.NextInSeries(ctx, series)
		if err != nil {
			return picker.Recommendation{}, false, err
		}
		if found {
			return picker.ContinueSeries(book, anchor.Rating), true, nil
		}
	}

	return picker.Recommendation{}, false, nil
}

func handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
