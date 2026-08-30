// Package web contains Nextleaf's HTTP server: routing, handlers, and templates.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
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

//go:embed layout.html view.html
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
	// hxvals JSON-encodes key/value pairs for a button's hx-vals attribute, so
	// a decision travels without a form wrapped around it.
	"hxvals": func(pairs ...string) (string, error) {
		m := make(map[string]string, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			m[pairs[i]] = pairs[i+1]
		}
		b, err := json.Marshal(m)
		return string(b), err
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

// shellHTML is the constant document every visit starts from: styles,
// masthead, and the mount the card and drawer are morphed into. It reads no
// source, so it cannot be slow and it cannot fail.
var shellHTML = func() []byte {
	t := template.Must(template.New("layout.html").Funcs(selectFuncs).ParseFS(templateFS, "layout.html", "view.html"))
	var buf bytes.Buffer
	if err := t.Execute(&buf, nil); err != nil {
		panic(err) // embedded and data-free: a failure here is a broken build
	}
	return buf.Bytes()
}()

// viewTmpl renders the card and the drawer, which are swapped as one so the
// two can never show state from two different reads.
var viewTmpl = template.Must(
	template.New("view.html").Funcs(selectFuncs).ParseFS(templateFS, "view.html"),
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
	mux.HandleFunc("GET /{$}", handleShell)
	mux.HandleFunc("GET /view", s.handleView)
	mux.HandleFunc("POST /series/{action}", s.handleSeriesDecision)
	mux.HandleFunc("GET /cover/{source}/{id}", s.handleCover)
	mux.HandleFunc("GET /healthcheck", handleHealthcheck)
	// Paths line up with the embed, so no prefix stripping is needed. The
	// assets are vendored and change only with a deploy, so a day-long cache
	// costs at worst one stale day after one.
	static := http.FileServerFS(staticFS)
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		static.ServeHTTP(w, r)
	}))
	return mux
}

