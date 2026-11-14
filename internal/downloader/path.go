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
	"unicode/utf8"
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
//
// The host is ignored; a crawl that mixes hosts uses layout.pagePath.
func FilePathFor(baseDir, rawURL string) (string, error) {
	return filePathFor(baseDir, "", rawURL)
}

// filePathFor is FilePathFor for the mirror of site: pages of other hosts
// (subdomains) are stored under _hosts/<host>/.
func filePathFor(baseDir, site, rawURL string) (string, error) {
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
		if path.Ext(filePath) == "" {
			filePath = filePath + "/index.html"
		}
	}

	if parsed.RawQuery != "" {
		safeQuery := queryFilenamePart(parsed.RawQuery)
		dir, name := path.Split(filePath)
		ext := path.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if ext == "" {
			ext = ".html"
		}
		filePath = dir + base + "__" + safeQuery + ext
	}

	return finishOutputPath(baseDir, site, parsed, filePath)
}

// AssetPathFor returns the on-disk path where an asset (image, css, etc.)
// should be stored. It mirrors the path the asset has on the server side.
//
// The query string is dropped for static files (logo.png?v=3 -> logo.png, so
// cache-busting versions share one file) but kept for dynamic or
// extension-less URLs, where it selects different content:
// image.php?id=1 -> image__id_1.php, image.php?id=2 -> image__id_2.php.
func AssetPathFor(baseDir, assetURL string) (string, error) {
	return assetPathFor(baseDir, "", assetURL)
}

// assetPathFor is AssetPathFor for the mirror of site: files of other hosts
// (CDNs, subdomains) are stored under _hosts/<host>/.
func assetPathFor(baseDir, site, assetURL string) (string, error) {
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
	return finishOutputPath(baseDir, site, parsed, p)
}

// hostsDir holds the files of hosts other than the mirrored site, one
// folder per host, so "/" of blog.example.com and "/" of example.com (or
// /lib/app.js on a CDN and on the site) do not overwrite each other.
const hostsDir = "_hosts"

// finishOutputPath prefixes files of other hosts, shortens over-long path
// segments and joins the slash-separated rel to baseDir.
func finishOutputPath(baseDir, site string, u *url.URL, rel string) (string, error) {
	prefix, err := hostPrefix(site, u)
	if err != nil {
		return "", err
	}
	if prefix != "" {
		rel = prefix + "/" + rel
	}
	return safeJoin(baseDir, filepath.FromSlash(shortenPathSegments(rel)))
}

// hostPrefix returns "" for URLs of the mirrored site (www and the bare
// domain count as one site, the port is ignored) and _hosts/<host> for any
// other host. An empty site disables the prefix (single-host layout).
func hostPrefix(site string, u *url.URL) (string, error) {
	if site == "" || u.Hostname() == "" {
		return "", nil
	}
	h := siteHost(u.Hostname())
	if h == siteHost(site) {
		return "", nil
	}
	name := strings.Map(func(r rune) rune {
		if r <= ' ' || strings.ContainsRune(`<>:"/\|?*%`, r) {
			return '_'
		}
		return r
	}, h)
	if name == "" || validateOutputPath(name) != nil {
		return "", fmt.Errorf("unsafe host folder %q", h)
	}
	return hostsDir + "/" + name, nil
}

// maxSegmentLen caps one file or folder name (in bytes, so well under
// Windows' limit of 255 UTF-16 units even for non-ASCII names).
const maxSegmentLen = 120

// shortenPathSegments shortens every over-long segment of a slash path to
// a prefix plus a hash of the whole segment (deterministic, so links to the
// file are computed the same way), keeping the extension.
func shortenPathSegments(rel string) string {
	parts := strings.Split(rel, "/")
	changed := false
	for i, seg := range parts {
		if len(seg) > maxSegmentLen {
			parts[i] = shortenSegment(seg)
			changed = true
		}
	}
	if !changed {
		return rel
	}
	return strings.Join(parts, "/")
}

func shortenSegment(seg string) string {
	ext := path.Ext(seg)
	if len(ext) > 16 || ext == seg {
		ext = ""
	}
	stem := strings.TrimSuffix(seg, ext)
	sum := sha256.Sum256([]byte(seg))
	keep := maxSegmentLen - len(ext) - 13
	for keep > 0 && !utf8.RuneStart(stem[keep]) {
		keep--
	}
	return stem[:keep] + "_" + hex.EncodeToString(sum[:6]) + ext
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

// RelativePagePath returns a URL-style (forward-slashed, percent-encoded)
// relative path from the on-disk location of fromURL to the on-disk location
// of toURL, with the hash of toURL appended if present.
func RelativePagePath(baseDir, fromURL, toURL string) (string, error) {
	l := layout{dir: baseDir}
	fromPath, err := l.pagePath(fromURL)
	if err != nil {
		return "", err
	}
	return l.pageHref(filepath.Dir(fromPath), toURL)
}

// RelativeAssetPath returns a URL-style relative path from the saved HTML page
// for fromURL to the saved asset/download file for assetURL.
func RelativeAssetPath(baseDir, fromURL, assetURL string) (string, error) {
	l := layout{dir: baseDir}
	fromPath, err := l.pagePath(fromURL)
	if err != nil {
		return "", err
	}
	return l.assetHref(filepath.Dir(fromPath), assetURL)
}

// layout maps the URLs of one mirrored site to files in dir. site is the
// start host; files of other hosts go to _hosts/<host>/ (empty site: no
// host folders).
type layout struct{ dir, site string }

func (l layout) pagePath(rawURL string) (string, error) {
	return filePathFor(l.dir, l.site, rawURL)
}

func (l layout) assetPath(rawURL string) (string, error) {
	return assetPathFor(l.dir, l.site, rawURL)
}

// pageHref is the link from a saved file in fromDir to the saved page of
// toURL (keeping its #fragment).
func (l layout) pageHref(fromDir, toURL string) (string, error) {
	toPath, err := l.pagePath(toURL)
	if err != nil {
		return "", err
	}
	rel, err := relHref(fromDir, toPath)
	if err != nil {
		return "", err
	}
	if parsed, err := url.Parse(toURL); err == nil && parsed.Fragment != "" {
		rel += "#" + parsed.EscapedFragment()
	}
	return rel, nil
}

// assetHref is the link from a saved file in fromDir to the saved file of
// assetURL.
func (l layout) assetHref(fromDir, assetURL string) (string, error) {
	assetPath, err := l.assetPath(assetURL)
	if err != nil {
		return "", err
	}
	return relHref(fromDir, assetPath)
}

// relHref returns the relative URL from dir to target: forward slashes and
// every segment percent-encoded, so a file named "a#b.html" or "100%.html"
// is not read as a fragment or a broken escape.
func relHref(dir, target string) (string, error) {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return "", err
	}
	return escapeRelPath(filepath.ToSlash(rel)), nil
}

func escapeRelPath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
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
