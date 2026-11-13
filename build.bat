@echo off
setlocal
REM Build script: produces a single Windows .exe in the project directory
REM   - Hides the console window (-H windowsgui)
REM   - Strips symbol/debug info (-s -w) to shrink the binary (~22 MB -> ~11 MB)
REM   - -trimpath removes local source paths (user name, drive layout) from
REM     the binary and makes builds reproducible across machines
REM Usage: build.bat [version]   e.g. build.bat 2.1.0

set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

set VERSION=%~1
if "%VERSION%"=="" set VERSION=dev

echo Building webdownloader.exe (version %VERSION%) ...
go build -trimpath -ldflags="-H windowsgui -s -w -X main.version=%VERSION%" -o webdownloader.exe .\cmd\webdownloader
if errorlevel 1 (
    echo.
    echo Build FAILED.
    endlocal
    exit /b 1
)

echo.
echo OK -^> webdownloader.exe
endlocal
