package web

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// compressed gzips text responses for clients that accept it. Images are
// compressed already, and are passed through. Every text response says it
// varies by encoding, compressed or not, so a shared cache keeps both.
func compressed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw := &gzipWriter{ResponseWriter: w,
			// A range counts bytes as stored, and a HEAD has no body.
			gzip: acceptsGzip(r.Header.Get("Accept-Encoding")) && r.Header.Get("Range") == "" && r.Method != http.MethodHead,
		}
		defer gw.close()
		h.ServeHTTP(gw, r)
	})
}

// acceptsGzip reads an Accept-Encoding header, where "gzip;q=0" is a refusal.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		name, value, _ := strings.Cut(params, "=")
		if strings.TrimSpace(name) != "q" {
			return true
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return err != nil || q > 0
	}
	return false
}

// gzipWriter decides at the first write whether the response is worth
// compressing, from its type, and compresses the rest if so.
type gzipWriter struct {
	http.ResponseWriter
	gzip    bool // the client takes gzip, and this response can have it
	zw      *gzip.Writer
	decided bool
}

func (g *gzipWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	switch {
	case status == http.StatusNotModified:
		// It stands for a body that varied; the file server leaves its type out.
		h.Set("Vary", "Accept-Encoding")
		return
	case status == http.StatusNoContent || h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")):
		return
	}
	h.Set("Vary", "Accept-Encoding")
	if !g.gzip {
		return
	}
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	h.Set("Content-Encoding", "gzip")
	g.zw = gzipWriters.Get().(*gzip.Writer)
	g.zw.Reset(g.ResponseWriter)
}

func (g *gzipWriter) WriteHeader(status int) {
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(p))
		}
		g.decide(http.StatusOK)
	}
	if g.zw != nil {
		return g.zw.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	if g.zw == nil {
		return
	}
	_ = g.zw.Close()
	g.zw.Reset(nil)
	gzipWriters.Put(g.zw)
}

func compressible(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.TrimSpace(mediaType)
	return strings.HasPrefix(mediaType, "text/") ||
		strings.HasSuffix(mediaType, "javascript") ||
		strings.HasSuffix(mediaType, "json") ||
		mediaType == "image/svg+xml"
}
