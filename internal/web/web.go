// Package web contains Nextleaf's HTTP server: routing, handlers, and templates.
package web

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"net/http"
	"time"

	"nextleaf/internal/library"
)

const appName = "Nextleaf"

//go:embed home.html library.html
var templateFS embed.FS

// libraryFuncs are template helpers for the library page.
var libraryFuncs = template.FuncMap{
	// firstN caps a string slice so, e.g., a book's long genre list stays tidy.
	"firstN": func(n int, s []string) []string {
		if n < len(s) {
			return s[:n]
		}
		return s
	},
}

var (
	homeTmpl    = template.Must(template.ParseFS(templateFS, "home.html"))
	libraryTmpl = template.Must(template.New("library.html").Funcs(libraryFuncs).ParseFS(templateFS, "library.html"))
)

type homeData struct {
	Title   string
	Heading string
	Message string
}

// server holds the handler's dependencies.
type server struct {
	src library.Source // reading-data source; nil when unconfigured
}

// NewHandler returns the application's HTTP handler. src may be nil, in which
// case the library page explains that no source is configured.
func NewHandler(src library.Source) http.Handler {
	s := &server{src: src}
	mux := http.NewServeMux()
	// {$} matches "/" exactly, so unknown paths fall through to 404 instead of
	// being swallowed by a catch-all root pattern.
	mux.HandleFunc("GET /{$}", handleHome)
	mux.HandleFunc("GET /library", s.handleLibrary)
	mux.HandleFunc("GET /healthcheck", handleHealthcheck)
	return mux
}

func handleHome(w http.ResponseWriter, _ *http.Request) {
	data := homeData{
		Title:   appName,
		Heading: appName,
		Message: "Recommends what to read next — variety over similarity.",
	}

	// Render into a buffer first so a template error yields a clean 500 rather
	// than a half-written response.
	var buf bytes.Buffer
	if err := homeTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// libraryData is the library page's view model.
type libraryData struct {
	Configured bool
	Error      string
	Reading    []library.Entry
	Reads      []library.Entry
	ToRead     []library.Entry
}

func (s *server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	data := libraryData{Configured: s.src != nil}

	if s.src != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		var err error
		if data.Reading, err = s.src.CurrentlyReading(ctx); err == nil {
			if data.Reads, err = s.src.RecentReads(ctx, 10); err == nil {
				data.ToRead, err = s.src.ToRead(ctx)
			}
		}
		if err != nil {
			data.Error = err.Error()
		}
	}

	// Render into a buffer first so a template error yields a clean 500 rather
	// than a half-written response.
	var buf bytes.Buffer
	if err := libraryTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
