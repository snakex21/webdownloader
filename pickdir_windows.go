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

	var dialogPtr uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)),
		0,            // pUnkOuter = NULL
		0x17,         // CLSCTX_ALL
		uintptr(unsafe.Pointer(&iid)),
		uintptr(unsafe.Pointer(&dialogPtr)),
	)
	if hr != 0 {
		return "", fmt.Errorf("CoCreateInstance hr=0x%X", uint32(hr))
	}
	defer releaseCOM(dialogPtr)

	// FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM
	vtableSetOptions(dialogPtr, 0x60)
	vtableSetTitle(dialogPtr, "Wybierz folder docelowy")

	// Show(parentHWND) — modal to our window.
	hr, _, _ = vtableShow(dialogPtr, hwnd)
	if hr != 0 { // S_OK = 0; anything else = cancel or error
		return "", nil
	}

	return vtableGetResultPath(dialogPtr)
}

// --- VTable helpers (IFileOpenDialog / IShellItem) ---
//
// Layout: IUnknown(3) > IModalWindow(1) > IFileDialog(24) > IFileOpenDialog(2)
//
// Show       = index 3   (IModalWindow)
// SetOptions = index 9   (IFileDialog)
// SetTitle   = index 17  (IFileDialog)
// GetResult  = index 20  (IFileDialog)

func vtableShow(dlg, hwnd uintptr) (uintptr, uintptr, error) {
	vtbl := *(*uintptr)(unsafe.Pointer(dlg))
	proc := *(*uintptr)(unsafe.Pointer(vtbl + 3*8))
	return syscall.SyscallN(proc, dlg, hwnd)
}

func vtableSetOptions(dlg uintptr, flags uint32) {
	vtbl := *(*uintptr)(unsafe.Pointer(dlg))
	proc := *(*uintptr)(unsafe.Pointer(vtbl + 9*8))
	syscall.SyscallN(proc, dlg, uintptr(flags))
}

func vtableSetTitle(dlg uintptr, title string) {
	p, _ := syscall.UTF16PtrFromString(title)
	vtbl := *(*uintptr)(unsafe.Pointer(dlg))
	proc := *(*uintptr)(unsafe.Pointer(vtbl + 17*8))
	syscall.SyscallN(proc, dlg, uintptr(unsafe.Pointer(p)))
}

func vtableGetResultPath(dlg uintptr) (string, error) {
	vtbl := *(*uintptr)(unsafe.Pointer(dlg))
	getResultProc := *(*uintptr)(unsafe.Pointer(vtbl + 20*8))

	var itemPtr uintptr
	hr, _, _ := syscall.SyscallN(getResultProc, dlg, uintptr(unsafe.Pointer(&itemPtr)))
	if hr != 0 || itemPtr == 0 {
		return "", fmt.Errorf("GetResult hr=0x%X", uint32(hr))
	}
	defer releaseCOM(itemPtr)

	// IShellItem::GetDisplayName — vtable index 5
	// SIGDN_FILESYSPATH = 0x80058000
	itemVtbl := *(*uintptr)(unsafe.Pointer(itemPtr))
	getNameProc := *(*uintptr)(unsafe.Pointer(itemVtbl + 5*8))

	var pathPtr uintptr
	hr, _, _ = syscall.SyscallN(getNameProc, itemPtr, 0x80058000, uintptr(unsafe.Pointer(&pathPtr)))
	if hr != 0 || pathPtr == 0 {
		return "", fmt.Errorf("GetDisplayName hr=0x%X", uint32(hr))
	}
	defer procCoTaskMemFree.Call(pathPtr)

	return utf16PtrToString(pathPtr), nil
}

func releaseCOM(obj uintptr) {
	if obj == 0 {
		return
	}
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	proc := *(*uintptr)(unsafe.Pointer(vtbl + 2*8)) // Release
	syscall.SyscallN(proc, obj)
}

func utf16PtrToString(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	var n int
	for p := ptr; ; p += 2 {
		if *(*uint16)(unsafe.Pointer(p)) == 0 {
			break
		}
		n++
	}
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), n))
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
	ole32               = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx  = ole32.NewProc("CoInitializeEx")
	procCoUninitialize  = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree   = ole32.NewProc("CoTaskMemFree")
)
