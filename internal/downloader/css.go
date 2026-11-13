package downloader

import (
	"net/url"
	"path/filepath"
	"strings"
)

// CSSReference is a single url() or @import reference found inside a CSS
// file. The Offset / Length fields are byte positions in the original
// stylesheet so the caller can rewrite the file in place.
type CSSReference struct {
	URL    string
	Offset int
	Length int
}

// ExtractCSSReferences walks a CSS source and returns every url(…) and
// @import reference. Quoted forms ("…", '…') are accepted; unquoted forms
// are accepted too. Byte ranges are valid for the original `source` string
// and include the leading "url(" / "@import " prefix.
func ExtractCSSReferences(source, _ string) []CSSReference {
	var refs []CSSReference
	i := 0
	for i < len(source) {
		// Skip comments to avoid matching url() inside /* … */.
		if i+1 < len(source) && source[i] == '/' && source[i+1] == '*' {
			end := strings.Index(source[i:], "*/")
			if end < 0 {
				break
			}
			i += end + 2
			continue
		}
		// url(…)
		if hasPrefixFold(source[i:], "url(") {
			start := i
			j := i + 4
			for j < len(source) && isCSSWhitespace(source[j]) {
				j++
			}
			if j >= len(source) {
				break
			}
			refStart := j
			var ref string
			var k int
			if source[j] == '"' || source[j] == '\'' {
				quote := source[j]
				j++
				k = j
				for k < len(source) && source[k] != quote {
					k++
				}
				ref = source[j:k]
				if k < len(source) {
					k++ // closing quote
				}
			} else {
				k = j
				for k < len(source) && source[k] != ')' && !isCSSWhitespace(source[k]) {
					k++
				}
				ref = source[j:k]
			}
			for k < len(source) && isCSSWhitespace(source[k]) {
				k++
			}
			if k < len(source) && source[k] == ')' {
				k++
			}
			refs = append(refs, CSSReference{URL: ref, Offset: start, Length: k - start})
			i = k
			_ = refStart
			continue
		}
		// @import …
		if hasPrefixFold(source[i:], "@import") {
			next := i + len("@import")
			if next < len(source) && !isCSSWhitespace(source[next]) {
				i++
				continue
			}
			start := i
			j := next
			for j < len(source) && isCSSWhitespace(source[j]) {
				j++
			}
			if j >= len(source) {
				break
			}
			refStart := j
			var ref string
			var k int
			if source[j] == '"' || source[j] == '\'' {
				quote := source[j]
				j++
				k = j
				for k < len(source) && source[k] != quote {
					k++
				}
				ref = source[j:k]
				if k < len(source) {
					k++
				}
			} else {
				// Unquoted form. Strip an optional url(…) wrapper so the
				// caller gets the URL itself, not "url(…)" — the @import
				// keyword is allowed to use either form.
				if hasPrefixFold(source[j:], "url(") {
					j += 4
					for j < len(source) && isCSSWhitespace(source[j]) {
						j++
					}
					// @import url("x.css") - quoted inside url().
					if j < len(source) && (source[j] == '"' || source[j] == '\'') {
						quote := source[j]
						end := strings.IndexByte(source[j+1:], quote)
						if end >= 0 {
							ref = source[j+1 : j+1+end]
							k = j + 1 + end + 1
							for k < len(source) && isCSSWhitespace(source[k]) {
								k++
							}
							if k < len(source) && source[k] == ')' {
								k++
							}
							for k < len(source) && (source[k] == ';' || isCSSWhitespace(source[k])) {
								k++
							}
							refs = append(refs, CSSReference{URL: strings.TrimSpace(ref), Offset: start, Length: k - start})
							i = k
							continue
						}
					}
				}
				k = j
				for k < len(source) && source[k] != ';' && !isCSSWhitespace(source[k]) {
					k++
				}
				// Drop a trailing ')' that came from url(...).
				if k > j && source[k-1] == ')' {
					k--
				}
				ref = source[j:k]
			}
			for k < len(source) && (source[k] == ';' || isCSSWhitespace(source[k])) {
				k++
			}
			refs = append(refs, CSSReference{URL: strings.TrimSpace(ref), Offset: start, Length: k - start})
			i = k
			_ = refStart
			continue
		}
		i++
	}
	return refs
}

func isCSSWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

// rewriteCSS downloads every url(…) / @import reference inside a CSS file,
// rewrites the bytes so the references point to the local files (keeping
// cache-busting queries), and persists the updated file. Nested stylesheets
// are handled recursively through fetchAsset. Returns the number of
// references rewritten.
func (c *crawler) rewriteCSS(cssPath, body, cssURL string) (int, error) {
	refs := ExtractCSSReferences(body, cssURL)
	if len(refs) == 0 {
		return 0, nil
	}
	var rewrites []cssRewrite
	for _, ref := range refs {
		if ref.URL == "" {
			continue
		}
		// data: URIs and fragment-only URLs are kept as-is.
		if strings.HasPrefix(strings.ToLower(ref.URL), "data:") || strings.HasPrefix(ref.URL, "#") {
			continue
		}
		assetURL, aErr := absoluteURL(cssURL, ref.URL)
		if aErr != nil {
			continue
		}
		localPath, ok := c.fetchAsset(assetURL, cssURL, false)
		if !ok {
			continue
		}
		rel, ok := localRef(filepath.Dir(cssPath), localPath, assetURL)
		if !ok {
			continue
		}
		with := "url(" + quoteCSSURL(rel) + ")"
		if hasPrefixFold(body[ref.Offset:], "@import") {
			with = "@import " + with + ";"
			// Keep whatever followed the URL (media queries) intact.
			if end := ref.Offset + ref.Length; end <= len(body) && !strings.HasSuffix(strings.TrimSpace(body[ref.Offset:end]), ";") {
				with = strings.TrimSuffix(with, ";") + " "
			}
		}
		rewrites = append(rewrites, cssRewrite{offset: ref.Offset, length: ref.Length, with: with})
	}
	if len(rewrites) == 0 {
		return 0, nil
	}
	newBody := applyRewrites(body, rewrites)
	if err := writeOutputFile(c.opts.OutputDir, cssPath, []byte(newBody)); err != nil {
		return len(rewrites), err
	}
	return len(rewrites), nil
}

// quoteCSSURL returns the CSS string literal form of a relative URL. If
// the URL contains characters that would break an unquoted url(…), it is
// wrapped in double quotes.
func quoteCSSURL(s string) string {
	if strings.ContainsAny(s, " \t\"'(),;\\") {
		esc := strings.ReplaceAll(s, "\\", "\\\\")
		esc = strings.ReplaceAll(esc, "\"", "\\\"")
		return "\"" + esc + "\""
	}
	return s
}

func applyRewrites(body string, rewrites []cssRewrite) string {
	// Right-to-left so offsets stay valid.
	for i := len(rewrites) - 1; i >= 0; i-- {
		r := rewrites[i]
		if r.offset < 0 || r.offset+r.length > len(body) {
			continue
		}
		body = body[:r.offset] + r.with + body[r.offset+r.length:]
	}
	return body
}

// ParseCSSURL is a small helper used by callers that want to resolve a
// CSS url() against a base CSS URL without downloading it.
func ParseCSSURL(ref, cssURL string) (string, error) {
	ref = strings.Trim(ref, "\"'")
	return absoluteURL(cssURL, ref)
}

// cssReferenceURL is used by browser-mode CSS fetching — it normalises a
// url() argument into a fully-qualified URL string.
func cssReferenceURL(ref, cssURL string) string {
	ref = strings.Trim(ref, "\"'")
	if u, err := url.Parse(ref); err == nil && u.IsAbs() {
		return ref
	}
	if abs, err := absoluteURL(cssURL, ref); err == nil {
		return abs
	}
	return ref
}

// cssRewrite is the package-private shape used by applyRewrites. It is
// distinct from the local `rewrite` variable inside rewriteCSSFile.
type cssRewrite struct {
	offset int
	length int
	with   string
}
