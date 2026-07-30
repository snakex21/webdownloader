// Command webdownloader is a desktop application that mirrors websites to
// a local directory.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	webview "github.com/webview/webview_go"

	"github.com/asrock/webdownloader/internal/webui"
)

const (
	applicationTitle = "WebDownloader"
	minWindowWidth   = 900
	minWindowHeight  = 720
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	debug := flag.Bool("debug", false, "enable webview devtools")
	portable := flag.Bool("portable", false, "store settings, logs and downloads next to the executable")
	flag.Parse()

	if err := configureStorage(*portable); err != nil {
		showFatalError(applicationTitle, err.Error())
		return 1
	}

	logFile, err := setupLogging()
	if err != nil {
		showFatalError(applicationTitle, err.Error())
		return 1
	}
	defer logFile.Close()

	if err := run(*debug); err != nil {
		log.Printf("fatal error: %v", err)
		showFatalError(applicationTitle, err.Error())
		return 1
	}
	return 0
}

func run(debug bool) error {
	uiServer, err := webui.StartServer()
	if err != nil {
		return fmt.Errorf("start embedded UI server: %w", err)
	}
	defer uiServer.Close()

	translations, err := webui.Translations()
	if err != nil {
		return fmt.Errorf("load embedded translations: %w", err)
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
	window.Navigate(uiServer.URL)
	window.Run()
	saveWindowSize(uintptr(window.Window()))
	return nil
}
