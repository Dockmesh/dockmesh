package notify

import (
	"context"
	"encoding/json"
	"testing"
)

type captureImpl struct{ got Notification }

func (c *captureImpl) Send(_ context.Context, n Notification) error {
	c.got = n
	return nil
}

func TestChannelLanguage(t *testing.T) {
	cases := []struct {
		cfg  string
		want string
	}{
		{`{"url":"x","language":"de"}`, "de"},
		{`{"url":"x"}`, "en"},
		{`{"language":"xx"}`, "en"},
		{``, "en"},
		{`not json`, "en"},
	}
	for _, c := range cases {
		if got := channelLanguage(json.RawMessage(c.cfg)); got != c.want {
			t.Errorf("channelLanguage(%s) = %q, want %q", c.cfg, got, c.want)
		}
	}
}

func TestLocalizedSend(t *testing.T) {
	inner := &captureImpl{}
	l := localized{impl: inner, lang: "de"}
	n := Notification{Title: "t", Body: "b", Localize: func(lang string) (string, string) { return "T-" + lang, "B-" + lang }}
	if err := l.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if inner.got.Title != "T-de" || inner.got.Body != "B-de" || inner.got.Lang != "de" {
		t.Errorf("got %+v", inner.got)
	}
	plain := &captureImpl{}
	if err := (localized{impl: plain, lang: "de"}).Send(context.Background(), Notification{Title: "fixed"}); err != nil {
		t.Fatal(err)
	}
	if plain.got.Title != "fixed" || plain.got.Lang != "de" {
		t.Errorf("got %+v", plain.got)
	}
}
