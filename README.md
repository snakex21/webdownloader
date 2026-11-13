# WebDownloader

Desktop application for downloading websites with subpages, resources, and
attachments — written in **Go** with a **webview** UI.

The HTML, CSS, JavaScript, icons, and translations are embedded in the binary;
the application logic (HTTP fetching, HTML rewriting, BFS crawl) runs natively
in Go. No Node.js or Electron is required.

![WebDownloader](docs/images/screenshot-app.png)

## Features

- Recursive page download with configurable depth (1 = start page only,
  2 = start page + pages it links to, … up to 5)
- Automatic asset downloading (images, CSS, JS, video posters, …)
- Optional attachment download (PDF, ZIP, DOC, … — 25 extensions)
- Multi-language UI (30+ languages, auto-detected)
- Live progress (pages, assets, attachments) and rolling log
- Native folder picker + "Open output folder" button
- Fully embedded frontend with no runtime CDN dependencies
- Confirmed cleanup of downloaded folders from history
- Resume from history: continues in the same folder and skips files that
  are already complete
- Single-file portable executable

## Requirements

- **Go 1.26.2+**
- **CGO** + a C compiler (GCC / MinGW on Windows — comes with `tdm-gcc`
  or installed via MSYS2 / Chocolatey)
- **Microsoft Edge WebView2** runtime (pre-installed on Windows 10/11 since
  2022; if missing download the Evergreen installer from
  <https://developer.microsoft.com/microsoft-edge/webview2/>)

## Quick start

```bat
git clone https://github.com/snakex21/webdownloader.git
cd webdownloader
build.bat
```

`build.bat` downloads the Go modules on first use and produces
`webdownloader.exe` in the project directory. It contains the application
icon, HTML, and translations, so it can be copied and run as a standalone
file. Pass a version to stamp it into the binary (shown in the log):
`build.bat 2.1.0`.

The release build uses `-trimpath -ldflags="-H windowsgui -s -w"`:

| Build                                   | Size (amd64) |
| --------------------------------------- | ------------ |
| `go build ./cmd/webdownloader`          | ~22.1 MB     |
| `build.bat` (`-trimpath`, `-s -w`)      | ~10.9 MB     |

`-trimpath` also keeps local source paths (and so the Windows user name)
out of the binary.

By default, preferences and logs are stored in
`%LOCALAPPDATA%\WebDownloader`, while downloaded sites go to
`%USERPROFILE%\Downloads\WebDownloader`. Run the application with
`--portable` to keep preferences, logs, and the default output directory next
to the executable.

For development checks (the same ones CI runs):

```bat
gofmt -l cmd internal tools
go vet ./...
go test -race ./...
```

`-race` needs CGO and a C compiler, which the build already requires.
`go run ./tools/examples/mythos -url https://example.com/ -depth 1 -out %TEMP%\wd-smoke`
runs the download engine from the command line without the UI (exit code
2 when nothing could be saved, e.g. a 404 start page).

## Download behaviour

- **Output layout.** Pages are saved as `path/index.html`; assets keep their
  server path. Query strings are dropped for static files
  (`logo.png?v=3` → `logo.png`) and kept for dynamic or extension-less URLs
  (`img.php?id=1` → `img__id_1.php`), capped at 64 characters plus a hash.
  File names come from the URL, not from `Content-Disposition`, so links in
  the mirror can be rewritten before a file is fetched.
- **Path safety.** URL paths containing `..`, Windows-reserved names
  (`CON`, `NUL`, `COM1`, …), characters Windows rejects, or trailing dots and
  spaces are refused; all writes go through `os.Root`, so nothing can be
  written outside the output folder (also via symlinks).
- **No partial files.** Every file is streamed to a `*.wdpart` temp file and
  renamed into place only when complete; on cancel, network errors, disk
  full or access denied the temp file is removed. Large files are streamed
  to disk, not buffered in memory (HTML and CSS are read into memory because
  they are rewritten).
- **Resume.** There is no HTTP `Range` resume of a single file; instead a
  resumed run reuses every file that is already complete and downloads the
  rest.
- **Timeouts and retries.** A request fails when no data arrives for 30 s
  (slow but steady downloads are not cut off). Network errors, timeouts,
  HTTP 408, 429 and 5xx are retried twice with exponential back-off
  (0.5 s, 1 s), honouring `Retry-After` up to 10 s. Up to 10 redirects are
  followed; a pasted cookie is only sent to the starting origin.
- **Errors.** Failed pages, assets and attachments are listed in the UI log,
  `download-errors.log` and `download-report.json` inside the output folder.
- **Concurrency.** Pages are crawled one at a time, with a 0.5 s pause
  between pages to be polite to the server.

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
│   │   ├── path.go          # URL → on-disk path mapping and validation
│   │   ├── storage.go       # atomic, root-confined file writes
│   │   └── downloader.go    # BFS orchestrator + event callbacks
│   ├── locale/
│   │   └── locale.go        # system UI language detection
│   └── webui/
│       ├── assets.go        # embedded frontend access
│       └── web/
│           ├── index.html   # frontend structure
│           ├── styles.css   # frontend styles
│           ├── app.js       # frontend behavior
│           ├── i18n.json    # 30+ language translations
│           └── vendor/      # offline third-party assets and licenses
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
webdownloader.exe [--debug] [--portable]
```

- `--debug` — enable the webview DevTools (right-click → Inspect). Useful
  for tweaking the front-end; not needed by end users.
- `--portable` — store preferences, logs, and downloads next to the EXE.

## How it works

1. The frontend is embedded by `internal/webui` and served from memory on a
   random loopback address (`127.0.0.1`). No temporary UI files are created;
   preferences and history are persisted by the Go backend.
2. The Go side exposes a small RPC API to JS (`api_download`,
   `api_cancel`, `api_pause`, `api_resume`, `api_pickFolder`,
   `api_openFolder`, `api_openFile`, `api_revealPath`, `api_getLocale`,
   `api_defaultOutputPath`, `api_savePrefs`, `api_loadPrefs`,
   `api_deleteFolder`, `api_deleteHistoryItem`, `api_i18n`). Paths passed to the
   open/delete calls are validated in Go (only directories / `.html`,
   `.json`, `.log` files; only recorded download folders can be deleted).
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
