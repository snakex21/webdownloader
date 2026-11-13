package downloader

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
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

	if err := validateOutputPath(parsed.Path); err != nil {
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
		safeQuery := queryFilenamePart(parsed.RawQuery)
		dir, name := filepath.Split(filePath)
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if ext == "" {
			ext = ".html"
		}
		filePath = filepath.Join(dir, base+"__"+safeQuery+ext)
	}

	return safeJoin(baseDir, filePath)
}

// AssetPathFor returns the on-disk path where an asset (image, css, etc.)
// should be stored. It mirrors the path the asset has on the server side.
//
// The query string is dropped for static files (logo.png?v=3 -> logo.png, so
// cache-busting versions share one file) but kept for dynamic or
// extension-less URLs, where it selects different content:
// image.php?id=1 -> image__id_1.php, image.php?id=2 -> image__id_2.php.
func AssetPathFor(baseDir, assetURL string) (string, error) {
	parsed, err := url.Parse(assetURL)
	if err != nil {
		return "", err
	}
	if err := validateOutputPath(parsed.Path); err != nil {
		return "", err
	}
	p := strings.TrimPrefix(parsed.Path, "/")
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index"
	}
	if parsed.RawQuery != "" && isDynamicAssetPath(p) {
		dir, name := path.Split(p)
		ext := path.Ext(name)
		p = dir + strings.TrimSuffix(name, ext) + "__" + queryFilenamePart(parsed.RawQuery) + ext
	}
	return safeJoin(baseDir, p)
}

var dynamicAssetExts = map[string]struct{}{
	"": {}, ".php": {}, ".asp": {}, ".aspx": {}, ".ashx": {}, ".axd": {},
	".jsp": {}, ".cgi": {}, ".pl": {}, ".py": {}, ".do": {},
}

func isDynamicAssetPath(p string) bool {
	_, ok := dynamicAssetExts[strings.ToLower(path.Ext(p))]
	return ok
}

// maxQueryNameLen keeps query-derived filenames well under Windows'
// 255-character component limit (and helps with MAX_PATH).
const maxQueryNameLen = 64

// queryFilenamePart turns a raw query into a filename-safe fragment. Long
// queries are truncated and suffixed with a hash so they stay unique.
func queryFilenamePart(rawQuery string) string {
	q := safeFilename(rawQuery)
	if len(q) <= maxQueryNameLen {
		return q
	}
	sum := sha256.Sum256([]byte(rawQuery))
	return q[:maxQueryNameLen-13] + "_" + hex.EncodeToString(sum[:6])
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

// validateOutputPath checks decoded URL paths before filepath.Join can clean
// away traversal. Apply Windows filename rules on every OS because mirrors
// are commonly downloaded on Windows or copied there afterward.
func validateOutputPath(p string) error {
	for _, part := range strings.Split(p, "/") {
		if part == "" {
			continue
		}
		if part == "." || part == ".." || strings.TrimRight(part, " .") != part || strings.ContainsAny(part, `\:<>"|?*`) {
			return fmt.Errorf("unsafe output path component %q", part)
		}
		for _, r := range part {
			if r < 32 {
				return fmt.Errorf("unsafe output path component %q", part)
			}
		}
		name := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if name == "CON" || name == "PRN" || name == "AUX" || name == "NUL" || name == "CONIN$" || name == "CONOUT$" || (len(name) == 4 && (strings.HasPrefix(name, "COM") || strings.HasPrefix(name, "LPT")) && name[3] >= '1' && name[3] <= '9') {
			return fmt.Errorf("reserved output filename %q", part)
		}
	}
	// Reject extra leading separators rather than allowing OS-specific joins.
	if strings.HasPrefix(p, "//") || path.IsAbs(strings.TrimPrefix(p, "/")) {
		return fmt.Errorf("unsafe output path %q", p)
	}
	return nil
}

// safeJoin is a final containment check after validateOutputPath: whatever
// the URL decoded to, the joined path must stay inside baseDir. It returns the
// path in the same form as baseDir (relative stays relative) so callers such
// as writeOutputFile can compute filepath.Rel(baseDir, path).
func safeJoin(baseDir, relative string) (string, error) {
	root, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	target, err := filepath.Abs(filepath.Join(root, relative))
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("check target path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("refusing path outside download directory: %q", relative)
	}
	return filepath.Join(baseDir, relative), nil
}
