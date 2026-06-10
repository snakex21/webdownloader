package downloader

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// FilePathFor returns the on-disk path where a downloaded URL should be stored
// inside baseDir. The mapping mirrors the original Node implementation:
//
//   - /            -> index.html
//   - /page/       -> page/index.html
//   - /page        -> page/index.html
//   - /file.pdf    -> file.pdf
//   - /page?q=1    -> page__q=1.html
//   - /file.pdf?q=1 -> file.pdf__q=1  (extension preserved)
func FilePathFor(baseDir, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	filePath := strings.TrimPrefix(parsed.Path, "/")
	if filePath == "" || strings.HasSuffix(filePath, "/") {
		filePath = filePath + "index.html"
	} else if !strings.HasSuffix(filePath, ".html") && !strings.HasSuffix(filePath, ".htm") {
		if filepath.Ext(filePath) == "" {
			filePath = filePath + "/index.html"
		}
	}

	if parsed.RawQuery != "" {
		safeQuery := safeFilename(parsed.RawQuery)
		dir, name := filepath.Split(filePath)
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if ext == "" {
			ext = ".html"
		}
		filePath = filepath.Join(dir, base+"__"+safeQuery+ext)
	}

	return filepath.Join(baseDir, filePath), nil
}

// AssetPathFor returns the on-disk path where an asset (image, css, etc.)
// should be stored. It mirrors the path the asset has on the server side.
func AssetPathFor(baseDir, assetURL string) (string, error) {
	parsed, err := url.Parse(assetURL)
	if err != nil {
		return "", err
	}
	p := strings.TrimPrefix(parsed.Path, "/")
	p = strings.SplitN(p, "?", 2)[0]
	p = strings.SplitN(p, "#", 2)[0]
	if p == "" {
		p = "index"
	}
	return filepath.Join(baseDir, p), nil
}

// RelativePagePath returns a URL-style (forward-slashed) relative path from
// the on-disk location of fromURL to the on-disk location of toURL, with the
// hash of toURL appended if present.
func RelativePagePath(baseDir, fromURL, toURL string) (string, error) {
	fromPath, err := FilePathFor(baseDir, fromURL)
	if err != nil {
		return "", err
	}
	toPath, err := FilePathFor(baseDir, toURL)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(filepath.Dir(fromPath), toPath)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "" {
		rel = "./index.html"
	}
	if parsed, err := url.Parse(toURL); err == nil && parsed.Fragment != "" {
		rel += "#" + parsed.Fragment
	}
	return rel, nil
}

// RelativeAssetPath returns a URL-style relative path from the saved HTML page
// for fromURL to the saved asset/download file for assetURL.
func RelativeAssetPath(baseDir, fromURL, assetURL string) (string, error) {
	fromPath, err := FilePathFor(baseDir, fromURL)
	if err != nil {
		return "", err
	}
	assetPath, err := AssetPathFor(baseDir, assetURL)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(filepath.Dir(fromPath), assetPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeFilename(s string) string {
	return unsafeChars.ReplaceAllString(s, "_")
}
