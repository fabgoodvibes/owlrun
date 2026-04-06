// Package i18n provides lightweight internationalisation for Owlrun.
// Locale strings are stored as flat JSON files embedded at compile time.
// The package exposes a simple T(key) function and supports runtime locale
// switching with automatic fallback to English.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

//go:embed locales/*.json
var localeFS embed.FS

var (
	mu       sync.RWMutex
	current  = "en"
	messages = map[string]map[string]string{} // lang -> key -> value
)

func init() {
	loadAll()
}

// loadAll reads every JSON file in locales/ into memory.
func loadAll() {
	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		log.Printf("i18n: failed to read embedded locales: %v", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		lang := strings.TrimSuffix(e.Name(), ".json")
		data, err := localeFS.ReadFile("locales/" + e.Name())
		if err != nil {
			log.Printf("i18n: skip %s: %v", e.Name(), err)
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			log.Printf("i18n: invalid JSON in %s: %v", e.Name(), err)
			continue
		}
		messages[lang] = m
	}
}

// SetLocale changes the active locale. Unknown locales fall back to English.
func SetLocale(lang string) {
	mu.Lock()
	defer mu.Unlock()
	lang = normaliseLang(lang)
	if _, ok := messages[lang]; !ok {
		lang = "en"
	}
	current = lang
}

// Locale returns the active locale tag.
func Locale() string {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// T returns the localised string for key. If the key is missing in the
// current locale it falls back to English; if still missing it returns the
// key itself. Optional args are passed to fmt.Sprintf when present.
func T(key string, args ...any) string {
	mu.RLock()
	lang := current
	mu.RUnlock()

	if v, ok := messages[lang][key]; ok {
		if len(args) > 0 {
			return fmt.Sprintf(v, args...)
		}
		return v
	}
	// Fallback to English.
	if v, ok := messages["en"][key]; ok {
		if len(args) > 0 {
			return fmt.Sprintf(v, args...)
		}
		return v
	}
	return key
}

// Locales returns the list of available locale tags.
func Locales() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(messages))
	for k := range messages {
		out = append(out, k)
	}
	return out
}

// Messages returns the full translation map for a locale (for serving
// to the dashboard JS). Falls back to English for unknown locales.
func Messages(lang string) map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	lang = normaliseLang(lang)
	if m, ok := messages[lang]; ok {
		return m
	}
	return messages["en"]
}

// normaliseLang converts "ca-ES" -> "ca", "pt_BR" -> "pt", etc.
func normaliseLang(lang string) string {
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	return strings.ToLower(lang)
}
