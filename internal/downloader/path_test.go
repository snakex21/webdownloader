package downloader

import (
	"path/filepath"
	"testing"
)

func TestFilePathFor(t *testing.T) {
	base := "/tmp/site"
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
	base := "/tmp/site"
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
	base := "/tmp/site"
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
	base := "/tmp/site"
	got, err := RelativeAssetPath(base, "https://example.com/blog/post", "https://example.com/files/doc.pdf")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "../../files/doc.pdf" {
		t.Errorf("got %q, want %q", got, "../../files/doc.pdf")
	}
}
