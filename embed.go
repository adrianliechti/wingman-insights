package main

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/dist
var webAssets embed.FS

type spaHandler struct {
	fs        http.Handler
	root      fs.FS
	indexHTML []byte
}

// newSPAHandler serves the embedded Vue SPA. basePath is the public mount
// point (e.g. "/insights", or "" for root); it is injected as a <base href>
// into index.html so the SPA's relative asset and API URLs resolve correctly
// regardless of how deep the current route is.
func newSPAHandler(basePath string) http.Handler {
	sub, _ := fs.Sub(webAssets, "web/dist")

	index, _ := fs.ReadFile(sub, "index.html")
	baseTag := []byte("<head>\n    <base href=\"" + basePath + "/\" />")
	index = bytes.Replace(index, []byte("<head>"), baseTag, 1)

	return &spaHandler{
		fs:        http.FileServerFS(sub),
		root:      sub,
		indexHTML: index,
	}
}

func (h *spaHandler) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(h.indexHTML)
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || path == "index.html" {
		h.serveIndex(w)
		return
	}
	if _, err := fs.Stat(h.root, path); err == nil {
		h.fs.ServeHTTP(w, r)
		return
	}
	h.serveIndex(w)
}