// handleSeriesDecision records a statement from the recommendation card or
// the series drawer, then returns the refreshed view to swap in place.
func (s *server) handleSeriesDecision(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		flash(w, "Series tracking is not running, so there is nothing to record it in. Check the server log and restart.", http.StatusNotFound)
		return
	}
	// NextLeaf has no login, so any page on the web could otherwise post a
	// decision through the reader's own browser. Browsers stamp cross-site
	// requests with Sec-Fetch-Site; its absence (curl, old browsers) is not
	// evidence of one.
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		flash(w, "cross-origin requests are refused", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		flash(w, "a series name is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	action := r.PathValue("action")
	uncached, err := s.engine.Decide(ctx, action, name, strings.TrimSpace(r.FormValue("to")))
	switch {
	case errors.Is(err, series.ErrUnknownAction):
		flash(w, "that is not something you can do to a series", http.StatusNotFound)
		return
	case errors.Is(err, series.ErrUnknownSeries), errors.Is(err, series.ErrNotAnAlternative):
		flash(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		flash(w, "could not record that decision", http.StatusInternalServerError)
		return
	}
	// A decision re-renders without asking the catalogue anything, so it cannot
	// be stretched by a slow one — except when Decide says the decision left
	// the group with no cached answer, which is the one case the re-render must
	// be allowed to ask, or the row comes back with nothing next.
	data := s.viewOf(ctx, false, uncached)
	// A decision made in the drawer shows its effect where the reader is
	// standing — the row moves, undo alongside — so only card decisions get
	// the confirmation banner.
	if r.FormValue("from") != "drawer" {
		data.Done = doneFor(action, name, strings.TrimSpace(r.FormValue("to")))
	}
	renderView(w, data, http.StatusOK)
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

// done is the confirmation a recorded decision answers with, paired with the
// undo that reverses it. It lands in the notice slot, so the next successful
// render clears it on its own.
type done struct {
	Msg        string
	UndoLabel  string
	UndoAction string // the /series/{action} that reverses the decision
	UndoName   string
	UndoTo     string // only a reverse switch names a destination
}

// doneFor phrases the confirmation for a decision. A clear is itself an undo,
// so its confirmation ends the chain rather than offering another one.
func doneFor(action, name, to string) *done {
	switch action {
	case "park":
		return &done{Msg: "Parked “" + name + "” for one book.", UndoLabel: "Resume now", UndoAction: "clear", UndoName: name}
	case "drop":
		return &done{Msg: "Dropped “" + name + "”.", UndoLabel: "Undrop", UndoAction: "clear", UndoName: name}
	case "pin":
		return &done{Msg: "Pinned “" + name + "” to read next.", UndoLabel: "Unpin", UndoAction: "clear", UndoName: name}
	case "switch":
		return &done{Msg: "Now tracking “" + to + "”.", UndoLabel: "Switch back", UndoAction: "switch", UndoName: to, UndoTo: name}
	case "clear":
		return &done{Msg: "“" + name + "” is back in the running."}
	}
	return nil
}

// viewData is the fragment's view model: the card and the drawer together.
type viewData struct {
	// Done is the decision just recorded, when the render answers one.
	Done       *done
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
	// Continuation is true when the card holds the next book in a series
	// rather than a variety pick. Such a card offers no reroll: stepping past
	// a series is a park, so the decision is recorded rather than given away.
	Continuation bool
	Panel        panel
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

// handleShell serves the constant document. It never reads a source, so the
// browser paints immediately and the card arrives on its own.
func handleShell(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(shellHTML)
}

// handleView renders the card and drawer as one fragment. "another" flips
// from the series continuation to a variety pick.
func (s *server) handleView(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	renderView(w, s.viewOf(ctx, r.URL.Query().Has("another"), true), http.StatusOK)
}

// viewOf builds the fragment's model. catalogue says whether this render may
// spend fresh next-in-series lookups; the decision path passes false so
// recording a park never waits on a slow backend.
func (s *server) viewOf(ctx context.Context, reroll, catalogue bool) viewData {
	data := viewData{Configured: s.src != nil}
	if s.src == nil || s.engine == nil {
		return data
	}

	var (
		rec  series.Recommendation
		view series.View
		err  error
	)
	if catalogue {
		rec, view, err = s.engine.Recommend(ctx, reroll)
	} else {
		rec, view, err = s.engine.RecommendWithin(ctx, reroll, 0)
	}
	if err != nil {
		data.Error = err.Error()
	} else {
		data.Rec, data.HasRec = rec.Rec, rec.OK
		data.Decidable, data.Decide = rec.Decidable, rec.Group
		data.Continuation = rec.Continuation
		data.Panel = group(view)
	}
	for _, h := range library.HealthOf(s.src) {
		if h.Stale {
			data.Stale = append(data.Stale, h)
		}
	}
	return data
}

// renderView writes the fragment. It renders into a buffer first so a template
// error yields a clean 500 rather than a half-written response.
func renderView(w http.ResponseWriter, data viewData, status int) {
	var buf bytes.Buffer
	if err := viewTmpl.ExecuteTemplate(&buf, "view", data); err != nil {
		// Nothing is written yet, so the failure can still be steered at the
		// notice slot rather than swapped over the card. Written literally: the
		// templates are what just failed.
		retargetToFlash(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `<p class="notice notice--error">Something went wrong showing that. Try again.</p>`)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// retargetToFlash steers htmx at the notice slot. Error responses are swapped
// by configuration, so without this an error would land over the card.
func retargetToFlash(w http.ResponseWriter) {
	w.Header().Set("HX-Retarget", "#flash")
	w.Header().Set("HX-Reswap", "innerHTML")
}

// flash reports a refusal as a swappable fragment. The status stays honest;
// HX-Retarget steers the swap into the notice slot, which htmx would not
// otherwise write to on a 4xx or 5xx.
func flash(w http.ResponseWriter, msg string, status int) {
	var buf bytes.Buffer
	if err := viewTmpl.ExecuteTemplate(&buf, "flash", msg); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	retargetToFlash(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
