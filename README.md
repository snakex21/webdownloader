# WebDownloader

Desktop application for downloading websites with subpages, resources, and
attachments — written in **Go** with a **webview** UI.

The whole UI is a single HTML file embedded in the binary; the
application logic (HTTP fetching, HTML rewriting, BFS crawl) runs natively
in Go. No Node.js, no Electron, no separate web server.

![WebDownloader](docs/images/screenshot-app.png)

## Features

- Recursive page download with configurable depth (1–5)
- Automatic asset downloading (images, CSS, JS, video posters, …)
- Optional attachment download (PDF, ZIP, DOC, … — 25 extensions)
- Multi-language UI (30+ languages, auto-detected)
- Live progress (pages, assets, attachments) and rolling log
- Native folder picker + "Open output folder" button
- Single-file portable executable

## Requirements

- **Go 1.22+** (tested on 1.26)
- **CGO** + a C compiler (GCC / MinGW on Windows — comes with `tdm-gcc`
  or installed via MSYS2 / Chocolatey)
- **Microsoft Edge WebView2** runtime (pre-installed on Windows 10/11 since
  2022; if missing download the Evergreen installer from
  <https://developer.microsoft.com/microsoft-edge/webview2/>)

## Quick start

```bat
git clone <repo>
cd webdownloader
go mod download
build.bat
```

The resulting `webdownloader.exe` is created in the project directory. It
contains the application icon, HTML, and translations, so it can be copied
and run as a standalone file.

For development checks:

```bat
go test ./...
go vet ./...
```

## Layout

```
.
├── cmd/
│   └── webdownloader/
│       ├── main.go          # application startup
│       ├── api.go           # frontend RPC state
│       ├── bridge.go        # webview bindings and events
│       ├── download.go      # download commands and lifecycle
│       ├── preferences.go   # persistent user preferences
│       ├── system.go        # operating-system commands
│       ├── pickdir_*.go     # native folder picker
│       ├── window_*.go      # window state per platform
│       ├── rsrc_*.syso      # compiled Windows resources
│       └── resources/
│           └── icon.png     # source application icon
├── internal/
│   ├── downloader/
│   │   ├── fetcher.go       # HTTP client (redirects, UA, timeout)
│   │   ├── html.go          # goquery-based HTML parsing & rewriting
│   │   ├── path.go          # URL → on-disk path mapping
│   │   └── downloader.go    # BFS orchestrator + event callbacks
│   ├── locale/
│   │   └── locale.go        # system UI language detection
│   └── webui/
│       ├── assets.go        # embedded frontend access
│       └── web/
│           ├── index.html   # UI (HTML + CSS + JS)
│           └── i18n.json    # 30+ language translations
├── docs/
│   └── images/              # README and documentation images
├── tools/
│   ├── examples/            # standalone usage examples
│   └── i18n/                # translation maintenance scripts
├── build.bat                # builds webdownloader.exe
└── go.mod
```

## CLI flags

```
webdownloader.exe [--debug]
```

- `--debug` — enable the webview DevTools (right-click → Inspect). Useful
  for tweaking the front-end; not needed by end users.

## How it works

1. The frontend is embedded by `internal/webui` and extracted to a temporary
   file URL so WebView2 can use `localStorage`.
2. The Go side exposes a small RPC API to JS (`api_download`,
   `api_pickFolder`, `api_openFolder`, `api_getLocale`,
   `api_defaultOutputPath`, `api_i18n`).
3. `api_download` spawns a goroutine that runs the BFS crawl, emitting
   `progress`, `asset`, `error` and `complete` events back to the UI
   via `webview.Dispatch + Eval`.
4. The HTML rewriter uses `goquery` (a `cheerio`-style library) to find
   every relative asset reference (`<img>`, `<link>`, `<script>`,
   `<source>`, `<video>`), download it, and rewrite the `src`/`href`/
   `poster` attribute to a relative on-disk path. Inter-page `<a href>`
   links are rewritten similarly so the saved site is fully browseable
   offline.

## License

MIT.
