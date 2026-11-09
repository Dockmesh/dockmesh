package i18n

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// storedText is the database form of a Text. Each argument records its
// type, because a plain JSON number would come back as a float and break
// the %d verbs.
type storedText struct {
	Key  string      `json:"key"`
	Args []storedArg `json:"args,omitempty"`
}

type storedArg struct {
	S *string     `json:"s,omitempty"`
	I *int64      `json:"i,omitempty"`
	F *float64    `json:"f,omitempty"`
	T *storedText `json:"t,omitempty"`
	L []storedArg `json:"l,omitempty"`
}

func toStored(t Text) storedText {
	st := storedText{Key: t.Key}
	for _, a := range t.Args {
		st.Args = append(st.Args, toStoredArg(a))
	}
	return st
}

func toStoredArg(a any) storedArg {
	switch v := a.(type) {
	case Text:
		st := toStored(v)
		return storedArg{T: &st}
	case Bullets:
		l := make([]storedArg, 0, len(v))
		for _, item := range v {
			l = append(l, toStoredArg(item))
		}
		return storedArg{L: l}
	case fmt.Stringer:
		s := v.String()
		return storedArg{S: &s}
	}
	rv := reflect.ValueOf(a)
	switch rv.Kind() {
	case reflect.String:
		s := rv.String()
		return storedArg{S: &s}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := rv.Int()
		return storedArg{I: &i}
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		return storedArg{F: &f}
	}
	s := fmt.Sprint(a)
	return storedArg{S: &s}
}

func fromStored(st storedText) Text {
	t := Text{Key: st.Key}
	for _, a := range st.Args {
		t.Args = append(t.Args, fromStoredArg(a))
	}
	return t
}

func fromStoredArg(a storedArg) any {
	switch {
	case a.S != nil:
		return *a.S
	case a.I != nil:
		return *a.I
	case a.F != nil:
		return *a.F
	case a.T != nil:
		return fromStored(*a.T)
	}
	l := make(Bullets, 0, len(a.L))
	for _, item := range a.L {
		l = append(l, fromStoredArg(item))
	}
	return l
}

// Encode returns t in the form Stored reads, or "" for an empty Text or
// one JSON cannot hold (a NaN metric value), which then shows its stored
// English text.
func (t Text) Encode() string {
	if t.Key == "" {
		return ""
	}
	b, err := json.Marshal(toStored(t))
	if err != nil {
		return ""
	}
	return string(b)
}

// Stored renders an encoded Text in lang. Rows written before texts were
// stored as keys, and keys a later version dropped, show fallback.
func Stored(encoded, lang, fallback string) string {
	if encoded == "" {
		return fallback
	}
	var st storedText
	if err := json.Unmarshal([]byte(encoded), &st); err != nil {
		return fallback
	}
	if _, ok := catalogs[Default][st.Key]; !ok {
		return fallback
	}
	return fromStored(st).In(lang)
}
