// Package web contains Nextleaf's HTTP server: routing, handlers, and templates.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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
	// anyPending reports whether a drawer section holds a series still being
	// checked, so a folded section can say so.
	"anyPending": func(groups []series.Group) bool {
		for _, g := range groups {
			if g.Pending() {
				return true
			}
		}
		return false
	},
}

// staticETags names each embedded asset by its content. Embedded files have
// no modification time, so the file server has nothing else to validate
// against. The tags are weak: compressed and plain copies share them.
var staticETags = func() map[string]string {
	tags := map[string]string{}
	err := fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		tags[path] = `W/"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if err != nil {
		panic(err) // embedded: unreadable means a broken build
	}
	return tags
}()

// pageTmpl renders the whole page: styles, masthead, and the places the card
// and the drawer's pieces live.
var pageTmpl = template.Must(template.New("layout.html").Funcs(selectFuncs).ParseFS(templateFS, "layout.html", "view.html"))

// page is what the page is rendered from: the view it carries, or none.
type page struct{ View *viewData }

// shellHTML is the page before the library is first held: a skeleton, and a
// request for the card once the page is up. It reads no source, so it cannot
// be slow and it cannot fail.
var shellHTML = func() []byte {
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, page{}); err != nil {
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
	// Wait is how long a drawer refresh waits for something new before
	// answering anyway; zero means the default. Kept under common proxy
	// timeouts.
	Wait time.Duration
	// LoadFresh is how old the library may be before a page load refreshes
	// it behind the page; zero means the default.
	LoadFresh time.Duration
	// Draining, when closed, ends every wait for a change: a server shutting
	// down answers what is waiting rather than holding it open.
	Draining <-chan struct{}
}

// server holds the handler's dependencies.
type server struct {
	src       library.Source
	engine    *series.Engine
	wait      time.Duration
	loadFresh time.Duration
	draining  <-chan struct{}
}

// NewHandler returns the application's HTTP handler. d.Source may be nil, in
// which case the selector explains that no source is configured.
func NewHandler(d Deps) http.Handler {
	s := &server{src: d.Source, engine: d.Engine, wait: d.Wait, loadFresh: d.LoadFresh, draining: d.Draining}
	if s.wait == 0 {
		s.wait = 20 * time.Second
	}
	if s.loadFresh == 0 {
		s.loadFresh = 30 * time.Second
	}

	mux := http.NewServeMux()
	// {$} matches "/" exactly, so unknown paths fall through to 404 instead of
	// being swallowed by a catch-all root pattern.
	mux.HandleFunc("GET /{$}", s.handleShell)
	mux.HandleFunc("GET /view", s.handleView)
	mux.HandleFunc("POST /series/{action}", s.handleSeriesDecision)
	mux.HandleFunc("GET /cover/{source}/{id}", s.handleCover)
	mux.HandleFunc("GET /healthcheck", handleHealthcheck)
	// Paths line up with the embed, so no prefix stripping is needed. The
	// assets are vendored and change only with a deploy, so a day-long cache
	// costs at worst one stale day after one. Past that day the ETag lets the
	// browser revalidate what it holds instead of fetching it again.
	static := http.FileServerFS(staticFS)
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if tag, ok := staticETags[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("ETag", tag)
		}
		static.ServeHTTP(w, r)
	}))
	return compressed(mux)
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
	s.visit()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	action := r.PathValue("action")
	source := strings.TrimSpace(r.FormValue("source"))
	uncached, err := s.engine.Decide(ctx, action, name, source, strings.TrimSpace(r.FormValue("to")))
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
	data := s.viewOf(ctx, false, uncached, "")
	data.WithPanel = r.FormValue("panel") != ""
	// A decision made in the drawer shows its effect where the reader is
	// standing — the row moves, undo alongside — so only card decisions get
	// the confirmation banner.
	if r.FormValue("from") != "drawer" {
		data.Done = doneFor(action, name, source, strings.TrimSpace(r.FormValue("to")))
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
	// A versioned URL names one cover for good: a new cover is a new URL.
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
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
	UndoSource string
	UndoTo     string // only a reverse switch names a destination
}

// doneFor phrases the confirmation for a decision. A clear is itself an undo,
// so its confirmation ends the chain rather than offering another one.
func doneFor(action, name, source, to string) *done {
	switch action {
	case "park":
		return &done{Msg: "Parked “" + name + "” for one book.", UndoLabel: "Resume now", UndoAction: "clear", UndoName: name, UndoSource: source}
	case "drop":
		return &done{Msg: "Dropped “" + name + "”.", UndoLabel: "Undrop", UndoAction: "clear", UndoName: name, UndoSource: source}
	case "pin":
		return &done{Msg: "Pinned “" + name + "” to read next.", UndoLabel: "Unpin", UndoAction: "clear", UndoName: name, UndoSource: source}
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
	Decidable    bool
	Decide       string
	DecideSource string
	// Continuation is true when the card holds the next book in a series
	// rather than a variety pick. Such a card offers no reroll: stepping past
	// a series is a park, so the decision is recorded rather than given away.
	Continuation bool
	Panel        panel
	// Gen is the engine's generation when the drawer was rendered, so a
	// waiting drawer can ask for whatever lands after it. Listening is false
	// with no series tracking, when there is nothing to listen for.
	Gen       uint64
	Listening bool
	// Settled marks the render in which a waiting drawer got its last answer.
	Settled bool
	// FollowUp is the library generation a page was painted from, when a
	// refresh behind it may bring something newer. CardKey names the book on
	// the card, so the follow-up can leave it there; it is query-escaped, as
	// html/template does not know hx-get for a URL.
	FollowUp string
	CardKey  string
	// StaleKey is the query-escaped staleKey the page was painted with.
	StaleKey string
	// WithPanel is set once the page has opened its drawer: only then do the
	// drawer's rows, and their covers, travel with the rest.
	WithPanel bool
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
	// Continuable holds series finished on their own provider that another
	// carries on past, counted apart from the ones that are done.
	Continuable []series.Group
	// Pending is true while any answer is still to come; Checking counts the
	// series waiting on one.
	Pending  bool
	Checking int
	// Unanswered is true when a series' catalogue keeps failing it, so the
	// drawer is not up to date even with nothing pending.
	Unanswered bool
}

// Count is how many series the drawer holds, for the toggle's label.
func (p panel) Count() int {
	return len(p.Pinned) + len(p.Active) + len(p.Parked) + len(p.Dropped) + len(p.Finished) + len(p.Continuable)
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
		case g.ContinueOn != nil:
			p.Continuable = append(p.Continuable, g)
		case g.CaughtUp:
			p.Finished = append(p.Finished, g)
		default:
			p.Active = append(p.Active, g)
		}
		if g.Pending() {
			p.Pending = true
			p.Checking++
		}
		p.Unanswered = p.Unanswered || g.Unanswered
	}
	return p
}

// handleShell serves the page. With the library held it carries the card, so
// the recommendation is in the first paint; rendering it reads only what is
// held and waits on no backend. Until the library is first held the page is
// the skeleton, and fetches the card once it is up.
func (s *server) handleShell(w http.ResponseWriter, r *http.Request) {
	s.visit()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.engine == nil {
		_, _ = w.Write(shellHTML)
		return
	}
	if at, _ := s.engine.Library(); at.IsZero() {
		_, _ = w.Write(shellHTML)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	data := s.pageView(ctx, false)
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, page{View: &data}); err != nil {
		_, _ = w.Write(shellHTML) // the skeleton still fetches the card
		return
	}
	_, _ = buf.WriteTo(w)
}

// handleView renders the card and drawer as one fragment. "another" flips
// from the series continuation to a variety pick; "drawer" asks for the
// drawer alone, and "after" for the follow-up to a page painted from an old
// library. "panel" says the page has opened its drawer, so the drawer's rows
// travel too; until then only its toggle and status do.
//
// A page load never waits on a backend. It paints from what is held and asks
// the catalogue nothing; the background pass does that. If the library is
// getting old, it is refreshed behind the page, and the page follows up for
// the result, so a book just added to a list still shows at once.
func (s *server) handleView(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	q := r.URL.Query()
	panel := q.Has("panel")
	switch {
	case q.Has("drawer"):
		s.refreshDrawer(ctx, w, q.Get("since"), q.Has("waiting"), panel)
		return
	case q.Has("after"):
		s.followUp(ctx, w, q.Get("after"), q.Get("keep"), q.Get("stale"), panel)
		return
	}
	s.visit()
	data := s.pageView(ctx, q.Has("another"))
	data.WithPanel = panel
	renderView(w, data, http.StatusOK)
}

// pageView is the view a page load shows. It paints from what is held and asks
// the catalogue nothing; if the library is getting old it is refreshed behind
// the page, with a follow-up set for the result.
func (s *server) pageView(ctx context.Context, reroll bool) viewData {
	var followUp string
	if s.engine != nil {
		at, gen := s.engine.Library()
		if time.Since(at) > s.loadFresh {
			s.engine.RefreshLibrary()
			// With nothing held yet the render fetches for itself, so there is
			// nothing newer to follow up with; and a follow-up would undo a
			// reroll.
			if !at.IsZero() && !reroll {
				followUp = strconv.FormatUint(gen, 10)
			}
		}
	}
	data := s.viewOf(ctx, reroll, false, "")
	data.FollowUp = followUp
	return data
}

// visit tells the engine a reader is here, as opposed to a tab listening on
// its own, so the background pass keeps to its schedule.
func (s *server) visit() {
	if s.engine != nil {
		s.engine.Visit()
	}
}

// followUp answers a page painted from an old library. Once the refresh behind
// it is done, it answers with the whole view again if the library changed
// since generation seen, or if the sources down are no longer those in stale,
// the page's own staleKey; and 204, nothing to swap, if neither. The book
// keyed keep stays on the card unless something should take its place.
func (s *server) followUp(ctx context.Context, w http.ResponseWriter, seen, keep, stale string, panel bool) {
	after, err := strconv.ParseUint(seen, 10, 64)
	if s.engine == nil || err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if done := s.engine.LibraryRefreshing(); done != nil {
		select {
		case <-done:
		case <-time.After(s.wait):
		case <-s.draining:
		case <-ctx.Done():
			return
		}
	}
	if _, gen := s.engine.Library(); gen <= after && staleKey(library.HealthOf(s.src)) == stale {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	data := s.viewOf(ctx, false, false, keep)
	data.WithPanel = panel
	renderView(w, data, http.StatusOK)
}

// staleKey names the sources serving old data, for comparing what a page said
// with what holds now.
func staleKey(health []library.Health) string {
	var down []string
	for _, h := range health {
		if h.Stale {
			down = append(down, h.Source)
		}
	}
	slices.Sort(down)
	return strings.Join(down, ",")
}

// refreshDrawer renders the drawer alone, for an open page listening for
// change. Given the generation it last saw, it waits for the next change, up
// to s.wait, so the drawer hears of it at once without polling; with nothing
// by then it answers 204, and the page listens again. It asks nothing
// itself: the background pass does the fetching, and is nudged if anything
// is still missing.
//
// The card is left alone on purpose: re-running the pick would deal the
// reader a different book on every refresh. A failure answers an error, so
// the page keeps the drawer it has and waits before asking again.
func (s *server) refreshDrawer(ctx context.Context, w http.ResponseWriter, since string, waiting, panel bool) {
	if s.engine == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if seen, err := strconv.ParseUint(since, 10, 64); err == nil {
		if gen, changed := s.engine.Changes(); gen <= seen {
			select {
			case <-changed:
			case <-time.After(s.wait):
				w.WriteHeader(http.StatusNoContent)
				return
			case <-s.draining:
			case <-ctx.Done():
				return
			}
		}
	}
	// Read before rendering: an answer landing mid-render sends the drawer
	// straight back for it.
	gen, _ := s.engine.Changes()
	view, err := s.engine.ViewCached(ctx)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	data := viewData{Panel: group(view), Gen: gen, Listening: true, WithPanel: panel}
	if data.Panel.Pending {
		s.engine.Nudge()
	}
	// A drawer that was waiting and is not any more has just settled.
	data.Settled = waiting && !data.Panel.Pending
	var buf bytes.Buffer
	if err := viewTmpl.ExecuteTemplate(&buf, "drawerRefresh", data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// viewOf builds the fragment's model. catalogue says whether this render may
// spend fresh next-in-series lookups: only a decision that left its row with
// nothing known does, so the result of the reader's click shows in one step.
func (s *server) viewOf(ctx context.Context, reroll, catalogue bool, keep string) viewData {
	data := viewData{Configured: s.src != nil}
	if s.src == nil || s.engine == nil {
		return data
	}

	var (
		rec  series.Recommendation
		view series.View
		err  error
	)
	data.Gen, _ = s.engine.Changes()
	data.Listening = true
	switch {
	case catalogue:
		rec, view, err = s.engine.Recommend(ctx, reroll)
	case keep != "":
		rec, view, err = s.engine.RecommendKeeping(ctx, keep, 0)
	default:
		rec, view, err = s.engine.RecommendWithin(ctx, reroll, 0)
	}
	if err != nil {
		data.Error = err.Error()
	} else {
		data.Rec, data.HasRec = rec.Rec, rec.OK
		if rec.OK {
			data.CardKey = url.QueryEscape(library.BookKey(rec.Rec.Entry))
		}
		data.Decidable, data.Decide, data.DecideSource = rec.Decidable, rec.Group, rec.Source
		data.Continuation = rec.Continuation
		data.Panel = group(view)
		if data.Panel.Pending {
			s.engine.Nudge()
		}
	}
	health := library.HealthOf(s.src)
	for _, h := range health {
		if h.Stale {
			data.Stale = append(data.Stale, h)
		}
	}
	data.StaleKey = url.QueryEscape(staleKey(health))
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
