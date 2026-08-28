// Package web contains Nextleaf's HTTP server: routing, handlers, and templates.
package web

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"io"
	"log"
	"math/rand"
	"mime"
	"net/http"
	"slices"
	"strconv"
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

// Deps are the handler's collaborators. Store and Backfill may be nil, in which
// case the app behaves as it did before series tracking: variety picks only.
type Deps struct {
	Source   library.Source // reading-data source; nil when unconfigured
	Store    *series.Store
	Backfill *series.Backfill
	Prefs    picker.Prefs
}

// server holds the handler's dependencies.
type server struct {
	src       library.Source
	store     *series.Store
	backfill  *series.Backfill
	prefs     picker.Prefs
	lookahead *series.Lookahead
}

// lookaheadTTL is how long a next-in-series answer is reused. A book is
// published at most once a day, so asking more often only burns rate limit.
const lookaheadTTL = 24 * time.Hour

// maxSeriesLookups caps how many series a single page load will ask the source
// about. A long-time reader is tracked in hundreds of series, nearly all of
// them finished with nothing shelved, and asking about every one on a cold
// cache would spend the request's whole deadline. The ranking puts the series
// most likely to have something to offer first, so a small cap loses little.
const maxSeriesLookups = 4

// NewHandler returns the application's HTTP handler. d.Source may be nil, in
// which case the selector explains that no source is configured.
func NewHandler(d Deps) http.Handler {
	s := &server{src: d.Source, store: d.Store, backfill: d.Backfill, prefs: d.Prefs}
	if resolver, ok := library.AsSeriesResolver(d.Source); ok {
		s.lookahead = series.NewLookahead(resolver, lookaheadTTL)
	}

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

// handleSeriesDecision records a standing decision from the recommendation card
// or the series panel, then redirects back so a refresh cannot repeat it.
func (s *server) handleSeriesDecision(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var err error
	switch r.PathValue("action") {
	case "park":
		// The park is spent by the next book finished after this moment, so it
		// is anchored to the newest finish the sources currently report.
		// Without that anchor the park would be cleared by the very next
		// reconcile, so a failure here refuses the park rather than recording
		// one that silently does nothing.
		reads, readErr := s.src.RecentReads(ctx, picker.RecentWindow)
		if readErr != nil {
			http.Error(w, "could not reach your library to park that series", http.StatusBadGateway)
			return
		}
		var newest time.Time
		for _, e := range reads {
			if e.FinishedAt.After(newest) {
				newest = e.FinishedAt
			}
		}
		err = s.store.Park(ctx, name, time.Now(), newest)
	case "drop":
		err = s.store.Drop(ctx, name, time.Now())
	case "pin":
		pos, _ := strconv.ParseFloat(r.FormValue("position"), 64)
		err = s.store.Pin(ctx, name, time.Now(), pos)
	case "clear":
		err = s.store.Clear(ctx, name)
	case "switch":
		to := strings.TrimSpace(r.FormValue("to"))
		// Only the series these books actually belong to are switchable;
		// anything else would invent a series the reader has never read.
		tracked, ok, getErr := s.store.Get(ctx, name)
		if getErr != nil {
			http.Error(w, "could not record that decision", http.StatusInternalServerError)
			return
		}
		if !ok || !slices.ContainsFunc(tracked.Alternatives, func(alt string) bool {
			return series.Key(alt) == series.Key(to)
		}) {
			http.Error(w, "that series is not an alternative of this one", http.StatusBadRequest)
			return
		}
		err = s.store.Switch(ctx, name, to)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
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
	// Importing is true while the one-time history import is still running,
	// during which continuations are unavailable.
	Importing bool
	// Decidable is true when the recommended book belongs to a series the
	// reader has actually read into, which is what makes park/drop/pin
	// meaningful for it. Decide names that series, which is not always the one
	// the book itself is filed under.
	Decidable bool
	Decide    series.Tracked
	Panel     panel
}

// panel is the series drawer: every tracked series, grouped by what applies to
// it. Finished sits last because it is a record of what is done, not a list of
// anything to act on.
type panel struct {
	Pinned   []series.Tracked
	Active   []series.Tracked
	Parked   []series.Tracked
	Dropped  []series.Tracked
	Finished []series.Tracked
}

// Count is how many series the drawer holds, for the toggle's label.
func (p panel) Count() int {
	return len(p.Pinned) + len(p.Active) + len(p.Parked) + len(p.Dropped) + len(p.Finished)
}

// Any reports whether there is anything worth opening the drawer for.
func (p panel) Any() bool { return p.Count() > 0 }

// group sorts tracked series into the drawer's sections. Being caught up is a
// fact about the series rather than a decision, so it only files a series under
// Finished when the reader has made no decision of their own about it.
func group(tracked []series.Tracked) panel {
	var p panel
	for _, t := range tracked {
		switch {
		case t.Decision == series.Pinned:
			p.Pinned = append(p.Pinned, t)
		case t.Decision == series.Parked:
			p.Parked = append(p.Parked, t)
		case t.Decision == series.Dropped:
			p.Dropped = append(p.Dropped, t)
		case t.CaughtUp:
			p.Finished = append(p.Finished, t)
		default:
			p.Active = append(p.Active, t)
		}
	}
	return p
}

func (s *server) handleSelect(w http.ResponseWriter, r *http.Request) {
	data := selectData{Configured: s.src != nil}
	data.Importing = s.backfill != nil && !s.backfill.Status().Done

	if s.src != nil {
		// "another" flips from the series continuation to a variety pick.
		rec, tracked, err := s.recommend(r.Context(), r.URL.Query().Has("another"))
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Rec, data.HasRec = rec.rec, rec.ok
			data.Decidable, data.Decide = rec.decidable, rec.decide
			data.Panel = group(tracked)
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

// outcome is a recommendation together with the tracked series a decision on
// the card would apply to, if any.
type outcome struct {
	rec    picker.Recommendation
	ok     bool
	decide series.Tracked
	// decidable is false when the book belongs to no series the reader has
	// read into, in which case the card offers no decisions.
	decidable bool
}

// recommend gathers the reading data, folds it into the tracked-series store,
// and produces one recommendation: continue a tracked series (initial load), or
// a variety-weighted pick (reroll, or when no series is up).
func (s *server) recommend(ctx context.Context, reroll bool) (outcome, []series.Tracked, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	toRead, err := s.src.ToRead(ctx)
	if err != nil {
		return outcome{}, nil, err
	}
	reads, err := s.src.RecentReads(ctx, picker.RecentWindow)
	if err != nil {
		return outcome{}, nil, err
	}
	reading, err := s.src.CurrentlyReading(ctx)
	if err != nil {
		return outcome{}, nil, err
	}

	tracked, err := s.syncTracked(ctx, reads, reading, toRead)
	if err != nil {
		return outcome{}, nil, err
	}

	// A dropped series is out of the running entirely, not merely skipped for
	// continuation, so its books leave the variety pool too.
	candidates := withoutDropped(toRead, tracked)

	if !reroll {
		rec, anchor, ok, err := s.continueSeries(ctx, tracked, reads, reading, toRead)
		if err != nil {
			return outcome{}, nil, err
		}
		if ok {
			// The decision applies to the series that produced the pick, which
			// is not always the series the book itself is filed under: a
			// catalogue may label the volume with an omnibus or sub-series.
			return outcome{rec: rec, ok: true, decide: anchor, decidable: true}, tracked, nil
		}
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rec, ok := picker.Pick(rng, s.prefs, candidates, reads, reading)
	decide, decidable := trackedFor(rec, tracked)
	return outcome{rec: rec, ok: ok, decide: decide, decidable: decidable}, tracked, nil
}

// syncTracked folds the current library state into the store and reads back
// every tracked series. Without a store, series tracking is simply off.
func (s *server) syncTracked(ctx context.Context, reads, reading, toRead []library.Entry) ([]series.Tracked, error) {
	if s.store == nil {
		return nil, nil
	}
	snap := series.Snapshot{Reads: reads, Reading: reading, ToRead: toRead}
	if err := s.store.Reconcile(ctx, snap); err != nil {
		return nil, err
	}
	return s.store.List(ctx)
}

// continueSeries offers the next book of the series that ranks highest, walking
// the ranking so a series with nothing left does not block the one behind it. A
// book already on the TBR is preferred; otherwise the source's resolver is
// asked, which is what surfaces a volume released since the series was finished.
//
// It stays silent until the history import has finished, since before then the
// store knows only the recent window and would rank series wrongly.
func (s *server) continueSeries(ctx context.Context, tracked []series.Tracked, reads, reading, toRead []library.Entry) (picker.Recommendation, series.Tracked, bool, error) {
	if s.store == nil || s.backfill == nil || !s.backfill.Status().Done {
		return picker.Recommendation{}, series.Tracked{}, false, nil
	}

	lookups := 0
	for _, t := range series.Order(tracked, reads, reading) {
		target := library.Series{Name: t.Name, Position: t.Position, Slug: t.Slug, Completed: t.Completed}

		// The shelf can answer even for an unplaced series: books the reader
		// put there are evidence of intent, so the earliest is next.
		if entry, ok := picker.NextOnShelves(target, toRead, s.prefs); ok {
			return picker.ContinueSeries(entry, lastRating(t.Name, reads)), t, true, nil
		}

		// A catalogue cannot: asked what follows nothing it would answer with
		// the series' first volume. A finished series can never gain a book,
		// so it is worth no lookup either.
		if s.lookahead == nil || !t.Placed() || t.Completed || lookups >= maxSeriesLookups {
			continue
		}
		lookups++
		q := library.SeriesQuery{Series: target, IncludeNovellas: s.prefs.IncludeNovellas}
		entry, found, err := s.lookahead.Next(ctx, q)
		if err != nil {
			// A throttled or unreachable backend costs the continuation, not
			// the page: fall through to a variety pick.
			log.Printf("looking up the next book in %q: %v", t.Name, err)
			return picker.Recommendation{}, series.Tracked{}, false, nil
		}
		if found {
			return picker.ContinueSeries(entry, lastRating(t.Name, reads)), t, true, nil
		}
	}
	return picker.Recommendation{}, series.Tracked{}, false, nil
}

// lastRating is how the reader rated the most recent book they finished in the
// named series, or 0 when they left it unrated.
func lastRating(name string, reads []library.Entry) float64 {
	for _, e := range reads {
		if e.Book.Series != nil && series.Key(e.Book.Series.Name) == series.Key(name) {
			return e.Rating
		}
	}
	return 0
}

// withoutDropped removes the books of dropped series from the variety pool.
func withoutDropped(toRead []library.Entry, tracked []series.Tracked) []library.Entry {
	dropped := make(map[string]bool)
	for _, t := range tracked {
		if t.Decision == series.Dropped {
			dropped[series.Key(t.Name)] = true
		}
	}
	if len(dropped) == 0 {
		return toRead
	}
	kept := make([]library.Entry, 0, len(toRead))
	for _, e := range toRead {
		if e.Book.Series != nil && dropped[series.Key(e.Book.Series.Name)] {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// trackedFor finds the tracked series a recommendation belongs to. Standing
// decisions are offered only for those: a series the reader has never started
// is refused by taking its book off the reading list instead.
func trackedFor(rec picker.Recommendation, tracked []series.Tracked) (series.Tracked, bool) {
	if rec.Entry.Book.Series == nil {
		return series.Tracked{}, false
	}
	for _, t := range tracked {
		if series.Key(t.Name) == series.Key(rec.Entry.Book.Series.Name) {
			return t, true
		}
	}
	return series.Tracked{}, false
}

func handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
