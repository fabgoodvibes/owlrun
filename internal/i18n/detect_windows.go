//go:build windows

package i18n

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetUserDefaultLocale  = kernel32.NewProc("GetUserDefaultLocaleName")
)

// DetectLocale returns the best locale tag for the current OS session.
// On Windows it calls GetUserDefaultLocaleName, falling back to the LANG
// environment variable, and finally to "en".
func DetectLocale() string {
	// Try Windows API first.
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	r, _, _ := procGetUserDefaultLocale.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r > 0 {
		name := syscall.UTF16ToString(buf)
		if name != "" {
			return normaliseLang(name)
		}
	}

	// Fallback to environment.
	for _, key := range []string{"LANG", "LC_ALL", "LC_MESSAGES"} {
		if v := os.Getenv(key); v != "" {
			return normaliseLang(v)
		}
	}
	return "en"
}
