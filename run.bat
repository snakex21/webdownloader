@echo off
REM Run script: builds (if needed) and launches the app in dev mode (DevTools enabled)
setlocal

set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

if not exist build\webdownloader.exe (
    echo Building first ...
    call build.bat || exit /b 1
)

REM DevTools enabled (F12) in dev mode. Build.bat does not pass --debug,
REM so production builds run with DevTools disabled.
start "" build\webdownloader.exe --debug
endlocal
