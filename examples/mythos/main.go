// Command mythos runs a downloader.Download against https://mythos.observer/
// using the real network. Useful for smoke-testing the rewrites end to end.
//
//	go run ./examples/mythos [depth]
//
// Output goes to <exe-dir>/output/mythos.observer.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/asrock/webdownloader/internal/downloader"
)

func main() {
	depth := flag.Int("depth", 1, "recursion depth (1 = main page only)")
	all := flag.Bool("all", true, "download all file attachments (PDF/ZIP/etc)")
	rawURL := flag.String("url", "https://mythos.observer/", "url to crawl")
	flag.Parse()

	parsed, err := url.Parse(*rawURL)
	if err != nil || parsed.Host == "" {
		log.Fatalf("invalid url: %v", err)
	}

	exe, _ := os.Executable()
	if exe == "" {
		exe, _ = os.Getwd()
	}
	outputDir := filepath.Join(filepath.Dir(exe), "output", strings.TrimPrefix(parsed.Hostname(), "www."))

	fmt.Printf("URL:        %s\n", *rawURL)
	fmt.Printf("Output:     %s\n", outputDir)
	fmt.Printf("Depth:      %d\n", *depth)
	fmt.Printf("All files:  %v\n", *all)
	fmt.Println()

	var pages, assets, attachments atomic.Int64
	progress := make(chan struct{}, 200)

	summary, err := downloader.Download(downloader.Options{
		URL:         *rawURL,
		OutputDir:   outputDir,
		MaxDepth:    *depth,
		DownloadAll: *all,
	}, downloader.Events{
		OnPage: func(ev downloader.PageEvent) {
			switch ev.Status {
			case "downloading":
				fmt.Printf("  [d=%d] GET %s\n", ev.Depth, ev.URL)
			case "done":
				pages.Store(int64(ev.Pages))
				assets.Store(int64(ev.Assets))
				attachments.Store(int64(ev.Attachments))
				fmt.Printf("  [d=%d] OK  %s (pages=%d assets=%d files=%d)\n",
					ev.Depth, ev.URL, ev.Pages, ev.Assets, ev.Attachments)
			case "error":
				fmt.Printf("  [d=%d] ERR %s\n", ev.Depth, ev.URL)
			}
		},
		OnAsset: func(assetURL, kind string) {
			fmt.Printf("        + %s %s\n", kind, assetURL)
			progress <- struct{}{}
		},
		OnError: func(pageURL string, err error) {
			fmt.Printf("  ERROR  %s: %v\n", pageURL, err)
		},
		OnComplete: func(s downloader.Summary) {
			close(progress)
			fmt.Println()
			fmt.Printf("Done. Pages=%d Assets=%d Attachments=%d OutputDir=%s\n",
				s.Pages, s.Assets, s.Attachments, s.OutputDir)
		},
	})
	_ = summary
	_ = strconv.Itoa
	_ = progress
	if err != nil {
		log.Fatalf("download failed: %v", err)
	}
}
