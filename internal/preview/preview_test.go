package preview

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func get(t *testing.T, s *Server, path, host string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", s.URL+path, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

func TestPreviewServesMirror(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "home")
	write("js/app.js", "export {}")
	write("page/index__q_1.html", "query page") // FilePathFor naming
	write("sub/index.html", "sub")
	write("img/pic__id_2.php", "dynamic")

	s, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.HasPrefix(s.URL, "http://127.0.0.1:") {
		t.Fatalf("must bind to loopback, got %s", s.URL)
	}

	cases := []struct{ path, body, ctype string }{
		{"/", "home", "text/html"},
		{"/js/app.js?v=7", "export {}", "text/javascript"},
		{"/page?q=1", "query page", "text/html"},
		{"/sub/", "sub", "text/html"},
		{"/sub", "sub", "text/html"},
		{"/img/pic.php?id=2", "dynamic", ""},
	}
	for _, c := range cases {
		code, ct, body := get(t, s, c.path, "")
		if code != 200 || body != c.body || !strings.HasPrefix(ct, c.ctype) {
			t.Errorf("GET %s = %d %q %q", c.path, code, ct, body)
		}
	}
	if code, _, _ := get(t, s, "/../secret", ""); code != 404 {
		t.Errorf("traversal answered %d", code)
	}
	if code, _, _ := get(t, s, "/", "evil.example"); code != 403 {
		t.Errorf("foreign Host header answered %d, want 403", code)
	}
	if got := s.PageURL(filepath.Join(dir, "sub", "index.html")); got != s.URL+"sub/index.html" {
		t.Errorf("PageURL = %s", got)
	}
}
