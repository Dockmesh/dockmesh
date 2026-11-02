package secrets

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// SealedPrefix marks an age-encrypted value stored in a TEXT column. Older
// rows hold plaintext in the same columns, so stored values need a marker
// to tell the two apart.
const SealedPrefix = "age:"

// ErrSealedButDisabled is returned for a sealed value while encryption is
// turned off: the plaintext can't be recovered without the key.
var ErrSealedButDisabled = errors.New("value is encrypted but secrets encryption is disabled")

// IsSealed reports whether stored was written by SealString.
func IsSealed(stored string) bool {
	enc, ok := strings.CutPrefix(stored, SealedPrefix)
	if !ok || enc == "" {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(enc)
	return err == nil
}

// SealString encrypts plain for a TEXT column. Empty values and a nil or
// disabled service store the plaintext unchanged.
func (s *Service) SealString(plain string) (string, error) {
	if plain == "" || s == nil || !s.Enabled() {
		return plain, nil
	}
	ct, err := s.Encrypt([]byte(plain))
	if err != nil {
		return "", err
	}
	return SealedPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// OpenString reverses SealString. Values without the marker are legacy
// plaintext and come back unchanged.
func (s *Service) OpenString(stored string) (string, error) {
	if !IsSealed(stored) {
		return stored, nil
	}
	if s == nil || !s.Enabled() {
		return "", ErrSealedButDisabled
	}
	ct, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, SealedPrefix))
	plain, err := s.Decrypt(ct)
	if err != nil {
		return "", fmt.Errorf("decrypt stored secret: %w", err)
	}
	return string(plain), nil
}
