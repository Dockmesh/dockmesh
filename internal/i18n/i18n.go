// Package i18n renders the server-side texts users receive outside the web
// UI (notification channels, mail) in English or German. Logs, CLI output
// and API field names stay English.
package i18n

import "fmt"

// Default is the language of a channel or user without a choice.
const Default = "en"

// Text is a message key plus its arguments, rendered per language later.
// Templates use explicit argument indexes (%[1]s) so a translation may
// order them differently.
type Text struct {
	Key  string
	Args []any
}

// Msg builds a Text.
func Msg(key string, args ...any) Text { return Text{Key: key, Args: args} }

// In renders t in lang, falling back to English per key.
func (t Text) In(lang string) string { return T(lang, t.Key, t.Args...) }

// Normalize maps a stored language to a supported one.
func Normalize(lang string) string {
	if _, ok := catalogs[lang]; ok {
		return lang
	}
	return Default
}

// T renders key in lang. An unknown key renders as the key itself so a
// missing entry is visible instead of empty.
func T(lang, key string, args ...any) string {
	tmpl, ok := catalogs[Normalize(lang)][key]
	if !ok {
		tmpl, ok = catalogs[Default][key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}

var catalogs = map[string]map[string]string{"en": en, "de": de}
