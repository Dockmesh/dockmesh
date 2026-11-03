package targets

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	if !decodes(raw, &m) || m == nil {
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
	if !decodes([]byte(raw), &cfg) {
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

// OpenConfigLenient is OpenConfig for listing and running: a credential
// that can't be decrypted is dropped from the config and its field name
// returned, so one broken row doesn't stop every other job and target.
func OpenConfigLenient(sec *secrets.Service, raw string) (cfg any, unreadable []string) {
	if !decodes([]byte(raw), &cfg) {
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
			delete(m, k)
			unreadable = append(unreadable, k)
			continue
		}
		m[k] = plain
	}
	return m, unreadable
}

// HasCredentials reports whether a stored config holds any credential.
func HasCredentials(raw string) bool {
	var m map[string]any
	if !decodes([]byte(raw), &m) {
		return false
	}
	for _, k := range secretFields {
		if v, ok := m[k].(string); ok && v != "" {
			return true
		}
	}
	return false
}

// HasPlainCredentials reports whether a stored config holds a credential
// that is not encrypted.
func HasPlainCredentials(raw string) bool {
	var m map[string]any
	if !decodes([]byte(raw), &m) {
		return false
	}
	for _, k := range secretFields {
		if v, ok := m[k].(string); ok && v != "" && !secrets.IsSealed(v) {
			return true
		}
	}
	return false
}

// ErrCredentialsUnreadable refuses saving a target whose stored
// credentials can't be decrypted without entering them again: keeping the
// blank would replace a value that a restored key could still open.
var ErrCredentialsUnreadable = errors.New("the stored credentials can't be decrypted; enter them again")

// credentialsError describes credentials that couldn't be decrypted.
func credentialsError(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return "stored " + strings.Join(fields, ", ") + " can't be decrypted (check the secrets key); enter it again"
}

// ResealIfPlain re-encodes a stored config whose credentials are still in
// plaintext. changed is false when there was nothing to encrypt.
func ResealIfPlain(sec *secrets.Service, raw string) (out string, changed bool, err error) {
	if sec == nil || !sec.Enabled() {
		return raw, false, nil
	}
	var m map[string]any
	if !decodes([]byte(raw), &m) || m == nil {
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

// decodes reports whether raw is valid JSON for v. Configs that aren't
// are kept as stored rather than treated as an error.
func decodes(raw []byte, v any) bool { return json.Unmarshal(raw, v) == nil }
