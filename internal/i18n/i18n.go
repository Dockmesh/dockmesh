// Package i18n renders the texts the server builds for users (notification
// channels, mail, notices, alert history) in English or German. Logs, CLI
// output and API field names stay English.
package i18n

import (
	"fmt"
	"strings"
)

// Default is the language of a channel or user without a choice.
const Default = "en"

// Text is a message key plus its arguments, rendered per language later. An
// argument that is itself a Text is rendered in the same language.
// Templates use explicit argument indexes (%[1]s) so a translation may
// order them differently.
type Text struct {
	Key  string
	Args []any
}

// Bullets is an argument rendered as list lines, each on its own line so
// it can sit between two sentences. Items are strings or Texts.
type Bullets []any

func (b Bullets) render(lang string) string {
	var out strings.Builder
	for _, it := range b {
		out.WriteString("\n- ")
		if t, ok := it.(Text); ok {
			it = t.In(lang)
		}
		fmt.Fprint(&out, it)
	}
	out.WriteString("\n")
	return out.String()
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
	rendered := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case Text:
			a = v.In(lang)
		case Bullets:
			a = v.render(lang)
		}
		rendered[i] = a
	}
	return fmt.Sprintf(tmpl, rendered...)
}

var catalogs = map[string]map[string]string{"en": en, "de": de}
