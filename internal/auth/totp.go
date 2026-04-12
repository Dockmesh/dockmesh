package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"strings"

	"github.com/pquerna/otp/totp"
)

var (
	ErrMFAAlreadyEnrolled = errors.New("mfa already enrolled")
	ErrMFANotEnrolled     = errors.New("mfa not enrolled")
	ErrMFAInvalidCode     = errors.New("invalid mfa code")
)

// TOTPEnrollment is what we hand back to the client to show the QR code.
type TOTPEnrollment struct {
	Secret string `json:"secret"`      // base32 text secret (for manual entry)
	URL    string `json:"url"`         // otpauth:// URL
	QR     string `json:"qr_data_url"` // data:image/png;base64,... for <img src>
}

// StartTOTPEnrollment generates a new TOTP secret for the user and stores
// it (verified=false). The user must call VerifyTOTPEnrollment with a valid
// code from their authenticator to finalize.
func (s *Service) StartTOTPEnrollment(ctx context.Context, userID string) (*TOTPEnrollment, error) {
	// Check user state.
	var username string
	var verified int
	err := s.db.QueryRowContext(ctx,
		`SELECT username, totp_verified FROM users WHERE id = ?`, userID).
		Scan(&username, &verified)
	if err != nil {
		return nil, err
	}
	if verified == 1 {
		return nil, ErrMFAAlreadyEnrolled
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "Dockmesh",
		AccountName: username,
	})
	if err != nil {
		return nil, err
	}

	// Render QR as PNG data URL.
	img, err := key.Image(220, 220)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	qrDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	// Save the secret — not yet verified.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_verified = 0, totp_recovery = NULL WHERE id = ?`,
		key.Secret(), userID); err != nil {
		return nil, err
	}

	return &TOTPEnrollment{
		Secret: key.Secret(),
		URL:    key.URL(),
		QR:     qrDataURL,
	}, nil
}

// VerifyTOTPEnrollment validates the first code and, on success, marks the
// user as MFA-enrolled and generates one-time recovery codes.
func (s *Service) VerifyTOTPEnrollment(ctx context.Context, userID, code string) ([]string, error) {
	var secret sql.NullString
	var verified int
	err := s.db.QueryRowContext(ctx,
		`SELECT totp_secret, totp_verified FROM users WHERE id = ?`, userID).
		Scan(&secret, &verified)
	if err != nil {
		return nil, err
	}
	if !secret.Valid || secret.String == "" {
		return nil, ErrMFANotEnrolled
	}
	if verified == 1 {
		return nil, ErrMFAAlreadyEnrolled
	}
	if !totp.Validate(code, secret.String) {
		return nil, ErrMFAInvalidCode
	}

	// Generate 10 recovery codes. Format: xxxx-xxxx-xxxx (12 hex + dashes).
	codes := make([]string, 10)
	hashes := make([]string, 10)
	for i := range codes {
		raw := make([]byte, 6)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		hexStr := hex.EncodeToString(raw)
		formatted := fmt.Sprintf("%s-%s-%s", hexStr[0:4], hexStr[4:8], hexStr[8:12])
		codes[i] = formatted
		hashes[i] = hashRecoveryCode(formatted)
	}
	hashJSON, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_verified = 1, totp_recovery = ? WHERE id = ?`,
		string(hashJSON), userID); err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP clears the MFA state for a user. No code required; caller is
// responsible for any additional re-auth step.
func (s *Service) DisableTOTP(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = NULL, totp_verified = 0, totp_recovery = NULL WHERE id = ?`,
		userID)
	return err
}

// HasMFA reports whether the user has verified MFA.
func (s *Service) HasMFA(ctx context.Context, userID string) (bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT totp_verified FROM users WHERE id = ?`, userID).Scan(&v)
	if err != nil {
		return false, err
	}
	return v == 1, nil
}

// verifyMFACode accepts either a fresh TOTP code or a recovery code.
// On recovery-code use the matching hash is removed from the stored list.
func (s *Service) verifyMFACode(ctx context.Context, userID, code string) (bool, error) {
	var secret sql.NullString
	var recovery sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT totp_secret, totp_recovery FROM users WHERE id = ? AND totp_verified = 1`, userID).
		Scan(&secret, &recovery)
	if err != nil {
		return false, err
	}
	if secret.Valid && totp.Validate(code, secret.String) {
		return true, nil
	}
	// Try recovery code.
	if recovery.Valid {
		var hashes []string
		if err := json.Unmarshal([]byte(recovery.String), &hashes); err == nil {
			target := hashRecoveryCode(strings.TrimSpace(code))
			for i, h := range hashes {
				if h == target {
					remaining := append(hashes[:i], hashes[i+1:]...)
					newJSON, _ := json.Marshal(remaining)
					_, _ = s.db.ExecContext(ctx,
						`UPDATE users SET totp_recovery = ? WHERE id = ?`,
						string(newJSON), userID)
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func hashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
