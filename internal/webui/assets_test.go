package webui

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEmbeddedUIServer(t *testing.T) {
	server, err := StartServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	for _, path := range []string{
		"",
		"styles.css",
		"app.js",
		"vendor/fontawesome/css/fontawesome.min.css",
		"vendor/fontawesome/css/solid.min.css",
		"vendor/fontawesome/webfonts/fa-solid-900.woff2",
	} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %q: %v", path, err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read %q: read=%v close=%v", path, readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %q status = %d", path, response.StatusCode)
		}
		if response.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %q did not include a content security policy", path)
		}
	}
}

func TestFrontendHasNoRuntimeCDNDependencies(t *testing.T) {
	for _, path := range []string{"web/index.html", "web/styles.css", "web/app.js"} {
		data, err := assets.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		for _, forbidden := range []string{
			"cdnjs.cloudflare.com",
			"fonts.googleapis.com",
			"flagcdn.com",
		} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s contains external runtime dependency %q", path, forbidden)
			}
		}
	}
}
