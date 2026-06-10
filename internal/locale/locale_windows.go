//go:build windows

package locale

import (
	"syscall"
	"unsafe"
)

// kernel32 is loaded once at package init. We use the standard library's
// lazy DLL binding so this file does not pull in golang.org/x/sys/windows.
var kernel32 = syscall.NewLazyDLL("kernel32.dll")

// procGetUserDefaultLocaleName resolves the kernel32 export for
// GetUserDefaultLocaleName. Its C signature is:
//
//	int GetUserDefaultLocaleName(LPWSTR lpLocaleName, int cchLocaleName)
//
// It fills lpLocaleName with the user's MUI (Multilingual User Interface)
// locale, e.g. "pl-PL", "en-US". Passing LOCALE_NAME_USER_DEFAULT (= 0)
// is not an argument here — that sentinel is for other NLS functions. This
// function always returns the user MUI locale.
var procGetUserDefaultLocaleName = kernel32.NewProc("GetUserDefaultLocaleName")

// localeNameUserDefault is the cchLocaleName value when no specific LCTYPE
// filtering is needed. We pass the maximum recommended buffer size.
const localeNameMaxLength = 85 // per MSDN, includes terminating NUL

// detectPlatform reads the user's default UI locale via the Windows API.
// Returns an empty string on any failure.
func detectPlatform() string {
	var buf [localeNameMaxLength]uint16

	ret, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	// The function returns the number of characters copied (excluding the
	// terminating NUL) on success, or 0 on failure.
	if ret == 0 {
		return ""
	}

	// The buffer is NUL-terminated, so UTF16ToString handles it directly.
	return syscall.UTF16ToString(buf[:])
}
