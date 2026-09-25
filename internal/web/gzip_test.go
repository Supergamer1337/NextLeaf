package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func fetch(h http.Handler, method, target, acceptEncoding string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("reading gzip body: %v", err)
	}
	return string(b)
}

func TestTextIsCompressedForABrowserThatAcceptsIt(t *testing.T) {
	// The page, htmx and the view are text that compresses to a quarter of
	// its size or less; on a phone's connection that is most of the load.
	h := ready(t, midSeries(), testStore(t))
	for _, path := range []string{"/", "/view", "/static/htmx.min.js"} {
		plain := fetch(h, http.MethodGet, path, "", nil)
		zipped := fetch(h, http.MethodGet, path, "gzip, deflate, br", nil)
		if got := zipped.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("%s: Content-Encoding = %q, want gzip", path, got)
			continue
		}
		if !strings.Contains(zipped.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("%s: no Vary on Accept-Encoding, so a cache could serve gzip to a client without it", path)
		}
		if zipped.Header().Get("Content-Length") != "" || zipped.Header().Get("Accept-Ranges") != "" {
			t.Errorf("%s: the length or ranges of the uncompressed body were kept on the compressed one", path)
		}
		if path == "/view" {
			continue // a fresh pick each time; its size is what matters here
		}
		if got := gunzip(t, zipped); got != plain.Body.String() {
			t.Errorf("%s: the compressed body is not the plain one", path)
		}
		if zipped.Body.Len()*2 > plain.Body.Len() {
			t.Errorf("%s: %d bytes compressed from %d, want well under half", path, zipped.Body.Len(), plain.Body.Len())
		}
	}
}

func TestNothingIsCompressedUnlessAskedFor(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	for _, enc := range []string{"", "br", "gzip;q=0"} {
		rec := fetch(h, http.MethodGet, "/", enc, nil)
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Accept-Encoding %q: Content-Encoding = %q, want none", enc, got)
		}
		if !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
			t.Errorf("Accept-Encoding %q: the page did not come back as plain text", enc)
		}
	}
}

func TestImagesAndEmptyAnswersAreLeftAlone(t *testing.T) {
	// A JPEG is compressed already, and an empty answer has nothing to
	// compress: gzip would only add bytes and a header htmx must see past.
	h := ready(t, &coverStub{}, testStore(t))
	cover := fetch(h, http.MethodGet, "/cover/grimmory/7", "gzip", nil)
	if cover.Header().Get("Content-Encoding") != "" || cover.Body.String() != "jpeg-bytes" {
		t.Errorf("the cover was compressed: encoding %q", cover.Header().Get("Content-Encoding"))
	}

	h = ready(t, midSeries(), testStore(t))
	empty := fetch(h, http.MethodGet, "/view?after=bad", "gzip", nil)
	if empty.Code != http.StatusNoContent || empty.Header().Get("Content-Encoding") != "" || empty.Body.Len() != 0 {
		t.Errorf("a 204 came back with encoding %q and %d bytes, want neither", empty.Header().Get("Content-Encoding"), empty.Body.Len())
	}
}

func TestACompressedRefusalStillSaysWhereItGoes(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	rec := fetch(h, http.MethodPost, "/series/nonsense", "gzip", url.Values{"name": {"Mistborn"}})
	if rec.Header().Get("HX-Retarget") != "#flash" {
		t.Error("compressing the refusal lost the header steering it to the notice slot")
	}
	if got := gunzip(t, rec); !strings.Contains(got, "notice--error") {
		t.Errorf("the refusal's body did not survive compression:\n%s", got)
	}
}

func TestWhatCountsAsAcceptingGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip":                  true,
		"GZIP":                  true,
		"gzip, deflate, br":     true,
		"br;q=1.0, gzip;q=0.8":  true,
		"gzip;q=nonsense":       true, // a weight that cannot be read refuses nothing
		"gzip;q=0":              false,
		"gzip; q=0.000":         false,
		"deflate, gzip ;q=0.0 ": false,
		"br":                    false,
		"":                      false,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestARangeIsServedAsTheBytesAsked(t *testing.T) {
	// A range counts bytes of the file as stored; compressing it would hand
	// back a slice of something else.
	req := httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-99")
	rec := httptest.NewRecorder()
	NewHandler(Deps{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 100 {
		t.Errorf("range: status %d, encoding %q, %d bytes; want 206, none, 100", rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}
}

func TestCompressionLeavesAnEncodedBodyAloneAndSniffsAnUntypedOne(t *testing.T) {
	encoded := compressed(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, "already brotli")
	}))
	rec := fetch(encoded, http.MethodGet, "/", "gzip, br", nil)
	if rec.Header().Get("Content-Encoding") != "br" || rec.Body.String() != "already brotli" {
		t.Errorf("an encoded body was encoded again: %q, %q", rec.Header().Get("Content-Encoding"), rec.Body.String())
	}

	untyped := compressed(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<!DOCTYPE html><p>"+strings.Repeat("words ", 100))
	}))
	rec = fetch(untyped, http.MethodGet, "/", "gzip", nil)
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(gunzip(t, rec), "<!DOCTYPE html>") {
		t.Error("an untyped HTML body was not recognised as text and compressed")
	}
}

func TestEveryTextAnswerSaysItVariesByEncoding(t *testing.T) {
	// A shared cache keys what it stores by the headers named in Vary. An
	// uncompressed answer without it, cached first, would be served to every
	// browser after, gzip or not; a revalidation must say the same.
	h := ready(t, midSeries(), testStore(t))
	for _, c := range []struct{ path, enc string }{
		{"/", ""},
		{"/static/htmx.min.js", ""},
		{"/static/htmx.min.js", "gzip"},
	} {
		rec := fetch(h, http.MethodGet, c.path, c.enc, nil)
		if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
			t.Errorf("%s with Accept-Encoding %q: Vary = %q, want Accept-Encoding once", c.path, c.enc, got)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil)
	req.Header.Set("If-None-Match", staticETags["static/htmx.min.js"])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("revalidation: status %d, Vary %q; want 304 varying by encoding", rec.Code, rec.Header().Get("Vary"))
	}

	cover := fetch(ready(t, &coverStub{}, testStore(t)), http.MethodGet, "/cover/grimmory/7", "", nil)
	if cover.Header().Get("Vary") != "" {
		t.Error("an image, never compressed, says it varies by encoding")
	}
}

func TestAHeadRequestSaysNothingItWouldNotSend(t *testing.T) {
	// A HEAD has no body to compress; closing a gzip stream anyway wrote its
	// empty trailer's length into the headers.
	req := httptest.NewRequest(http.MethodHead, "/static/htmx.min.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	NewHandler(Deps{}).ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 0 {
		t.Errorf("HEAD: encoding %q with %d bytes, want neither", rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}
}
