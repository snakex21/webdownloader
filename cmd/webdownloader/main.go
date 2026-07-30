// Command webdownloader is a desktop application that mirrors websites to
// a local directory.
package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"

	webview "github.com/webview/webview_go"

	"github.com/asrock/webdownloader/internal/webui"
)

const (
	applicationTitle = "WebDownloader"
	minWindowWidth   = 900
	minWindowHeight  = 720
)

func main() {
	debug := flag.Bool("debug", false, "enable webview devtools")
	flag.Parse()

	if err := run(*debug); err != nil {
		log.Fatal(err)
	}
}

func run(debug bool) error {
	htmlBytes, err := webui.HTML()
	if err != nil {
		return fmt.Errorf("load embedded UI: %w", err)
	}

	translations, err := webui.Translations()
	if err != nil {
		return fmt.Errorf("load embedded translations: %w", err)
	}

	htmlPath, err := writeHTMLToTemp(htmlBytes)
	if err != nil {
		return fmt.Errorf("prepare UI: %w", err)
	}

	window := webview.New(debug)
	defer window.Destroy()

	window.SetTitle(applicationTitle)
	windowWidth, windowHeight := loadWindowSize()
	window.SetSize(windowWidth, windowHeight, webview.HintNone)
	window.SetSize(minWindowWidth, minWindowHeight, webview.HintMin)

	api := newAPI(window, translations)
	if err := bindAPI(window, api); err != nil {
		return err
	}

	window.Init("window.__goReady = true;")
	window.Navigate("file:///" + filepath.ToSlash(htmlPath))
	window.Run()
	saveWindowSize(uintptr(window.Window()))
	return nil
}
