//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pickFolderWindows opens a native Vista+ folder picker dialog via
// IFileOpenDialog (shell32 COM API). Falls back to PowerShell
// Shell.Application.BrowseForFolder if COM fails for any reason.
func pickFolderWindows(hwnd uintptr) (string, error) {
	// Try the modern COM dialog first.
	path, err := pickFolderCOM(hwnd)
	if err == nil || path != "" {
		return path, nil
	}
	// Fallback: PowerShell Shell.Application (BIF_NEWDIALOGSTYLE = big dialog).
	return pickFolderPowerShell()
}

// ---------- COM IFileOpenDialog -------------------------------------------

func pickFolderCOM(hwnd uintptr) (string, error) {
	// WebView2 already initialised COM on this thread.
	// Calling CoInitializeEx again is harmless — returns S_FALSE when
	// already STA. We DON'T call CoUninitialize since WebView2 owns it.
	procCoInitializeEx.Call(0, 0x2) // COINIT_APARTMENTTHREADED; ignore return

	// CLSID_FileOpenDialog = {DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7}
	clsid := windows.GUID{
		Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE,
		Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7},
	}
	// IID_IFileOpenDialog = {D57C7288-D4AD-4768-BE02-9D969532D960}
	iid := windows.GUID{
		Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768,
		Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60},
	}

	var dialog *comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)),
		0,    // pUnkOuter = NULL
		0x17, // CLSCTX_ALL
		uintptr(unsafe.Pointer(&iid)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if hr != 0 || dialog == nil {
		return "", fmt.Errorf("CoCreateInstance hr=0x%X", uint32(hr))
	}
	defer releaseCOM(dialog)

	// FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM
	vtableSetOptions(dialog, 0x60)
	vtableSetTitle(dialog, "Wybierz folder docelowy")

	// Show(parentHWND) — modal to our window.
	hr, _, _ = vtableShow(dialog, hwnd)
	if hr != 0 { // S_OK = 0; anything else = cancel or error
		return "", nil
	}

	return vtableGetResultPath(dialog)
}

// --- VTable helpers (IFileOpenDialog / IShellItem) ---
//
// Layout: IUnknown(3) > IModalWindow(1) > IFileDialog(24) > IFileOpenDialog(2)
//
// Show       = index 3   (IModalWindow)
// SetOptions = index 9   (IFileDialog)
// SetTitle   = index 17  (IFileDialog)
// GetResult  = index 20  (IFileDialog)

type comObject struct {
	vtable *[21]uintptr
}

func vtableShow(dialog *comObject, hwnd uintptr) (uintptr, uintptr, error) {
	return syscall.SyscallN(
		dialog.vtable[3],
		uintptr(unsafe.Pointer(dialog)),
		hwnd,
	)
}

func vtableSetOptions(dialog *comObject, flags uint32) {
	syscall.SyscallN(
		dialog.vtable[9],
		uintptr(unsafe.Pointer(dialog)),
		uintptr(flags),
	)
}

func vtableSetTitle(dialog *comObject, title string) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	syscall.SyscallN(
		dialog.vtable[17],
		uintptr(unsafe.Pointer(dialog)),
		uintptr(unsafe.Pointer(titlePtr)),
	)
}

func vtableGetResultPath(dialog *comObject) (string, error) {
	var item *comObject
	hr, _, _ := syscall.SyscallN(
		dialog.vtable[20],
		uintptr(unsafe.Pointer(dialog)),
		uintptr(unsafe.Pointer(&item)),
	)
	if hr != 0 || item == nil {
		return "", fmt.Errorf("GetResult hr=0x%X", uint32(hr))
	}
	defer releaseCOM(item)

	// IShellItem::GetDisplayName — vtable index 5
	// SIGDN_FILESYSPATH = 0x80058000
	var pathPtr *uint16
	hr, _, _ = syscall.SyscallN(
		item.vtable[5],
		uintptr(unsafe.Pointer(item)),
		0x80058000,
		uintptr(unsafe.Pointer(&pathPtr)),
	)
	if hr != 0 || pathPtr == nil {
		return "", fmt.Errorf("GetDisplayName hr=0x%X", uint32(hr))
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pathPtr)))

	return windows.UTF16PtrToString(pathPtr), nil
}

func releaseCOM(object *comObject) {
	if object == nil {
		return
	}
	syscall.SyscallN(object.vtable[2], uintptr(unsafe.Pointer(object)))
}

// ---------- PowerShell fallback -------------------------------------------

func pickFolderPowerShell() (string, error) {
	// Shell.Application.BrowseForFolder with:
	//   0x01 = BIF_RETURNONLYFSDIRS (real paths only)
	//   0x10 = BIF_NEWDIALOGSTYLE   (large resizable dialog)
	//   0x400 = BIF_USENEWUI         (edit box + new UI)
	// Combined = 0x411 — gives a large modern dialog.
	// HWND 0 = no parent (standalone window).
	// Start in Desktop so the dialog is maximised/visible.
	script := `
$shell = New-Object -ComObject Shell.Application
$desktop = [Environment]::GetFolderPath('Desktop')
$folder = $shell.BrowseForFolder(0, 'Wybierz folder docelowy', 0x411, $desktop)
if ($folder) {
    Write-Output $folder.Self.Path
}
`
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("folder picker: %w", err)
	}
	path := strings.TrimSpace(string(out))
	return path, nil
}

// ---------- Lazy DLL/proc -------------------------------------------------

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
)
