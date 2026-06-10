@echo off
REM Build script: produces a single Windows .exe in build\
REM   - Hides the console window (-H windowsgui)
REM   - Strips symbol/debug info (-s -w) to shrink the binary

set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

if not exist build mkdir build

echo Building webdownloader.exe ...
go build -ldflags="-H windowsgui -s -w" -o build\webdownloader.exe .
if errorlevel 1 (
    echo.
    echo Build FAILED.
    exit /b 1
)

echo.
echo OK -^> build\webdownloader.exe
