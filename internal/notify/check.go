package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"

	"github.com/dockmesh/dockmesh/internal/redact"
)

// CheckChannel reports what keeps a channel of channelType with config
// from being built: an unknown type, a field it does not read, or a
// missing required field.
func CheckChannel(channelType string, config json.RawMessage) error {
	if !validType(channelType) {
		return ErrUnknownType
	}
	m := redact.ToMap(config)
	if m == nil {
		return errors.New("channel config must be a JSON object")
	}
	fields := ConfigFields(channelType)
	if err := redact.CheckKeys(m, fields); err != nil {
		return err
	}
	for k := range m {
		if !slices.Contains(fields, k) {
			return fmt.Errorf("%s is not a field of a %s channel", k, channelType)
		}
	}
	impl, err := buildChannel(&Channel{Type: channelType, Config: config}, http.DefaultClient)
	if err != nil {
		return err
	}
	if e, ok := impl.(*emailChannel); ok && len(e.cfg.To) > maxTestRecipients {
		return fmt.Errorf("at most %d recipients", maxTestRecipients)
	}
	return nil
}

// maxTestRecipients bounds the recipients of a channel CheckChannel takes.
const maxTestRecipients = 50

// SendTest sends n once to a channel that is not stored, as setup's Send
// test does before the database exists. hc and dial (for email) carry the
// dial rules; the error names no secret of the config.
func SendTest(ctx context.Context, hc *http.Client, dial func(ctx context.Context, network, addr string) (net.Conn, error), channelType string, config json.RawMessage, n Notification) error {
	if err := CheckChannel(channelType, config); err != nil {
		return err
	}
	c := &Channel{Type: channelType, Config: config}
	impl, err := buildChannel(c, hc)
	if err != nil {
		return err
	}
	lang := channelLanguage(config)
	if e, ok := impl.(*emailChannel); ok {
		e.lang = lang
		e.dial = dial
	} else {
		impl = localized{impl: impl, lang: lang}
	}
	if err := impl.Send(ctx, n); err != nil {
		return errors.New(errorText(err, true, secretValues(c)))
	}
	return nil
}
