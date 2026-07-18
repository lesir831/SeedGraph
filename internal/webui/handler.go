package webui

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

type Handler struct {
	root       string
	filesystem fs.FS
	files      http.Handler
}

func New(directory string) (*Handler, error) {
	if directory == "" {
		return nil, errors.New("web directory is required")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	_ = root.Close()
	filesystem := os.DirFS(directory)
	if _, err := fs.Stat(filesystem, "index.html"); err != nil {
		return nil, errors.New("web directory does not contain index.html; run the frontend build first")
	}
	return &Handler{
		root: directory, filesystem: filesystem, files: http.FileServer(http.Dir(directory)),
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" {
		if info, err := fs.Stat(h.filesystem, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			h.files.ServeHTTP(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path.Join(h.root, "index.html"))
}
