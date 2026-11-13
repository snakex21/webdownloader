@echo off
setlocal
REM Build script: produces a single Windows .exe in the project directory
REM   - Hides the console window (-H windowsgui)
REM   - Strips symbol/debug info (-s -w) to shrink the binary

set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

echo Building webdownloader.exe ...
go build -ldflags="-H windowsgui -s -w" -o webdownloader.exe .\cmd\webdownloader
if errorlevel 1 (
    echo.
    echo Build FAILED.
    endlocal
    exit /b 1
)

echo.
echo OK -^> webdownloader.exe
endlocal
