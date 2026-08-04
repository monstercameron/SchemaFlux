//go:build !js

package localization

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Localization handles dynamic translation of UI strings
type Localization struct {
	locale     string
	cache      map[string]string
	cacheMutex sync.RWMutex
}

// Global localization instance
var l10n *Localization

// InitLocalization initializes the localization system
func InitLocalization() {
	locale := detectSystemLocale()
	l10n = &Localization{
		locale: locale,
		cache:  make(map[string]string),
	}

	// Log the detected locale
	if locale != "en" && locale != "" {
		schemaflux.GetLogger().Info("Detected system locale - UI will be translated", "locale", locale)
	}
}

// detectSystemLocale detects the system's locale
func detectSystemLocale() string {
	// Try environment variables first
	locale := os.Getenv("LANG")
	if locale == "" {
		locale = os.Getenv("LC_ALL")
	}
	if locale == "" {
		locale = os.Getenv("LC_MESSAGES")
	}

	// On macOS, try to get from defaults
	if locale == "" && runtime.GOOS == "darwin" {
		cmd := exec.Command("defaults", "read", "-g", "AppleLocale")
		if output, err := cmd.Output(); err == nil {
			locale = strings.TrimSpace(string(output))
		}
	}

	// Extract language code (e.g., "en_US.UTF-8" -> "en")
	if locale != "" {
		parts := strings.Split(locale, "_")
		if len(parts) > 0 {
			lang := strings.ToLower(parts[0])
			// Return empty for English (no translation needed)
			if lang == "en" {
				return ""
			}
			return lang
		}
	}

	return ""
}

// T translates a string based on current locale
func T(key string, args ...interface{}) string {
	// If English or no locale set, return original
	if l10n == nil || l10n.locale == "" || l10n.locale == "en" {
		if len(args) > 0 {
			return fmt.Sprintf(key, args...)
		}
		return key
	}

	// Check cache first
	l10n.cacheMutex.RLock()
	if translated, exists := l10n.cache[key]; exists {
		l10n.cacheMutex.RUnlock()
		if len(args) > 0 {
			return fmt.Sprintf(translated, args...)
		}
		return translated
	}
	l10n.cacheMutex.RUnlock()

	// Translate using schemaflux
	translated := l10n.translateString(key)

	// Cache the translation
	l10n.cacheMutex.Lock()
	l10n.cache[key] = translated
	l10n.cacheMutex.Unlock()

	if len(args) > 0 {
		return fmt.Sprintf(translated, args...)
	}
	return translated
}

// translateString performs the actual translation using schemaflux
func (l *Localization) translateString(text string) string {
	// Build translation prompt
	prompt := fmt.Sprintf(`Translate the following UI text from English to %s.
Keep the translation natural and appropriate for a task management application.
Preserve any format specifiers like %%s, %%d, etc.
Only return the translated text, nothing else.

Text to translate: "%s"`, l.getLanguageName(), text)

	// Use schemaflux Generate for translation
	type TranslationResult struct {
		Translation string `json:"translation" jsonschema:"description=The translated text"`
	}

	result, err := schemaflux.Generate[TranslationResult](
		prompt,
		schemaflux.NewGenerateOptions().
			WithIntelligence(schemaflux.Fast).
			WithMode(schemaflux.TransformMode),
	)

	if err != nil {
		// Fall back to original text if translation fails
		return text
	}

	return result.Translation
}

