// Package preview serves a downloaded site from disk on 127.0.0.1, so pages
// that use ES modules, fetch() or import maps (which browsers block on
// file://) work offline.
package preview

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/asrock/webdownloader/internal/downloader"
)

// Server is a running preview of one download folder.
type Server struct {
	// URL is the server root, e.g. http://127.0.0.1:53817/.
	URL  string
	Root string

	root   *os.Root
	server *http.Server
	host   string
}

// contentTypes are set explicitly: on Windows the registry may map .js to
// text/plain, which browsers refuse for module scripts.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".wasm":  "application/wasm",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
	".xml":   "application/xml",
	".mp3":   "audio/mpeg",
	".ogg":   "audio/ogg",
	".wav":   "audio/wav",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
}

// Start serves dir on a random free port bound to 127.0.0.1 only.
func Start(dir string) (*Server, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("not a folder: %s", abs)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		root.Close()
		return nil, err
	}
	s := &Server{
		URL:  "http://" + ln.Addr().String() + "/",
		Root: abs,
		root: root,
		host: ln.Addr().String(),
	}
	s.server = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.server.Serve(ln) }()
	return s, nil
}

// PageURL returns the preview URL of a file inside the served folder.
func (s *Server) PageURL(file string) string {
	if file == "" {
		return s.URL
	}
	rel, err := filepath.Rel(s.Root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return s.URL
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return s.URL + strings.Join(parts, "/")
}

// ServeHTTP serves files from the mirror. Requests with a query string are
// first mapped the way the downloader names such files (page/index__q_1.html,
// image__id_1.php); otherwise the query is ignored, so "app.js?v=7" serves
// app.js.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only answer requests addressed to this exact origin: blocks DNS
	// rebinding pages from reading the folder through another host name.
	if r.Host != s.host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	name := s.resolve(r.URL)
	if name == "" {
		http.NotFound(w, r)
		return
	}
	f, err := s.root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// resolve maps a request URL to a regular file inside the root ("" if none).
func (s *Server) resolve(u *url.URL) string {
	clean := path.Clean("/" + u.Path)
	var candidates []string
	if u.RawQuery != "" {
		full := "http://preview" + clean + "?" + u.RawQuery
		if p, err := downloader.FilePathFor("", full); err == nil {
			candidates = append(candidates, p)
		}
		if p, err := downloader.AssetPathFor("", full); err == nil {
			candidates = append(candidates, p)
		}
	}
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" {
		candidates = append(candidates, "index.html")
	} else {
		candidates = append(candidates, rel, path.Join(rel, "index.html"))
		if path.Ext(rel) == "" {
			candidates = append(candidates, rel+".html")
		}
	}
	for _, c := range candidates {
		c = filepath.ToSlash(c)
		if !fs.ValidPath(c) {
			continue
		}
		if info, err := s.root.Stat(c); err == nil && info.Mode().IsRegular() {
			return c
		}
	}
	return ""
}

// Close stops the server.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.server.Shutdown(ctx)
	_ = s.root.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
