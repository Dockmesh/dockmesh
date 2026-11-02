package targets

import (
	"encoding/json"
	"fmt"

	"github.com/dockmesh/dockmesh/internal/secrets"
)

// secretFields are the config fields that hold credentials. They are
// sealed one by one so the rest of the config stays readable for the SQL
// that records SFTP host keys and the disk-role lookup of local paths.
var secretFields = []string{"password", "secret_key", "private_key", "passphrase"}

// SealConfig returns cfg as JSON with every credential field encrypted.
func SealConfig(sec *secrets.Service, cfg any) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return string(raw), nil
	}
	for _, k := range secretFields {
		v, ok := m[k].(string)
		if !ok || v == "" {
			continue
		}
		if m[k], err = sec.SealString(v); err != nil {
			return "", fmt.Errorf("encrypt %s: %w", k, err)
		}
	}
	out, err := json.Marshal(m)
	return string(out), err
}

// OpenConfig parses a stored config and decrypts its credential fields.
// Unparseable JSON yields a nil config.
func OpenConfig(sec *secrets.Service, raw string) (any, error) {
	var cfg any
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return nil, nil
	}
	m, ok := cfg.(map[string]any)
	if !ok {
		return cfg, nil
	}
	for _, k := range secretFields {
		v, ok := m[k].(string)
		if !ok || v == "" {
			continue
		}
		plain, err := sec.OpenString(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		m[k] = plain
	}
	return m, nil
}

// ResealIfPlain re-encodes a stored config whose credentials are still in
// plaintext. changed is false when there was nothing to encrypt.
func ResealIfPlain(sec *secrets.Service, raw string) (out string, changed bool, err error) {
	if sec == nil || !sec.Enabled() {
		return raw, false, nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		return raw, false, nil
	}
	for _, k := range secretFields {
		if v, ok := m[k].(string); ok && v != "" && !secrets.IsSealed(v) {
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	cfg, err := OpenConfig(sec, raw)
	if err != nil {
		return raw, false, err
	}
	out, err = SealConfig(sec, cfg)
	return out, err == nil, err
}
