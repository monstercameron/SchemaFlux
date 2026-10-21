//go:build js && wasm

package localization

import (
	"fmt"
	"strings"
	"syscall/js"
)

type Localization struct {
	locale string
	cache  map[string]string
}

var l10n *Localization

func InitLocalization() {
	locale := ""
	navigator := js.Global().Get("navigator")
	if !navigator.IsUndefined() && !navigator.IsNull() {
		lang := navigator.Get("language")
		if !lang.IsUndefined() && !lang.IsNull() {
			locale = strings.ToLower(strings.TrimSpace(lang.String()))
		}
	}
	if strings.HasPrefix(locale, "en") {
		locale = ""
	}
	l10n = &Localization{
		locale: locale,
		cache:  map[string]string{},
	}
}

func T(key string, args ...interface{}) string {
	if len(args) > 0 {
		return fmt.Sprintf(key, args...)
	}
	return key
}

func BatchTranslate(keys []string) map[string]string {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		result[key] = key
	}
	return result
}

func PreloadCommonStrings() {}

func GetLocale() string {
	if l10n == nil {
		return ""
	}
	return l10n.locale
}
