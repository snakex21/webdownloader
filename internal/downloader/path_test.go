package downloader

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFilePathFor(t *testing.T) {
	base := filepath.Join(t.TempDir(), "site")
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"root", "https://example.com/", filepath.Join(base, "index.html")},
		{"root-no-slash", "https://example.com", filepath.Join(base, "index.html")},
		{"path-trailing-slash", "https://example.com/blog/", filepath.Join(base, "blog", "index.html")},
		{"path-no-ext", "https://example.com/blog", filepath.Join(base, "blog", "index.html")},
		{"path-with-html", "https://example.com/blog/post.html", filepath.Join(base, "blog", "post.html")},
		{"path-with-pdf", "https://example.com/files/handout.pdf", filepath.Join(base, "files", "handout.pdf")},
		{"path-with-query-html", "https://example.com/blog?id=1", filepath.Join(base, "blog", "index__id_1.html")},
		{"path-with-query-no-ext", "https://example.com/blog/", filepath.Join(base, "blog", "index.html")},
		{"path-with-query-pdf", "https://example.com/files/handout.pdf?id=1", filepath.Join(base, "files", "handout__id_1.pdf")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FilePathFor(base, c.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestAssetPathFor(t *testing.T) {
	base := filepath.Join(t.TempDir(), "site")
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absolute", "https://example.com/img/logo.png", filepath.Join(base, "img", "logo.png")},
		{"with-query", "https://example.com/img/logo.png?v=1", filepath.Join(base, "img", "logo.png")},
		{"with-hash", "https://example.com/img/logo.png#a", filepath.Join(base, "img", "logo.png")},
		{"root", "https://example.com/", filepath.Join(base, "index")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := AssetPathFor(base, c.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRelativePagePath(t *testing.T) {
	base := filepath.Join(t.TempDir(), "site")
	cases := []struct {
		name    string
		fromURL string
		toURL   string
		want    string
	}{
		{
			"same-dir",
			"https://example.com/index.html",
			"https://example.com/about.html",
			"about.html",
		},
		{
			"sub-dir",
			"https://example.com/index.html",
			"https://example.com/blog/post.html",
			"blog/post.html",
		},
		{
			"with-hash",
			"https://example.com/index.html",
			"https://example.com/about.html#team",
			"about.html#team",
		},
		{
			"trailing-html",
			"https://example.com/",
			"https://example.com/about",
			"about/index.html",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RelativePagePath(base, c.fromURL, c.toURL)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRelativeAssetPath(t *testing.T) {
	base := filepath.Join(t.TempDir(), "site")
	got, err := RelativeAssetPath(base, "https://example.com/blog/post", "https://example.com/files/doc.pdf")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "../../files/doc.pdf" {
		t.Errorf("got %q, want %q", got, "../../files/doc.pdf")
	}
}

func TestFilePathForRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	for _, rawURL := range []string{
		"https://example.com/../outside.txt",
		"https://example.com/%2e%2e/outside.txt",
	} {
		if got, err := FilePathFor(base, rawURL); err == nil {
			t.Fatalf("FilePathFor(%q) = %q, want traversal error", rawURL, got)
		}
	}
}

func TestAssetPathForRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	for _, rawURL := range []string{
		"https://example.com/../outside.png",
		"https://example.com/%2e%2e/outside.png",
	} {
		if got, err := AssetPathFor(base, rawURL); err == nil {
			t.Fatalf("AssetPathFor(%q) = %q, want traversal error", rawURL, got)
		}
	}
}

func TestAssetPathForKeepsQueryForDynamicURLs(t *testing.T) {
	base := filepath.Join(t.TempDir(), "site")
	cases := map[string]string{
		"https://example.com/image.php?id=1":        filepath.Join(base, "image__id_1.php"),
		"https://example.com/image.php?id=2":        filepath.Join(base, "image__id_2.php"),
		"https://example.com/thumb?w=200&h=100":     filepath.Join(base, "thumb__w_200_h_100"),
		"https://example.com/img/?id=7":             filepath.Join(base, "img", "index__id_7"),
		"https://example.com/static/app.js?v=1.2.3": filepath.Join(base, "static", "app.js"),
	}
	for in, want := range cases {
		got, err := AssetPathFor(base, in)
		if err != nil || got != want {
			t.Errorf("AssetPathFor(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestLongQueriesStayShortAndUnique(t *testing.T) {
	base := t.TempDir()
	long := strings.Repeat("a", 300)
	p1, err1 := FilePathFor(base, "https://example.com/page?q="+long+"1")
	p2, err2 := FilePathFor(base, "https://example.com/page?q="+long+"2")
	a1, err3 := AssetPathFor(base, "https://example.com/img.php?q="+long)
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatal(err1, err2, err3)
	}
	if p1 == p2 {
		t.Fatal("distinct long queries collapsed to one file")
	}
	for _, p := range []string{p1, p2, a1} {
		if n := len(filepath.Base(p)); n > 100 {
			t.Errorf("file name too long (%d): %s", n, filepath.Base(p))
		}
	}
}

func TestOutputPathsRejectWindowsSpecialNames(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/con", "https://example.com/dir/LPT1.txt", "https://example.com/aux.html",
		"https://example.com/a%3Ab", "https://example.com/trailing.", "https://example.com/space%20",
		"https://example.com/a%5Cb", "https://example.com/%00x", "https://example.com/a%2F..%2F..%2Fb",
	} {
		if _, err := FilePathFor(t.TempDir(), raw); err == nil {
			t.Errorf("FilePathFor(%q) accepted", raw)
		}
		if _, err := AssetPathFor(t.TempDir(), raw); err == nil {
			t.Errorf("AssetPathFor(%q) accepted", raw)
		}
	}
}
