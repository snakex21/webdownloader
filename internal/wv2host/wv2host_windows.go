//go:build windows

// Package wv2host starts a private, hidden WebView2 instance and exposes it
// over the Chrome DevTools Protocol, so the downloader can render pages with
// the WebView2 runtime the app already depends on (no Edge/Chrome needed).
//
// The instance gets its own temporary user data folder, so it runs in a
// separate browser process from the app's UI: the UI and its Go bindings are
// never reachable over the DevTools port. The environment is created by
// calling the runtime's EmbeddedBrowserWebView.dll directly (the same thing
// webview_go's built-in loader does) with an options object implemented in Go
// that adds --remote-debugging-port=0. Chromium then writes the chosen port to
// <user data>\EBWebView\DevToolsActivePort.
package wv2host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ErrRuntimeNotFound means no WebView2 runtime is installed.
var ErrRuntimeNotFound = errors.New("WebView2 runtime not found")

const webview2ClientGUID = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

var (
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procCoInitializeEx     = ole32.NewProc("CoInitializeEx")
	procCoUninitialize     = ole32.NewProc("CoUninitialize")
	procCoTaskMemAlloc     = ole32.NewProc("CoTaskMemAlloc")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostThreadMessageW = user32.NewProc("PostThreadMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
)

const (
	coinitApartmentThreaded = 0x2
	wmQuit                  = 0x0012
	wmAppClose              = 0x8000 + 1
	sOK                     = 0
	eNoInterface            = 0x80004002
	ePointer                = 0x80004003
	wsOverlappedWindow      = 0x00CF0000
)

// Host is a running hidden WebView2 browser with a DevTools endpoint.
type Host struct {
	// DevToolsURL is the browser-level WebSocket URL
	// (ws://127.0.0.1:<port>/devtools/browser/<id>).
	DevToolsURL string
	// Version is the WebView2 runtime version that was used.
	Version string

	userDataDir string
	threadID    uint32
	done        chan struct{}
	closeOnce   sync.Once
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	iidIUnknown       = guid{0x00000000, 0x0000, 0x0000, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidEnvOptions     = guid{0x2fde08a8, 0x1e9a, 0x4766, [8]byte{0x8c, 0x05, 0x95, 0xa9, 0xce, 0xb9, 0xd1, 0xc5}}
	iidEnvCompleted   = guid{0x4e8a3389, 0xc9d8, 0x4bd2, [8]byte{0xb6, 0xb5, 0x12, 0x4f, 0xee, 0x6c, 0xc1, 0x4d}}
	iidCtrlCompleted  = guid{0x6c4819f3, 0xc9b7, 0x4260, [8]byte{0x81, 0x27, 0xc9, 0xf5, 0xbd, 0xe7, 0xf6, 0x8c}}
	errStartupTimeout = errors.New("WebView2 did not start in time")
)

// winMsg is the Win32 MSG structure.
type winMsg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       [2]int32
	lPrivate uint32
}

// comObject is the memory layout every COM object starts with. The objects
// below are allocated in Go and pinned in liveObjects while COM may use them.
type comObject struct {
	vtbl *[16]uintptr
	refs int32
	// kind selects behaviour in the shared callbacks.
	kind int
	host *startState
}

const (
	kindOptions = iota + 1
	kindEnvHandler
	kindCtrlHandler
)

// startState is shared between the COM callbacks of one Start call.
type startState struct {
	hwnd       uintptr
	args       string
	env        unsafe.Pointer
	controller unsafe.Pointer
	result     chan error
	once       sync.Once
}

func (s *startState) finish(err error) {
	s.once.Do(func() { s.result <- err })
}

var (
	liveMu      sync.Mutex
	liveObjects = map[*comObject]struct{}{}

	callbacksOnce sync.Once
	optionsVtbl   [16]uintptr
	handlerVtbl   [16]uintptr
)

func initCallbacks() {
	callbacksOnce.Do(func() {
		qi := syscall.NewCallback(comQueryInterface)
		addRef := syscall.NewCallback(comAddRef)
		release := syscall.NewCallback(comRelease)

		optionsVtbl[0], optionsVtbl[1], optionsVtbl[2] = qi, addRef, release
		optionsVtbl[3] = syscall.NewCallback(optGetArgs)    // get_AdditionalBrowserArguments
		optionsVtbl[4] = syscall.NewCallback(optPutIgnored) // put_AdditionalBrowserArguments
		optionsVtbl[5] = syscall.NewCallback(optGetEmpty)   // get_Language
		optionsVtbl[6] = optionsVtbl[4]                     // put_Language
		optionsVtbl[7] = syscall.NewCallback(optGetVersion) // get_TargetCompatibleBrowserVersion
		optionsVtbl[8] = optionsVtbl[4]                     // put_TargetCompatibleBrowserVersion
		optionsVtbl[9] = syscall.NewCallback(optGetFalse)   // get_AllowSingleSignOnUsingOSPrimaryAccount
		optionsVtbl[10] = optionsVtbl[4]                    // put_AllowSingleSignOnUsingOSPrimaryAccount

		handlerVtbl[0], handlerVtbl[1], handlerVtbl[2] = qi, addRef, release
		handlerVtbl[3] = syscall.NewCallback(handlerInvoke)
	})
}

func newObject(kind int, vtbl *[16]uintptr, st *startState) *comObject {
	o := &comObject{vtbl: vtbl, refs: 1, kind: kind, host: st}
	liveMu.Lock()
	liveObjects[o] = struct{}{}
	liveMu.Unlock()
	return o
}

func releaseObject(o *comObject) {
	liveMu.Lock()
	delete(liveObjects, o)
	liveMu.Unlock()
}

func comQueryInterface(o *comObject, riid *guid, out *unsafe.Pointer) uintptr {
	if out == nil || riid == nil {
		return ePointer
	}
	id := *riid
	ok := id == iidIUnknown
	switch o.kind {
	case kindOptions:
		ok = ok || id == iidEnvOptions
	case kindEnvHandler:
		ok = ok || id == iidEnvCompleted
	case kindCtrlHandler:
		ok = ok || id == iidCtrlCompleted
	}
	if !ok {
		*out = nil
		return eNoInterface
	}
	o.refs++
	*out = unsafe.Pointer(o)
	return sOK
}

func comAddRef(o *comObject) uintptr {
	o.refs++
	return uintptr(o.refs)
}

func comRelease(o *comObject) uintptr {
	o.refs--
	// Objects stay pinned until Close; never free memory COM might touch.
	return uintptr(max(o.refs, 0))
}

// coTaskString returns s as a CoTaskMemAlloc'ed UTF-16 string, which the
// caller (WebView2) frees.
func coTaskString(s string) unsafe.Pointer {
	u, _ := windows.UTF16FromString(s)
	mem, _, _ := procCoTaskMemAlloc.Call(uintptr(len(u) * 2))
	if mem == 0 {
		return nil
	}
	// mem is foreign (COM heap) memory; unsafe.Add avoids a uintptr
	// conversion that go vet cannot tell apart from a misuse.
	p := unsafe.Add(unsafe.Pointer(nil), mem)
	copy(unsafe.Slice((*uint16)(p), len(u)), u)
	return p
}

func putString(out *unsafe.Pointer, s string) uintptr {
	if out == nil {
		return ePointer
	}
	*out = coTaskString(s)
	return sOK
}

func optGetArgs(o *comObject, out *unsafe.Pointer) uintptr  { return putString(out, o.host.args) }
func optGetEmpty(_ *comObject, out *unsafe.Pointer) uintptr { return putString(out, "") }

// The oldest WebView2 runtime release: we only use APIs present since then.
func optGetVersion(_ *comObject, out *unsafe.Pointer) uintptr {
	return putString(out, "86.0.616.0")
}
func optGetFalse(_ *comObject, out *int32) uintptr {
	if out == nil {
		return ePointer
	}
	*out = 0
	return sOK
}
func optPutIgnored(_ *comObject, _ uintptr) uintptr { return sOK }

// comCall invokes method index of the COM object obj's vtable.
func comCall(obj unsafe.Pointer, index int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, index*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

// handlerInvoke is Invoke(HRESULT, IUnknown*) for both completion handlers.
func handlerInvoke(o *comObject, hr uintptr, result unsafe.Pointer) uintptr {
	st := o.host
	if int32(hr) < 0 || result == nil {
		st.finish(fmt.Errorf("WebView2 initialisation failed (HRESULT 0x%08x)", uint32(hr)))
		return sOK
	}
	switch o.kind {
	case kindEnvHandler:
		comCall(result, 1) // AddRef: keep the environment alive
		st.env = result
		ctrlHandler := newObject(kindCtrlHandler, &handlerVtbl, st)
		// ICoreWebView2Environment::CreateCoreWebView2Controller
		if r := comCall(result, 3, st.hwnd, uintptr(unsafe.Pointer(ctrlHandler))); int32(r) < 0 {
			st.finish(fmt.Errorf("create WebView2 controller (HRESULT 0x%08x)", uint32(r)))
		}
	case kindCtrlHandler:
		comCall(result, 1) // AddRef
		st.controller = result
		// A desktop-sized viewport so pages lay out as in a normal window.
		bounds := [4]int32{0, 0, 1366, 900}
		comCall(result, 6, uintptr(unsafe.Pointer(&bounds))) // put_Bounds
		st.finish(nil)
	}
	return sOK
}

// runtimeDLL locates EmbeddedBrowserWebView.dll of the installed (evergreen)
// WebView2 runtime, per-machine first, then per-user.
func runtimeDLL() (dll, version string, err error) {
	arch := "x64"
	switch runtime.GOARCH {
	case "386":
		arch = "x86"
	case "arm64":
		arch = "arm64"
	}
	sub := `SOFTWARE\Microsoft\EdgeUpdate\ClientState\` + webview2ClientGUID
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, sub, registry.QUERY_VALUE|registry.WOW64_32KEY)
		if err != nil {
			continue
		}
		dir, _, err := k.GetStringValue("EBWebView")
		k.Close()
		if err != nil || dir == "" {
			continue
		}
		path := filepath.Join(dir, "EBWebView", arch, "EmbeddedBrowserWebView.dll")
		if _, err := os.Stat(path); err == nil {
			return path, filepath.Base(dir), nil
		}
	}
	return "", "", ErrRuntimeNotFound
}

// Available reports whether a WebView2 runtime is installed.
func Available() bool {
	_, _, err := runtimeDLL()
	return err == nil
}

// Start launches the hidden WebView2 browser. Close must be called to stop
// it and remove its temporary profile.
func Start(timeout time.Duration) (*Host, error) {
	dllPath, version, err := runtimeDLL()
	if err != nil {
		return nil, err
	}
	userDataDir, err := os.MkdirTemp("", "webdownloader-wv2-*")
	if err != nil {
		return nil, fmt.Errorf("create WebView2 profile: %w", err)
	}
	initCallbacks()

	h := &Host{Version: version, userDataDir: userDataDir, done: make(chan struct{})}
	st := &startState{
		// Keep the instance quiet: no first-run work, no background
		// networking that would show up as captured responses.
		args: "--remote-debugging-port=0 --no-first-run --disable-sync " +
			"--disable-background-networking --mute-audio",
		result: make(chan error, 1),
	}
	threadReady := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(h.done)

		procCoInitializeEx.Call(0, coinitApartmentThreaded)
		defer procCoUninitialize.Call()

		tid, _, _ := procGetCurrentThreadId.Call()
		h.threadID = uint32(tid)

		className, _ := windows.UTF16PtrFromString("STATIC")
		hwnd, _, cerr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), 0,
			wsOverlappedWindow, 0, 0, 1366, 900, 0, 0, 0, 0)
		if hwnd == 0 {
			threadReady <- fmt.Errorf("create hidden window: %v", cerr)
			return
		}
		st.hwnd = hwnd

		lib, err := windows.LoadDLL(dllPath)
		if err != nil {
			procDestroyWindow.Call(hwnd)
			threadReady <- fmt.Errorf("load WebView2 runtime: %w", err)
			return
		}
		proc, err := lib.FindProc("CreateWebViewEnvironmentWithOptionsInternal")
		if err != nil {
			procDestroyWindow.Call(hwnd)
			threadReady <- fmt.Errorf("WebView2 runtime entry point: %w", err)
			return
		}
		threadReady <- nil

		options := newObject(kindOptions, &optionsVtbl, st)
		envHandler := newObject(kindEnvHandler, &handlerVtbl, st)
		udf, _ := windows.UTF16PtrFromString(userDataDir)
		// (checkRunningInstance, runtimeType=installed, userDataFolder, options, handler)
		r, _, _ := proc.Call(1, 0, uintptr(unsafe.Pointer(udf)),
			uintptr(unsafe.Pointer(options)), uintptr(unsafe.Pointer(envHandler)))
		if int32(r) < 0 {
			st.finish(fmt.Errorf("create WebView2 environment (HRESULT 0x%08x)", uint32(r)))
		}

		var msg winMsg
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			if msg.message == wmAppClose {
				if st.controller != nil {
					comCall(st.controller, 24) // ICoreWebView2Controller::Close
					comCall(st.controller, 2)  // Release
					st.controller = nil
				}
				if st.env != nil {
					comCall(st.env, 2)
					st.env = nil
				}
				procDestroyWindow.Call(hwnd)
				procPostQuitMessage.Call(0)
				continue
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
		liveMu.Lock()
		for o := range liveObjects {
			if o.host == st {
				delete(liveObjects, o)
			}
		}
		liveMu.Unlock()
		_ = lib // keep the runtime DLL loaded for the process lifetime
	}()

	if err := <-threadReady; err != nil {
		<-h.done
		_ = os.RemoveAll(userDataDir)
		return nil, err
	}

	select {
	case err = <-st.result:
	case <-time.After(timeout):
		err = errStartupTimeout
	}
	if err == nil {
		h.DevToolsURL, err = waitDevToolsURL(userDataDir, timeout)
	}
	if err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// waitDevToolsURL reads the port Chromium picked from DevToolsActivePort.
func waitDevToolsURL(userDataDir string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	candidates := []string{
		filepath.Join(userDataDir, "EBWebView", "DevToolsActivePort"),
		filepath.Join(userDataDir, "DevToolsActivePort"),
	}
	for time.Now().Before(deadline) {
		for _, path := range candidates {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) < 2 {
				continue
			}
			port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
			if err != nil || port <= 0 {
				continue
			}
			return "ws://127.0.0.1:" + strconv.Itoa(port) + strings.TrimSpace(lines[1]), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", errors.New("WebView2 DevTools endpoint did not appear")
}

// Close stops the hidden browser and removes its temporary profile. It is
// safe to call more than once.
func (h *Host) Close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		if h.threadID != 0 {
			procPostThreadMessageW.Call(uintptr(h.threadID), wmAppClose, 0, 0)
		}
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
		}
		// The browser processes exit asynchronously and keep profile files
		// open for a moment.
		for i := 0; i < 50; i++ {
			if err := os.RemoveAll(h.userDataDir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}
