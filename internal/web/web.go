// Package web contains Nextleaf's HTTP server: routing, handlers, and templates.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
	"nextleaf/internal/series"
)

//go:embed select.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

func init() {
	// Not in Go's built-in table, and browsers reject a manifest served as
	// text/plain.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// markSVG is the masthead logo, inlined from the same file browsers fetch for
// the tab icon so the two cannot drift apart.
var markSVG = template.HTML(func() []byte {
	b, err := staticFS.ReadFile("static/icon.svg")
	if err != nil {
		panic(err) // embedded: missing means a broken build, not a runtime fault
	}
	return b
}())

// selectFuncs are template helpers for the selector page.
var selectFuncs = template.FuncMap{
	// firstN caps a string slice so, e.g., a book's long genre list stays tidy.
	"firstN": func(n int, s []string) []string {
		if n < len(s) {
			return s[:n]
		}
		return s
	},
	// dict builds a map for passing several values into a sub-template.
	"dict": func(pairs ...any) map[string]any {
		m := make(map[string]any, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			key, _ := pairs[i].(string)
			m[key] = pairs[i+1]
		}
		return m
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
	// mark renders the logo inline, so it inherits the page's theme colour.
	"mark": func() template.HTML { return markSVG },
}

var selectTmpl = template.Must(
	template.New("select.html").Funcs(selectFuncs).ParseFS(templateFS, "select.html"),
)

// Deps are the handler's collaborators. Engine may be nil, in which case the
// app behaves as it did before series tracking: variety picks only.
type Deps struct {
	Source library.Source // reading-data source; nil when unconfigured
	Engine *series.Engine
}

// server holds the handler's dependencies.
type server struct {
	src    library.Source
	engine *series.Engine
}

// NewHandler returns the application's HTTP handler. d.Source may be nil, in
// which case the selector explains that no source is configured.
func NewHandler(d Deps) http.Handler {
	s := &server{src: d.Source, engine: d.Engine}

	mux := http.NewServeMux()
	// {$} matches "/" exactly, so unknown paths fall through to 404 instead of
	// being swallowed by a catch-all root pattern.
	mux.HandleFunc("GET /{$}", s.handleSelect)
	mux.HandleFunc("POST /series/{action}", s.handleSeriesDecision)
	mux.HandleFunc("GET /cover/{source}/{id}", s.handleCover)
	mux.HandleFunc("GET /healthcheck", handleHealthcheck)
	// Paths line up with the embed, so no prefix stripping is needed.
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	return mux
}

// handleSeriesDecision records a statement from the recommendation card or
// the series drawer, then redirects back so a refresh cannot repeat it.
func (s *server) handleSeriesDecision(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.NotFound(w, r)
		return
	}
	// NextLeaf has no login, so any page on the web could otherwise post a
	// decision through the reader's own browser. Browsers stamp cross-site
	// requests with Sec-Fetch-Site; its absence (curl, old browsers) is not
	// evidence of one.
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		http.Error(w, "cross-origin requests are refused", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "a series name is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	err := s.engine.Decide(ctx, r.PathValue("action"), name, strings.TrimSpace(r.FormValue("to")))
	switch {
	case errors.Is(err, series.ErrUnknownAction):
		http.NotFound(w, r)
		return
	case errors.Is(err, series.ErrUnknownSeries), errors.Is(err, series.ErrNotAnAlternative):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, "could not record that decision", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleCover relays a cover image from the source holding it, for providers
// (like Grimmory) whose covers sit behind their own authentication. Browsers
// cache the result, so repeats rarely reach the backend.
func (s *server) handleCover(w http.ResponseWriter, r *http.Request) {
	provider, ok := library.AsCoverProvider(s.src, r.PathValue("source"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	body, contentType, err := provider.CoverImage(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = body.Close() }()

	// Backends can mislabel image bytes (Grimmory says application/json);
	// only pass through image types and let the response writer sniff the rest.
	if strings.HasPrefix(contentType, "image/") {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = io.Copy(w, body)
}

// selectData is the selector page's view model.
type selectData struct {
	Configured bool
	Error      string
	Rec        picker.Recommendation
	HasRec     bool // false when there's nothing to recommend (empty list)
	// Stale lists sources whose fetch failed, so the page can say the data
	// shown for them is old rather than quietly pretending it is fresh.
	Stale []library.Health
	// Decidable is true when the recommended book belongs to a series the
	// reader has actually read into, which is what makes park/drop/pin
	// meaningful for it. Decide names that series, which is not always the one
	// the book itself is labelled with.
	Decidable bool
	Decide    string
	Panel     panel
}

// panel is the series drawer: every tracked series, grouped by what applies
// to it. Finished sits last because it is a record of what is done, not a
// list of anything to act on.
type panel struct {
	Pinned   []series.Group
	Active   []series.Group
	Parked   []series.Group
	Dropped  []series.Group
	Finished []series.Group
}

// Count is how many series the drawer holds, for the toggle's label.
func (p panel) Count() int {
	return len(p.Pinned) + len(p.Active) + len(p.Parked) + len(p.Dropped) + len(p.Finished)
}

// Any reports whether there is anything worth opening the drawer for.
func (p panel) Any() bool { return p.Count() > 0 }

// group sorts the view's series into the drawer's sections. Being caught up
// is a fact rather than a decision, so it only files a series under Finished
// when the reader has made no decision of their own about it.
func group(v series.View) panel {
	var p panel
	for _, g := range v.Groups {
		switch {
		case g.Decision == series.Pinned:
			p.Pinned = append(p.Pinned, g)
		case g.Decision == series.Parked:
			p.Parked = append(p.Parked, g)
		case g.Decision == series.Dropped:
			p.Dropped = append(p.Dropped, g)
		case g.CaughtUp:
			p.Finished = append(p.Finished, g)
		default:
			p.Active = append(p.Active, g)
		}
	}
	return p
}

func (s *server) handleSelect(w http.ResponseWriter, r *http.Request) {
	data := selectData{Configured: s.src != nil}

	if s.src != nil && s.engine != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// "another" flips from the series continuation to a variety pick.
		rec, view, err := s.engine.Recommend(ctx, r.URL.Query().Has("another"))
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Rec, data.HasRec = rec.Rec, rec.OK
			data.Decidable, data.Decide = rec.Decidable, rec.Group
			data.Panel = group(view)
		}
		for _, h := range library.HealthOf(s.src) {
			if h.Stale {
				data.Stale = append(data.Stale, h)
			}
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

func handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
