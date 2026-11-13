package webui

import (
	"regexp"
	"testing"
)

// TestUIStringsAreTranslated checks that every key the UI uses exists in
// English (the fallback) and Polish, so no raw key or hard-coded text shows.
func TestUIStringsAreTranslated(t *testing.T) {
	tr, err := Translations()
	if err != nil {
		t.Fatal(err)
	}
	html, _ := assets.ReadFile("web/index.html")
	js, _ := assets.ReadFile("web/app.js")

	keys := map[string]bool{}
	for _, m := range regexp.MustCompile(`data-i18n(?:-placeholder|-title|-aria)?="([^"]+)"`).FindAllSubmatch(html, -1) {
		keys[string(m[1])] = true
	}
	// Literal keys only; tt('err.' + kind) style prefixes are listed below.
	for _, m := range regexp.MustCompile(`\btt\('([a-zA-Z0-9_.]*[a-zA-Z0-9_])'`).FindAllSubmatch(js, -1) {
		keys[string(m[1])] = true
	}
	for _, kind := range []string{"pages", "assets", "files", "errors"} {
		keys["count."+kind+".one"] = true
		keys["count."+kind+".other"] = true
	}
	for _, s := range []string{"completed", "warn", "cancelled", "interrupted", "paused", "error", "downloading"} {
		keys["statusLabel."+s] = true
	}
	for _, k := range []string{"dns", "refused", "timeout", "tls", "browser", "too_large", "write", "invalid", "busy", "resume", "other"} {
		keys["err."+k] = true
	}
	if len(keys) < 50 {
		t.Fatalf("found only %d keys - extraction broken?", len(keys))
	}
	for _, lang := range []string{"en", "pl"} {
		table, ok := tr[lang].(map[string]any)
		if !ok {
			t.Fatalf("language %s missing", lang)
		}
		for k := range keys {
			if _, ok := table[k]; !ok {
				t.Errorf("%s: missing translation %q", lang, k)
			}
		}
	}
}
