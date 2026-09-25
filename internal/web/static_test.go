package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticAssetsAreServed(t *testing.T) {
	tests := []struct {
		path        string
		contentType string
	}{
		{"/static/icon.svg", "image/svg+xml"},
		{"/static/icon-180.png", "image/png"},
		{"/static/icon-192.png", "image/png"},
		{"/static/icon-512.png", "image/png"},
		{"/static/manifest.webmanifest", "application/manifest+json"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := get(t, nil, tt.path)
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tt.contentType) {
				t.Errorf("Content-Type = %q, want prefix %q", got, tt.contentType)
			}
			if rec.Body.Len() == 0 {
				t.Error("body is empty")
			}
			// Vendored assets change only with a deploy; without a max-age
			// every visit revalidates each of them.
			if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
				t.Errorf("Cache-Control = %q, want a max-age so browsers cache assets", cc)
			}
		})
	}
}

func TestStaticUnknownAssetIs404(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/static/nope.svg", nil)
	rec := httptest.NewRecorder()
	NewHandler(Deps{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// The icon is only usable as a favicon if the page points browsers at it;
// they never scrape the inline mark out of the body.
func TestPageLinksTheIconAssets(t *testing.T) {
	body := get(t, nil, "/").Body.String()
	head, _, ok := strings.Cut(body, "</head>")
	if !ok {
		t.Fatal("page has no </head>")
	}
	for _, want := range []string{
		`rel="icon" type="image/svg+xml" href="/static/icon.svg"`,
		`rel="apple-touch-icon" href="/static/icon-180.png"`,
		`rel="manifest" href="/static/manifest.webmanifest"`,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("<head> is missing %s", want)
		}
	}
}

// The masthead mark and the icon file must stay one artwork, so the tab icon
// never drifts from the logo on the page.
func TestMastheadMarkIsTheIconFile(t *testing.T) {
	icon, err := staticFS.ReadFile("static/icon.svg")
	if err != nil {
		t.Fatalf("read icon: %v", err)
	}
	body := get(t, nil, "/").Body.String()
	for _, line := range strings.Split(string(icon), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "<path") {
			if !strings.Contains(body, line) {
				t.Errorf("page is missing icon path:\n%s", line)
			}
		}
	}
}

func TestAnExpiredAssetIsRevalidatedNotRefetched(t *testing.T) {
	// The assets are kept a day. Embedded files have no modification time,
	// so without a validator every day brought the whole of htmx down again.
	h := NewHandler(Deps{})
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the asset has no ETag to revalidate against")
	}
	again := httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil)
	again.Header.Set("If-None-Match", etag)
	again.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, again)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("revalidating: status %d with %d bytes, want 304 and nothing", rec.Code, rec.Body.Len())
	}

	other := httptest.NewRecorder()
	h.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/static/idiomorph-ext.min.js", nil))
	if other.Header().Get("ETag") == etag {
		t.Error("two different assets share an ETag")
	}
}
