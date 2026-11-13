// Command enginebench downloads one site with a chosen mode/engine and
// prints totals, wall time and the peak memory of the browser processes it
// started (Windows). It is the harness behind the engine comparison in
// docs/engines.md.
//
//	go run ./tools/enginebench -url https://example.com/ -mode browser -engine webview2 -out C:\tmp\x
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asrock/webdownloader/internal/downloader"
)

func main() {
	rawURL := flag.String("url", "", "start URL")
	out := flag.String("out", "", "output folder (must not exist)")
	mode := flag.String("mode", "http", "http | browser")
	engine := flag.String("engine", "", "webview2 | edge | chrome | (empty = auto)")
	depth := flag.Int("depth", 2, "crawl depth")
	maxPages := flag.Int("maxpages", 0, "page limit (0 = none)")
	flag.Parse()
	if *rawURL == "" || *out == "" {
		log.Fatal("-url and -out are required")
	}

	var peak atomic.Int64
	stop := make(chan struct{})
	if runtime.GOOS == "windows" {
		go sampleMemory(&peak, stop)
	}
	start := time.Now()
	sum, err := downloader.Download(downloader.Options{
		URL:       *rawURL,
		OutputDir: *out,
		MaxDepth:  *depth,
		MaxPages:  *maxPages,
		Mode:      downloader.Mode(*mode),
		Engine:    downloader.Engine(*engine),
	}, downloader.Events{})
	close(stop)
	if err != nil {
		log.Fatal(err)
	}
	files, byExt := countFiles(*out)
	res := map[string]any{
		"url": *rawURL, "mode": *mode, "engine": sum.Engine,
		"pages": sum.Pages, "assets": sum.Assets, "bytes": sum.Bytes,
		"errors": sum.Errors, "failed": sum.Failed, "failKind": sum.FailKind,
		"skippedExternal": sum.SkippedExternal, "needsServer": sum.NeedsServer,
		"seconds":       time.Since(start).Seconds(),
		"peakBrowserMB": peak.Load() / (1 << 20),
		"files":         files, "byExt": byExt,
	}
	data, _ := json.MarshalIndent(res, "", " ")
	fmt.Println(string(data))
}

// sampleMemory tracks the summed private memory of browser processes whose
// command line contains the downloader's temporary profile prefix.
func sampleMemory(peak *atomic.Int64, stop <-chan struct{}) {
	script := `(Get-CimInstance Win32_Process | Where-Object { $_.Name -match '^(msedgewebview2|msedge|chrome)\.exe$' -and ($_.CommandLine -like '*webdownloader-wv2-*' -or $_.CommandLine -like '*webdownloader-chrome-*') } | Measure-Object PrivatePageCount -Sum).Sum`
	for {
		select {
		case <-stop:
			return
		default:
		}
		outb, err := exec.Command("powershell", "-NoProfile", "-Command", script).Output()
		if err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(outb)), 10, 64); err == nil && v > peak.Load() {
				peak.Store(v)
			}
		}
		select {
		case <-stop:
			return
		case <-time.After(700 * time.Millisecond):
		}
	}
}

func countFiles(dir string) (int, map[string]int) {
	n := 0
	byExt := map[string]int{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
			byExt[strings.ToLower(filepath.Ext(p))]++
		}
		return nil
	})
	return n, byExt
}
