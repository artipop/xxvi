//go:build darwin

package nativeterm

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>

// The native half lives in libxxvinative.dylib, with Ghostty in it, and is
// opened at run time rather than linked: Ghostty's full library and the
// libghostty-vt the holder uses share hundreds of symbols, and only separate
// images keep them apart. Opening it late also keeps it out of every build and
// test that does not show a native terminal.
typedef void (*nt_show_fn)(void *, const char *, const char *, double, double, double, double, double, double, double, double, double, double);
typedef void (*nt_id_fn)(const char *);
typedef void (*nt_void_fn)(void);
static nt_show_fn p_show;
static nt_id_fn p_hide, p_close, p_focus;
static nt_void_fn p_close_all, p_init;

static int nt_load(const char *path) {
	void *h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (!h) return 0;
	p_show = (nt_show_fn)dlsym(h, "nt_show_in");
	p_hide = (nt_id_fn)dlsym(h, "nt_hide");
	p_close = (nt_id_fn)dlsym(h, "nt_close");
	p_close_all = (nt_void_fn)dlsym(h, "nt_close_all");
	p_focus = (nt_id_fn)dlsym(h, "nt_focus");
	p_init = (nt_void_fn)dlsym(h, "nt_init");
	return p_show && p_hide && p_close && p_close_all && p_focus && p_init;
}
static void nt_call_show(void *w, const char *id, const char *cmd, double x, double y, double wd, double ht, double cx, double cy, double cw, double ch, double dpr, double font) { p_show(w, id, cmd, x, y, wd, ht, cx, cy, cw, ch, dpr, font); }
static void nt_call_hide(const char *id) { p_hide(id); }
static void nt_call_close(const char *id) { p_close(id); }
static void nt_call_close_all(void) { p_close_all(); }
static void nt_call_focus(const char *id) { p_focus(id); }
static void nt_call_init(void) { p_init(); }
*/
import "C"

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

// LibName is the native half's file, beside the executable in a bundle's
// Frameworks (build/darwin/Taskfile.yml puts it there).
const LibName = "libxxvinative.dylib"

var (
	loadOnce sync.Once
	loaded   bool
)

// Available says native terminals can be shown: the native half was found and
// opened. XXVI_NATIVE_LIB names it outside a bundle.
func Available() bool {
	loadOnce.Do(func() {
		var paths []string
		if p := os.Getenv("XXVI_NATIVE_LIB"); p != "" {
			paths = append(paths, p)
		}
		if exe, err := os.Executable(); err == nil {
			dir := filepath.Dir(exe)
			paths = append(paths, filepath.Join(dir, "..", "Frameworks", LibName), filepath.Join(dir, LibName))
		}
		for _, p := range paths {
			cp := C.CString(p)
			ok := C.nt_load(cp) == 1
			C.free(unsafe.Pointer(cp))
			if ok {
				loaded = true
				return
			}
		}
	})
	return loaded
}

// Init sets Ghostty up. It has to be called from main, on the main thread,
// before the application starts any process: Ghostty's setup calls
// setlocale, which deadlocks against a fork happening at the same moment
// (native/view_darwin.m). Go's fork lock is held throughout, in case anything
// is already starting one.
func Init() {
	if !Available() {
		return
	}
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	C.nt_call_init()
}

// Show puts the terminal id in a native view over the window at the page's
// rectangle (CSS pixels from the top left, at the page's device pixel ratio
// dpr), starting its bridge with command the first time, in fontSize points.
// Only the part inside the clip rectangle cx, cy, cw, ch is seen: what of the
// pane the page itself would show.
func Show(nswindow unsafe.Pointer, id, command string, x, y, w, h, cx, cy, cw, ch, dpr, fontSize float64) {
	if !Available() {
		return
	}
	cid, ccmd := C.CString(id), C.CString(command)
	defer C.free(unsafe.Pointer(cid))
	defer C.free(unsafe.Pointer(ccmd))
	C.nt_call_show(nswindow, cid, ccmd, C.double(x), C.double(y), C.double(w), C.double(h), C.double(cx), C.double(cy), C.double(cw), C.double(ch), C.double(dpr), C.double(fontSize))
}

// Hide takes the view off screen and keeps it — and its bridge — alive.
func Hide(id string) {
	if !Available() {
		return
	}
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	C.nt_call_hide(cid)
}

// Close ends the view and its bridge.
func Close(id string) {
	if !Available() {
		return
	}
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	C.nt_call_close(cid)
}

// CloseAll ends every view: the page that laid them out has been replaced.
func CloseAll() {
	if !Available() {
		return
	}
	C.nt_call_close_all()
}

// Focus gives the view the keyboard, now or as it appears; "" gives it back to
// the page.
func Focus(id string) {
	if !Available() {
		return
	}
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	C.nt_call_focus(cid)
}
