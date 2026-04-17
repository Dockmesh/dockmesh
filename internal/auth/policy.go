// Password policy + account lockout config (P.12.1). All settings are
// read from the runtime settings store so admins can edit via the UI
// without a restart. Defaults match the status quo (8-char minimum,
// no character-class requirements, no lockout) so existing installs
// don't have their users locked out on the first post-upgrade login.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode"
)

// Setting keys.
const (
	PolicyMinLengthKey      = "auth.password_min_length"
	PolicyRequireUpperKey   = "auth.password_require_upper"
	PolicyRequireLowerKey   = "auth.password_require_lower"
	PolicyRequireDigitKey   = "auth.password_require_digit"
	PolicyRequireSymbolKey  = "auth.password_require_symbol"
	PolicyRotationDaysKey   = "auth.password_rotation_days"   // 0 = disabled
	LockoutMaxAttemptsKey   = "auth.lockout_max_attempts"     // 0 = disabled
	LockoutDurationMinsKey  = "auth.lockout_duration_minutes" // 0 = disabled
)

// PolicyConfig is the typed snapshot of the settings above.
type PolicyConfig struct {
	MinLength            int `json:"min_length"`
	RequireUpper         bool `json:"require_upper"`
	RequireLower         bool `json:"require_lower"`
	RequireDigit         bool `json:"require_digit"`
	RequireSymbol        bool `json:"require_symbol"`
	RotationDays         int `json:"rotation_days"`
	LockoutMaxAttempts   int `json:"lockout_max_attempts"`
	LockoutDurationMins  int `json:"lockout_duration_minutes"`
}

// SettingsReader is the minimal interface we need from settings.Store.
// Kept tiny so auth doesn't import the settings package (avoids a
// cycle with middleware.RBACStore which lives elsewhere).
type SettingsReader interface {
	Get(key, def string) string
	Set(ctx context.Context, key, value string) error
}

// LoadPolicy reads the current policy snapshot. Cheap — just reads
// the settings cache.
func LoadPolicy(s SettingsReader) PolicyConfig {
	return PolicyConfig{
		MinLength:           intSetting(s, PolicyMinLengthKey, 8),
		RequireUpper:        boolSetting(s, PolicyRequireUpperKey, false),
		RequireLower:        boolSetting(s, PolicyRequireLowerKey, false),
		RequireDigit:        boolSetting(s, PolicyRequireDigitKey, false),
		RequireSymbol:       boolSetting(s, PolicyRequireSymbolKey, false),
		RotationDays:        intSetting(s, PolicyRotationDaysKey, 0),
		LockoutMaxAttempts:  intSetting(s, LockoutMaxAttemptsKey, 5),
		LockoutDurationMins: intSetting(s, LockoutDurationMinsKey, 15),
	}
}

// SavePolicy persists a full policy snapshot through the settings
// store. Validation here is the one place we enforce "don't shoot
// yourself in the foot" rules.
func SavePolicy(ctx context.Context, s SettingsReader, p PolicyConfig) error {
	if p.MinLength < 1 || p.MinLength > 256 {
		return errors.New("min_length must be 1..256")
	}
	if p.RotationDays < 0 {
		return errors.New("rotation_days must be >= 0")
	}
	if p.LockoutMaxAttempts < 0 || p.LockoutDurationMins < 0 {
		return errors.New("lockout values must be >= 0")
	}
	set := func(k, v string) error { return s.Set(ctx, k, v) }
	if err := set(PolicyMinLengthKey, strconv.Itoa(p.MinLength)); err != nil {
		return err
	}
	if err := set(PolicyRequireUpperKey, boolStr(p.RequireUpper)); err != nil {
		return err
	}
	if err := set(PolicyRequireLowerKey, boolStr(p.RequireLower)); err != nil {
		return err
	}
	if err := set(PolicyRequireDigitKey, boolStr(p.RequireDigit)); err != nil {
		return err
	}
	if err := set(PolicyRequireSymbolKey, boolStr(p.RequireSymbol)); err != nil {
		return err
	}
	if err := set(PolicyRotationDaysKey, strconv.Itoa(p.RotationDays)); err != nil {
		return err
	}
	if err := set(LockoutMaxAttemptsKey, strconv.Itoa(p.LockoutMaxAttempts)); err != nil {
		return err
	}
	return set(LockoutDurationMinsKey, strconv.Itoa(p.LockoutDurationMins))
}

// ValidatePassword enforces the configured policy. Called from
// CreateUser and ChangePassword — NOT from Login (an existing user
// with a now-too-short password doesn't get locked out retroactively;
// they just won't be able to change TO another too-short one).
func ValidatePassword(p PolicyConfig, password string) error {
	if len(password) < p.MinLength {
		return fmt.Errorf("password must be at least %d characters", p.MinLength)
	}
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r):
			hasSymbol = true
		}
	}
	if p.RequireUpper && !hasUpper {
		return errors.New("password must contain an uppercase letter")
	}
	if p.RequireLower && !hasLower {
		return errors.New("password must contain a lowercase letter")
	}
	if p.RequireDigit && !hasDigit {
		return errors.New("password must contain a digit")
	}
	if p.RequireSymbol && !hasSymbol {
		return errors.New("password must contain a symbol (punctuation, whitespace, or other non-alphanumeric)")
	}
	return nil
}

// PasswordRotationOverdue reports whether a user's password is older
// than the configured rotation window. Returns (overdue, daysOver).
// If the feature is disabled or the user's password_changed_at is
// NULL (unknown baseline), returns (false, 0) — we don't force-change
// on users we've never seen change one.
func PasswordRotationOverdue(p PolicyConfig, changedAt *time.Time) (bool, int) {
	if p.RotationDays <= 0 || changedAt == nil {
		return false, 0
	}
	window := time.Duration(p.RotationDays) * 24 * time.Hour
	age := time.Since(*changedAt)
	if age < window {
		return false, 0
	}
	over := int((age - window) / (24 * time.Hour))
	return true, over
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func intSetting(s SettingsReader, key string, def int) int {
	v := s.Get(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func boolSetting(s SettingsReader, key string, def bool) bool {
	v := s.Get(key, "")
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
