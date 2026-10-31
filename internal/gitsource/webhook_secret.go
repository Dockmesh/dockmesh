package gitsource

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

// encryptedWebhookPrefix marks an age-encrypted webhook secret. The column
// is TEXT and older rows hold the secret in plaintext, so stored values
// need a marker to tell the two apart.
const encryptedWebhookPrefix = "age:"

func (s *Service) encryptionEnabled() bool {
	return s.secrets != nil && s.secrets.Enabled()
}

func (s *Service) encryptWebhookSecret(plain string) (string, error) {
	if plain == "" || !s.encryptionEnabled() {
		return plain, nil
	}
	ct, err := s.secrets.Encrypt([]byte(plain))
	if err != nil {
		return "", err
	}
	return encryptedWebhookPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Service) decryptWebhookSecret(stored string) (string, error) {
	enc, ok := strings.CutPrefix(stored, encryptedWebhookPrefix)
	if !ok {
		return stored, nil
	}
	ct, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return stored, nil //nolint:nilerr // not valid base64: a legacy plaintext secret that happens to start with the prefix
	}
	if !s.encryptionEnabled() {
		return "", fmt.Errorf("webhook secret is encrypted but secrets encryption is disabled")
	}
	plain, err := s.secrets.Decrypt(ct)
	if err != nil {
		return "", fmt.Errorf("decrypt webhook secret: %w", err)
	}
	return string(plain), nil
}

// EncryptLegacyWebhookSecrets encrypts webhook secrets that were stored
// in plaintext and returns how many rows it rewrote.
func (s *Service) EncryptLegacyWebhookSecrets(ctx context.Context) (int, error) {
	if !s.encryptionEnabled() {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name, webhook_secret FROM stack_git_sources
		 WHERE COALESCE(webhook_secret, '') <> ''`)
	if err != nil {
		return 0, err
	}
	type legacy struct{ stack, secret string }
	var pending []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.stack, &l.secret); err != nil {
			rows.Close()
			return 0, err
		}
		if !strings.HasPrefix(l.secret, encryptedWebhookPrefix) {
			pending = append(pending, l)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, l := range pending {
		enc, err := s.encryptWebhookSecret(l.secret)
		if err != nil {
			return n, fmt.Errorf("encrypt webhook secret of %s: %w", l.stack, err)
		}
		// Matching on the old value leaves a secret saved in the meantime alone.
		res, err := s.db.ExecContext(ctx,
			`UPDATE stack_git_sources SET webhook_secret = ? WHERE stack_name = ? AND webhook_secret = ?`,
			enc, l.stack, l.secret)
		if err != nil {
			return n, fmt.Errorf("store webhook secret of %s: %w", l.stack, err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, nil
}
