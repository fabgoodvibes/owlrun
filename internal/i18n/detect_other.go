//go:build !windows

package i18n

import "os"

// DetectLocale returns the best locale tag for the current OS session.
// On Linux/macOS it reads LANG, LC_ALL, or LC_MESSAGES, falling back to "en".
func DetectLocale() string {
	for _, key := range []string{"LANG", "LC_ALL", "LC_MESSAGES"} {
		if v := os.Getenv(key); v != "" {
			// Strip encoding suffix: "ca_ES.UTF-8" -> "ca_ES" -> "ca"
			return normaliseLang(v)
		}
	}
	return "en"
}
