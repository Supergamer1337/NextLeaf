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
