package web

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipped compresses text responses for clients that accept it. The page is
// mostly the drawer's markup, which shrinks many times over: on a phone's
// connection that is the difference between the card arriving and waiting.
func gzipped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipWriter decides at the header whether to compress: text, in full. Images
// are compressed already, and a partial or empty answer has nothing to gain.
type gzipWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	decided bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		if code == http.StatusOK && h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type")) {
			h.Set("Content-Encoding", "gzip")
			h.Del("Content-Length")
			g.zw = gzip.NewWriter(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.zw != nil {
		return g.zw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) close() {
	if g.zw != nil {
		_ = g.zw.Close()
	}
}

func compressible(contentType string) bool {
	for _, t := range []string{"text/", "application/javascript", "application/json", "application/manifest+json", "image/svg+xml"} {
		if strings.HasPrefix(contentType, t) {
			return true
		}
	}
	return false
}
