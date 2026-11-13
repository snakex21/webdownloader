package downloader

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// JavaScript and JSON reference discovery.
//
// The fast mode never runs scripts, so it reads them instead. The scanner is
// deliberately conservative and only uses string literals, never evaluated
// expressions:
//
//   - module references, resolved against the script's own URL:
//     import … from '…', import '…', export … from '…', import('…') and
//     new URL('…', import.meta.url);
//   - document-relative references, resolved against the page that loads the
//     script: fetch('…'), new Worker('…') and any other string literal that
//     looks like a relative path to a known file type ('data/games.json',
//     "wojna_er/index.html", …). Data files such as games-data.js often list
//     pages only this way.
//
// Known limits: URLs built at run time ('themes/' + name + '.css'), template
// literals with ${…}, bare module specifiers (resolved through the import
// map instead) and paths without a file extension are not discovered. Every
// literal-derived URL is speculative: a miss (404) is skipped silently.

// JSRefKind tells how a discovered reference must be resolved.
type JSRefKind int

const (
	// JSRefModule is resolved against the script URL (import, export from,
	// dynamic import(), new URL(…, import.meta.url)).
	JSRefModule JSRefKind = iota + 1
	// JSRefDocument is resolved against the page URL (fetch, Worker and
	// path-like string literals).
	JSRefDocument
)

// JSRef is one URL-like reference found in a script.
type JSRef struct {
	URL  string
	Kind JSRefKind
}

var (
	jsStringRE = `("(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|` + "`[^`$\\\\]*`" + `)`
	// import x from '…' / import {a} from "…" / export * from '…'
	jsFromRE = regexp.MustCompile(`(?:^|[;\s})])(?:import|export)\s[^;'"` + "`" + `]*?\bfrom\s*` + jsStringRE)
	// import '…' (side-effect import)
	jsBareImportRE = regexp.MustCompile(`(?:^|[;\s})])import\s*` + jsStringRE)
	// import('…')
	jsDynImportRE = regexp.MustCompile(`\bimport\s*\(\s*` + jsStringRE + `\s*[,)]`)
	// new URL('…', import.meta.url)
	jsMetaURLRE = regexp.MustCompile(`\bnew\s+URL\s*\(\s*` + jsStringRE + `\s*,\s*import\.meta\.url\s*\)`)
	// any string literal
	jsAnyStringRE = regexp.MustCompile(jsStringRE)
)

// literalExtensions are the file types a path-like literal must end with.
var literalExtensions = map[string]struct{}{
	".html": {}, ".htm": {}, ".json": {}, ".js": {}, ".mjs": {}, ".css": {},
	".svg": {}, ".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {},
	".avif": {}, ".ico": {}, ".bmp": {}, ".mp3": {}, ".ogg": {}, ".wav": {},
	".m4a": {}, ".mp4": {}, ".webm": {}, ".woff": {}, ".woff2": {}, ".ttf": {},
	".otf": {}, ".txt": {}, ".xml": {}, ".csv": {}, ".wasm": {}, ".glb": {},
	".gltf": {}, ".pdf": {},
}

// ScanJS returns the references found in a script, deduplicated, in
// source order.
func ScanJS(src string) []JSRef {
	var out []JSRef
	seen := map[string]struct{}{}
	add := func(raw string, kind JSRefKind) {
		v, ok := unquoteJSString(raw)
		if !ok || v == "" {
			return
		}
		if kind == JSRefModule && !isRelativeOrAbsoluteSpecifier(v) {
			return // bare specifier, mapped by the import map
		}
		if kind == JSRefDocument && !looksLikeFilePath(v) {
			return
		}
		key := v + "|" + string(rune('0'+kind))
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, JSRef{URL: v, Kind: kind})
	}
	moduleStrings := map[string]struct{}{}
	for _, re := range []*regexp.Regexp{jsFromRE, jsBareImportRE, jsDynImportRE, jsMetaURLRE} {
		for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
			raw := src[m[2]:m[3]]
			moduleStrings[raw] = struct{}{}
			add(raw, JSRefModule)
		}
	}
	for _, m := range jsAnyStringRE.FindAllStringIndex(src, -1) {
		raw := src[m[0]:m[1]]
		if _, isModule := moduleStrings[raw]; isModule {
			continue
		}
		add(raw, JSRefDocument)
	}
	return out
}

// ScanJSON returns path-like string values of a JSON document.
func ScanJSON(data []byte) []string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			if looksLikeFilePath(t) {
				if _, dup := seen[t]; !dup {
					seen[t] = struct{}{}
					out = append(out, t)
				}
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func unquoteJSString(raw string) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	body := raw[1 : len(raw)-1]
	if !strings.Contains(body, `\`) {
		return body, true
	}
	// Only simple escapes; anything fancier is not a plain path.
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(body) {
			return "", false
		}
		switch body[i] {
		case '/', '\\', '\'', '"', '`':
			b.WriteByte(body[i])
		default:
			return "", false
		}
	}
	return b.String(), true
}

func isRelativeOrAbsoluteSpecifier(v string) bool {
	return strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") ||
		strings.HasPrefix(v, "/") || strings.HasPrefix(v, "http://") ||
		strings.HasPrefix(v, "https://")
}

// looksLikeFilePath accepts relative or same-site paths to a known file type,
// e.g. "data/games.json", "./img/a.png?v=2", "gra zapamietywanie/gra.html".
func looksLikeFilePath(v string) bool {
	if len(v) < 4 || len(v) > 300 {
		return false
	}
	if v[0] == ' ' || v[len(v)-1] == ' ' || strings.ContainsAny(v, "<>{}|\\^\"'`\n\r\t*$") {
		return false
	}
	// "Popover requires tooltip.js" is a message, not a path: a literal with
	// spaces counts only when it also has a directory part.
	if strings.Contains(v, " ") && !strings.Contains(v, "/") {
		return false
	}
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "//") || strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "blob:") || strings.HasPrefix(lower, "javascript:") ||
		strings.HasPrefix(lower, "mailto:") {
		return false
	}
	if strings.Contains(lower, "://") && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	p := lower
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	ext := path.Ext(p)
	if _, ok := literalExtensions[ext]; !ok {
		return false
	}
	return strings.TrimSuffix(path.Base(p), ext) != ""
}

// isHTMLPath reports whether a URL path names an HTML page.
func isHTMLPath(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".html" || ext == ".htm"
}

// ImportMapTargets returns every module URL an import map points at.
// Directory prefixes ("./js/")
// are skipped: they name no single file.
func ImportMapTargets(data string) []string {
	var m struct {
		Imports map[string]string            `json:"imports"`
		Scopes  map[string]map[string]string `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	add := func(v string) {
		if v == "" || strings.HasSuffix(v, "/") || !isRelativeOrAbsoluteSpecifier(v) {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range m.Imports {
		add(v)
	}
	for _, scope := range m.Scopes {
		for _, v := range scope {
			add(v)
		}
	}
	return out
}

// RewriteImportMap replaces target values in an import map's source text,
// keeping its formatting. replace maps original value -> new value.
func RewriteImportMap(src string, replace map[string]string) string {
	for from, to := range replace {
		if from == to {
			continue
		}
		fq, _ := json.Marshal(from)
		tq, _ := json.Marshal(to)
		// Only values: a JSON value follows a colon.
		re := regexp.MustCompile(`(:\s*)` + regexp.QuoteMeta(string(fq)))
		src = re.ReplaceAllString(src, "${1}"+strings.ReplaceAll(string(tq), "$", "$$"))
	}
	return src
}
