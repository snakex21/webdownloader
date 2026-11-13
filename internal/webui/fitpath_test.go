package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFitPath runs the folder-field shortening (app.js fitPath) in Node,
// when Node is installed, with a fixed-width "font" of 8 px per character.
func TestFitPath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const src = require('fs').readFileSync(process.argv[2], 'utf8');
eval(src.match(/function fitPath[\s\S]*?\r?\n}\r?\n/)[0]);
const p = ['C:', 'Users', 'User', 'Downloads', 'WebDownloader'].join(String.fromCharCode(92));
const out = [500, 200, 120, 100].map(w => fitPath(p, t => t.length * 8 <= w));
console.log(out.join('|'));`
	dir := t.TempDir()
	js := filepath.Join(dir, "fit.js")
	if err := os.WriteFile(js, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(dir, "app.js")
	data, _ := assets.ReadFile("web/app.js")
	if err := os.WriteFile(app, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, js, app).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	want := `C:\Users\User\Downloads\WebDownloader|…\Downloads\WebDownloader|…\WebDownloader|…\WebDow…der`
	if got != want {
		t.Errorf("fitPath:\n got %s\nwant %s", got, want)
	}
}
