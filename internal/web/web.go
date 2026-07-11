// Package web contains Nextleaf's HTTP server: routing, handlers, and templates.
package web

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
)

const appName = "Nextleaf"

//go:embed home.html
var templateFS embed.FS

var homeTmpl = template.Must(template.ParseFS(templateFS, "home.html"))

type homeData struct {
	Title   string
	Heading string
	Message string
}

// NewHandler returns the application's HTTP handler.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	// {$} matches "/" exactly, so unknown paths fall through to 404 instead of
	// being swallowed by a catch-all root pattern.
	mux.HandleFunc("GET /{$}", handleHome)
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

func handleHealthcheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
