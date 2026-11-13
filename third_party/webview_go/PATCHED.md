# Local copy of github.com/webview/webview_go

Upstream: github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6 (MIT,
see LICENSE; `libs/mswebview2` is the Microsoft WebView2 SDK header, BSD-3).

One change, in `libs/webview/include/webview.h` (`embed()`): when the
environment variable `WEBVIEW_GO_USER_DATA_FOLDER` is set, it replaces the
hard-coded WebView2 profile folder `%APPDATA%\<exe name>`. WebDownloader sets
it in `--portable` mode so the browser profile stays next to the executable.
The official `WEBVIEW2_USER_DATA_FOLDER` variable does not work here because
webview_go's built-in loader bypasses WebView2Loader.dll, which is what reads it.