// getLanguageName returns the full language name for the locale code
func (l *Localization) getLanguageName() string {
	languages := map[string]string{
		"es": "Spanish",
		"fr": "French",
		"de": "German",
		"it": "Italian",
		"pt": "Portuguese",
		"ru": "Russian",
		"ja": "Japanese",
		"ko": "Korean",
		"zh": "Chinese",
		"ar": "Arabic",
		"hi": "Hindi",
		"nl": "Dutch",
		"sv": "Swedish",
		"no": "Norwegian",
		"da": "Danish",
		"fi": "Finnish",
		"pl": "Polish",
		"tr": "Turkish",
		"he": "Hebrew",
		"th": "Thai",
		"vi": "Vietnamese",
		"id": "Indonesian",
		"ms": "Malay",
		"uk": "Ukrainian",
		"cs": "Czech",
		"hu": "Hungarian",
		"ro": "Romanian",
		"bg": "Bulgarian",
		"hr": "Croatian",
		"sr": "Serbian",
		"sk": "Slovak",
		"sl": "Slovenian",
		"et": "Estonian",
		"lv": "Latvian",
		"lt": "Lithuanian",
		"ca": "Catalan",
		"eu": "Basque",
		"gl": "Galician",
	}

	if name, exists := languages[l.locale]; exists {
		return name
	}
	return l.locale
}

// BatchTranslate translates multiple strings at once for efficiency
func BatchTranslate(keys []string) map[string]string {
	if l10n == nil || l10n.locale == "" || l10n.locale == "en" {
		result := make(map[string]string)
		for _, key := range keys {
			result[key] = key
		}
		return result
	}

	// Check cache for all keys first
	result := make(map[string]string)
	toTranslate := []string{}

	l10n.cacheMutex.RLock()
	for _, key := range keys {
		if translated, exists := l10n.cache[key]; exists {
			result[key] = translated
		} else {
			toTranslate = append(toTranslate, key)
		}
	}
	l10n.cacheMutex.RUnlock()

	// If all cached, return early
	if len(toTranslate) == 0 {
		return result
	}

	// Batch translate remaining strings
	translations := l10n.batchTranslateStrings(toTranslate)

	// Cache and add to result
	l10n.cacheMutex.Lock()
	for i, key := range toTranslate {
		if i < len(translations) {
			l10n.cache[key] = translations[i]
			result[key] = translations[i]
		} else {
			// Fallback to original if translation failed
			result[key] = key
		}
	}
	l10n.cacheMutex.Unlock()

	return result
}

// batchTranslateStrings translates multiple strings in one API call
func (l *Localization) batchTranslateStrings(texts []string) []string {
	if len(texts) == 0 {
		return []string{}
	}

	// Build batch translation prompt
	textList := strings.Join(texts, "\n")
	prompt := fmt.Sprintf(`Translate the following UI texts from English to %s.
Keep translations natural and appropriate for a task management application.
Preserve any format specifiers like %%s, %%d, etc.
Return ONLY the translations, one per line, in the same order as the input.

Texts to translate:
%s`, l.getLanguageName(), textList)

	type BatchTranslationResult struct {
		Translations []string `json:"translations" jsonschema:"description=List of translated texts in order"`
	}

	result, err := schemaflux.Generate[BatchTranslationResult](
		prompt,
		schemaflux.NewGenerateOptions().
			WithIntelligence(schemaflux.Fast).
			WithMode(schemaflux.TransformMode),
	)

	if err != nil {
		// Return original texts if translation fails
		return texts
	}

	return result.Translations
}

// GetLocale returns the current locale
func GetLocale() string {
	if l10n == nil {
		return "en"
	}
	if l10n.locale == "" {
		return "en"
	}
	return l10n.locale
}

// SetLocale manually sets the locale (for testing or user preference)
func SetLocale(locale string) {
	if l10n == nil {
		InitLocalization()
	}
	l10n.locale = locale
	// Clear cache when locale changes
	l10n.cacheMutex.Lock()
	l10n.cache = make(map[string]string)
	l10n.cacheMutex.Unlock()
}

// PreloadCommonStrings preloads common UI strings for better performance
func PreloadCommonStrings() {
	if l10n == nil || l10n.locale == "" || l10n.locale == "en" {
		return
	}

	// List of common strings to preload
	commonStrings := []string{
		AppName,
		StatusToday,
		ActionAdd,
		ActionComplete,
		ActionDelete,
		ActionEdit,
		ModalEnterSave,
		ProgressPending,
		StatusNoTasks,
		ShortcutsTitle,
		ActivityLogTitle,
	}

	// Batch translate for efficiency
	BatchTranslate(commonStrings)
}
