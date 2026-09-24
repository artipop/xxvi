package app

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

// preferred copies the person's languages from System Settings into a
// NUL-separated buffer the caller frees. The webview cannot be asked instead:
// it answers with the languages this bundle is localized into, not the ones
// the person reads.
static char *preferred(int *count) {
	CFArrayRef langs = CFLocaleCopyPreferredLanguages();
	*count = 0;
	if (langs == NULL) return NULL;
	CFIndex n = CFArrayGetCount(langs);
	char *buf = calloc(n > 0 ? n * 64 : 1, 1);
	char *p = buf;
	for (CFIndex i = 0; i < n; i++) {
		CFStringRef s = CFArrayGetValueAtIndex(langs, i);
		if (CFStringGetCString(s, p, 64, kCFStringEncodingUTF8)) {
			p += strlen(p) + 1;
			(*count)++;
		}
	}
	CFRelease(langs);
	return buf;
}
*/
import "C"

import "unsafe"

func systemLanguages() []string {
	var n C.int
	buf := C.preferred(&n)
	if buf == nil {
		return envLanguages()
	}
	defer C.free(unsafe.Pointer(buf))
	out := make([]string, 0, int(n))
	p := buf
	for i := 0; i < int(n); i++ {
		s := C.GoString(p)
		out = append(out, s)
		p = (*C.char)(unsafe.Add(unsafe.Pointer(p), len(s)+1))
	}
	if len(out) == 0 {
		return envLanguages()
	}
	return out
}
