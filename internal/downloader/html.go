package downloader

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// AssetRef describes a single downloadable asset reference inside an HTML
// document.
type AssetRef struct {
	Tag  string // e.g. "img", "link", "script", "source", "video"
	Attr string // e.g. "src", "href", "poster", "srcset"
	URL  string // the original (non-absolute) URL value (first URL if srcset)
}

// RewriteAssets parses the given HTML, returns a *goquery.Document ready to
// be re-serialized, and a deduplicated list of relative asset references
// that the caller should download.
//
// References may be relative, same-origin absolute, protocol-relative, or
// external absolute URLs. The caller resolves them against baseURL and applies
// scope rules (same domain / subdomains / external CDN assets).
func RewriteAssets(html string) (*goquery.Document, []AssetRef, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, nil, err
	}

	seen := make(map[string]int) // key -> index in `out`
	var out []AssetRef

	collect := func(sel *goquery.Selection, tag, attr string) {
		s, exists := sel.Attr(attr)
		if !exists {
			return
		}
		// srcset is a comma-separated list of URL [descriptor] entries;
		// pick the first URL only.
		if attr == "srcset" {
			first, _ := firstSrcsetURL(s)
			if first == "" {
				return
			}
			s = first
		}
		if !isDownloadableAssetRef(s) {
			return
		}
		key := tag + "|" + attr + "|" + s
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = len(out)
		out = append(out, AssetRef{Tag: tag, Attr: attr, URL: s})
	}

	// Standard asset references.
	doc.Find("img[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "img", "src") })
	doc.Find("img[srcset]").Each(func(_ int, s *goquery.Selection) { collect(s, "img", "srcset") })
	doc.Find("img[data-src]").Each(func(_ int, s *goquery.Selection) { collect(s, "img", "data-src") })

	doc.Find("link[href]").Each(func(_ int, s *goquery.Selection) {
		h, ok := s.Attr("href")
		if !ok {
			return
		}
		lh := strings.ToLower(h)
		rel, _ := s.Attr("rel")
		rel = strings.ToLower(rel)
		// Stylesheets, preloads, prefetch, modulepreload, icons, manifests…
		if strings.HasSuffix(lh, ".css") {
			collect(s, "link", "href")
			return
		}
		switch rel {
		case "preload", "prefetch", "modulepreload", "icon", "shortcut icon",
			"apple-touch-icon", "manifest", "stylesheet":
			collect(s, "link", "href")
		}
	})

	doc.Find("script[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "script", "src") })
	doc.Find("script[data-src]").Each(func(_ int, s *goquery.Selection) { collect(s, "script", "data-src") })

	// <source> may appear inside <picture>, <video>, <audio>.
	doc.Find("source[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "source", "src") })
	doc.Find("source[srcset]").Each(func(_ int, s *goquery.Selection) { collect(s, "source", "srcset") })
	doc.Find("source[data-src]").Each(func(_ int, s *goquery.Selection) { collect(s, "source", "data-src") })

	doc.Find("video[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "video", "src") })
	doc.Find("video[poster]").Each(func(_ int, s *goquery.Selection) { collect(s, "video", "poster") })
	doc.Find("audio[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "audio", "src") })
	doc.Find("iframe[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "iframe", "src") })
	doc.Find("iframe[src]").Each(func(_ int, s *goquery.Selection) {
		// Scope filtering happens later in downloadPage, not here.
	})
	doc.Find("embed[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "embed", "src") })
	doc.Find("object[data]").Each(func(_ int, s *goquery.Selection) { collect(s, "object", "data") })
	doc.Find("track[src]").Each(func(_ int, s *goquery.Selection) { collect(s, "track", "src") })

	return doc, out, nil
}

// RewriteAssetURL rewrites every matching element in doc so that its attr
// points to the new (relative) value. Only elements whose current attr
// equals the original are touched.
func RewriteAssetURL(doc *goquery.Document, tag, attr, original, newValue string) {
	doc.Find(tag).Each(func(_ int, s *goquery.Selection) {
		v, ok := s.Attr(attr)
		if !ok {
			return
		}
		// For srcset, rewrite every comma-separated entry that matches
		// the original URL.
		if attr == "srcset" {
			newSet := rewriteSrcset(v, original, newValue)
			s.SetAttr(attr, newSet)
			return
		}
		if v == original {
			s.SetAttr(attr, newValue)
		}
	})
}

// RewriteLink rewrites every navigation href in doc whose href equals oldHref
// to newHref. Used to point inter-page links at locally-saved HTML files.
func RewriteLink(doc *goquery.Document, oldHref, newHref string) {
	doc.Find("a[href], area[href]").Each(func(_ int, s *goquery.Selection) {
		if v, ok := s.Attr("href"); ok && v == oldHref {
			s.SetAttr("href", newHref)
		}
	})
}

// RewriteLocalNavigation rewrites clickable page/file links to paths in the
// local mirror and returns URLs that should be followed/downloaded. It handles
// <a href> and <area href>, skips pseudo links, keeps pure hash anchors intact,
// and prevents <base href> from forcing local relative links back online.
func RewriteLocalNavigation(doc *goquery.Document, pageURL, baseDir, domain string, opts Options) (links, attachments []string) {
	// A remote <base href="https://site/"> would make every relative link in the
	// saved HTML resolve against the internet, not the local folder.
	doc.Find("base[href]").Each(func(_ int, s *goquery.Selection) {
		s.RemoveAttr("href")
	})

	seenLinks := make(map[string]struct{})
	seenAttach := make(map[string]struct{})

	doc.Find("a[href], area[href]").Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok || shouldSkipNavigationHref(href) {
			return
		}

		full, absErr := absoluteURL(pageURL, href)
		if absErr != nil {
			return
		}
		linkParsed, parseErr := url.Parse(full)
		if parseErr != nil {
			return
		}
		if isTechnicalCrawlerURL(linkParsed) {
			return
		}
		if !shouldFollowPage(full, domain, opts) {
			return
		}

		ext := strings.ToLower(filepath.Ext(linkParsed.Path))
		hasSearch := linkParsed.RawQuery != ""
		isHTMLish := ext == "" || ext == ".html" || ext == ".htm"

		if opts.DownloadAll {
			if _, isAttach := fileExtensions[ext]; isAttach {
				if rel, relErr := RelativeAssetPath(baseDir, pageURL, full); relErr == nil {
					s.SetAttr("href", rel)
				}
				if _, dup := seenAttach[full]; !dup {
					seenAttach[full] = struct{}{}
					attachments = append(attachments, full)
				}
				return
			}
		}

		if isHTMLish || hasSearch {
			if rel, relErr := RelativePagePath(baseDir, pageURL, full); relErr == nil {
				s.SetAttr("href", rel)
			}
			if _, dup := seenLinks[full]; !dup {
				seenLinks[full] = struct{}{}
				links = append(links, full)
			}
		}
	})

	return links, attachments
}

func shouldSkipNavigationHref(href string) bool {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return true
	}
	lower := strings.ToLower(href)
	return strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "tel:") ||
		strings.HasPrefix(lower, "javascript:") ||
		strings.HasPrefix(lower, "data:")
}

// CollectLinks scans the document for <a href> and <area href> links and
// returns their href values, resolved against baseURL. The caller decides
// which ones to follow (same host, no mailto/tel/javascript, html-like
// extension or query string).
func CollectLinks(doc *goquery.Document, baseURL string) []string {
	var out []string
	doc.Find("a[href], area[href]").Each(func(_ int, s *goquery.Selection) {
		if h, ok := s.Attr("href"); ok {
			out = append(out, h)
		}
	})
	return out
}

// RewriteMetaRefresh rewrites the URL in <meta http-equiv="refresh"> so
// the saved page navigates locally if the user clicks through.
func RewriteMetaRefresh(doc *goquery.Document, pageURL, baseDir string) {
	doc.Find("meta[http-equiv]").Each(func(_ int, s *goquery.Selection) {
		he, _ := s.Attr("http-equiv")
		if !strings.EqualFold(he, "refresh") {
			return
		}
		content, ok := s.Attr("content")
		if !ok {
			return
		}
		newContent := rewriteMetaRefreshContent(content, pageURL, baseDir)
		if newContent != content {
			s.SetAttr("content", newContent)
		}
	})
}

var (
	metaRefreshRE = regexp.MustCompile(`(?i)^\s*(\d+)\s*;\s*url\s*=\s*(.+?)\s*$`)
)

// rewriteMetaRefreshContent parses a "N; url=…" value, resolves the URL
// against pageURL, and returns the rewritten content string. If the URL
// can't be rewritten (e.g. off-domain and the user disabled external
// assets, or just unparseable) the original is returned unchanged.
func rewriteMetaRefreshContent(content, pageURL, baseDir string) string {
	m := metaRefreshRE.FindStringSubmatch(content)
	if m == nil {
		return content
	}
	delay := m[1]
	target := strings.Trim(m[2], "'\"")

	abs, err := absoluteURL(pageURL, target)
	if err != nil {
		return content
	}
	rel, err := RelativePagePath(baseDir, pageURL, abs)
	if err != nil {
		return content
	}
	return delay + "; url=" + rel
}

// rewriteSrcset replaces every occurrence of original with newValue in a
// srcset attribute. Original/newValue are single URL strings (no
// descriptors). The descriptor (width / pixel density) is preserved.
func rewriteSrcset(value, original, newValue string) string {
	entries := strings.Split(value, ",")
	for i, e := range entries {
		parts := strings.Fields(strings.TrimSpace(e))
		if len(parts) == 0 {
			continue
		}
		if parts[0] == original {
			parts[0] = newValue
			entries[i] = strings.Join(parts, " ")
		}
	}
	return strings.Join(entries, ",")
}

// firstSrcsetURL returns the URL part of the first entry in a srcset
// attribute ("url 2x", "url 600w 300h", …) along with the descriptor, if
// any. Returns "" if the value is empty.
func firstSrcsetURL(value string) (string, []string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	idx := strings.Index(value, ",")
	var first string
	if idx < 0 {
		first = value
	} else {
		first = value[:idx]
	}
	parts := strings.Fields(strings.TrimSpace(first))
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

// isDownloadableAssetRef filters out non-downloadable pseudo URLs. It does NOT
// reject http(s) or protocol-relative URLs; those are needed for CDN assets and
// are scoped later by shouldDownloadAsset.
func isDownloadableAssetRef(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "data:") {
		return false
	}
	if strings.HasPrefix(lower, "mailto:") || strings.HasPrefix(lower, "tel:") || strings.HasPrefix(lower, "javascript:") {
		return false
	}
	if strings.HasPrefix(s, "#") {
		return false
	}
	return true
}

// parseSrcset is a public helper used by the JS-side progress UI when it
// wants to show the highest-resolution variant. Kept around for
// completeness — currently unused by the downloader core.
func parseSrcset(value string) []srcsetCandidate {
	entries := strings.Split(value, ",")
	var out []srcsetCandidate
	for _, e := range entries {
		parts := strings.Fields(strings.TrimSpace(e))
		if len(parts) == 0 {
			continue
		}
		c := srcsetCandidate{URL: parts[0]}
		for _, p := range parts[1:] {
			if strings.HasSuffix(p, "w") {
				if n, err := strconv.Atoi(strings.TrimSuffix(p, "w")); err == nil {
					c.Width = n
				}
			} else if strings.HasSuffix(p, "x") {
				if f, err := strconv.ParseFloat(strings.TrimSuffix(p, "x"), 64); err == nil {
					c.Density = f
				}
			}
		}
		out = append(out, c)
	}
	return out
}

type srcsetCandidate struct {
	URL     string
	Width   int
	Density float64
}

// extFromURL is a small helper used by some refactorings. Kept to avoid
// importing the same logic twice from two places.
func extFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(filepath.Ext(parsed.Path))
}
