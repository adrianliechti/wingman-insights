package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/dist
var webAssets embed.FS

type spaHandler struct {
	fs   http.Handler
	root fs.FS
}

func newSPAHandler() http.Handler {
	sub, _ := fs.Sub(webAssets, "web/dist")
	return &spaHandler{
		fs:   http.FileServerFS(sub),
		root: sub,
	}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(h.root, path); err == nil {
		h.fs.ServeHTTP(w, r)
		return
	}
	r.URL.Path = "/"
	h.fs.ServeHTTP(w, r)
}
