# Rendering engines for the accurate mode

The accurate mode opens every page in a real browser engine, records all
network responses through the Chrome DevTools Protocol (`Network.*` events +
`Network.getResponseBody`) and saves them at their mirror paths. Which
browser does the rendering is a separate choice; the capture code is shared.

## Options evaluated (November 2026, Windows 10 22H2)

| | (a) system Edge via CDP | (b) app's WebView2 runtime | (c) Lightpanda | (d) installed Chrome via CDP |
|---|---|---|---|---|
| Works on the test PC | **no** – Edge was uninstalled (only an `Installer` folder left) | **yes** | **no** – no Windows build (Linux/macOS only) | yes |
| Extra dependency | Edge browser | none (the UI already requires WebView2) | binary download, WSL | Chrome |
| Start-up | – | ~1.0 s | – | ~1 s |
| Peak private memory, 15 pages | – | **170 MB** | – | 344 MB |
| Desktop site, depth 2 | – | 1 page, 119 files, 2.8 s | – | 1 page, 119 files, 2.6 s |
| snakex21, depth 3 | – | 95 pages, 4.6 MB, 118 s | – | 95 pages, 4.6 MB, 124 s |
| Licence | proprietary, not redistributed | proprietary, part of Windows / Evergreen runtime | AGPL-3.0 | proprietary, not redistributed |
| Risk | Edge is removable and missing on some systems | uses the runtime's `CreateWebViewEnvironmentWithOptionsInternal` entry point (the same one webview_go's loader calls) | – | Chrome often missing |

(a) and (d) run the same code with a different executable; WebView2 157 is
the same Chromium as Edge 157, so (a) would behave like (b).

Launching `msedgewebview2.exe` directly as a headless browser does not work
(it exits immediately without its host), so (b) hosts a real, hidden WebView2
controller instead: `internal/wv2host` creates a private environment in a
temporary profile with `--remote-debugging-port=0` (an options COM object
implemented in Go), reads the port from `DevToolsActivePort` and lets chromedp
attach to the existing page target. It runs in its own browser process, so
the app's UI and its bindings are never reachable over that port.

## Decision

**Auto = WebView2 → Edge → Chrome.** WebView2 is always present wherever the
app starts, uses half the memory of Chrome, and gives identical results.
Edge/Chrome remain as fallbacks (also selectable in the advanced options;
`WEBDOWNLOADER_BROWSER` can point at a specific executable).

## Offline completeness (served by the offline preview, all non-local hosts blocked)

| Copy | Desktop site (ES modules, import map, fetch JSON) | snakex21 games (linked only from `games-data.js`) |
|---|---|---|
| 2.0 fast | blank page, 10 module 404s | 0 of 25 game pages |
| 2.0 accurate (rendered DOM only) | frozen snapshot, modules 404, nothing opens | games saved (DOM links); hub saved pre-rendered |
| 2.1 fast | desktop works, windows open; `data/*.json` missing (built as `` `data/${name}.json` ``), start menu partial | 25 of 25; 2048, Wojna Er, Tetris, Snake play |
| 2.1 accurate (WebView2) | everything works; only 48 px program icons that load when a window opens are missing | 25 of 25; same games play |

Measured with `tools/enginebench` (time, files, browser memory) and a
headless-Chrome check of the copies (404s, exceptions, interaction probes).
