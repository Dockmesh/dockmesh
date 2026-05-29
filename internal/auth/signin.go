// Sign-in flow settings — surface area for the "Sessions & sign-in"
// section of the Authentication page. Settings are persisted via the
// generic settings.Store (key/value rows), same pattern as
// PolicyConfig in policy.go.
package auth

import (
	"context"
	"errors"
	"strconv"
)

// Setting keys for the sign-in flow.
const (
	AllowLocalPasswordKey       = "auth.allow_local_password"         // 1 = local pw login enabled
	AllowSelfRegisterKey        = "auth.allow_self_register"          // 1 = self-serve signup
	AutoCreateOnSSOKey          = "auth.auto_create_on_sso"           // 1 = create user on first SSO login
	RequireTFAForAdminKey       = "auth.require_tfa_for_admin"        // 1 = admins MUST have TOTP
	SessionIdleTTLMinKey        = "auth.session_idle_ttl_min"         // minutes; 0 = no idle expiry
	SessionAbsoluteTTLHrKey     = "auth.session_absolute_ttl_hr"      // hours; hard cap on refresh chain
	SessionRememberMeDaysKey    = "auth.session_remember_me_days"     // days; "stay signed in" duration
	SessionMaxPerUserKey        = "auth.session_max_per_user"         // cap on concurrent active sessions per user; 0 = unlimited
	PasswordForbidReuseCountKey = "auth.password_forbid_reuse_count"  // N most-recent disallowed
)

// SignInConfig is the typed snapshot of the sign-in-flow settings.
type SignInConfig struct {
	AllowLocalPassword       bool `json:"allow_local_password"`
	AllowSelfRegister        bool `json:"allow_self_register"`
	AutoCreateOnSSO          bool `json:"auto_create_on_sso"`
	RequireTFAForAdmin       bool `json:"require_tfa_for_admin"`
	SessionIdleTTLMin        int  `json:"session_idle_ttl_min"`
	SessionAbsoluteTTLHr     int  `json:"session_absolute_ttl_hr"`
	SessionRememberMeDays    int  `json:"session_remember_me_days"`
	SessionMaxPerUser        int  `json:"session_max_per_user"`
	PasswordForbidReuseCount int  `json:"password_forbid_reuse_count"`
}

// LoadSignInConfig reads the snapshot. Defaults are conservative —
// local password ON (so a fresh install still has a way in), SSO
// auto-create ON (matches existing OIDC behaviour), and forbid-reuse
// OFF (the feature requires a password-history table we haven't built).
func LoadSignInConfig(s SettingsReader) SignInConfig {
	return SignInConfig{
		AllowLocalPassword:       boolSetting(s, AllowLocalPasswordKey, true),
		AllowSelfRegister:        boolSetting(s, AllowSelfRegisterKey, false),
		AutoCreateOnSSO:          boolSetting(s, AutoCreateOnSSOKey, true),
		RequireTFAForAdmin:       boolSetting(s, RequireTFAForAdminKey, false),
		SessionIdleTTLMin:        intSetting(s, SessionIdleTTLMinKey, 60),
		SessionAbsoluteTTLHr:     intSetting(s, SessionAbsoluteTTLHrKey, 24),
		SessionRememberMeDays:    intSetting(s, SessionRememberMeDaysKey, 14),
		SessionMaxPerUser:        intSetting(s, SessionMaxPerUserKey, 20),
		PasswordForbidReuseCount: intSetting(s, PasswordForbidReuseCountKey, 0),
	}
}

// SaveSignInConfig persists a full snapshot, validating the numbers.
func SaveSignInConfig(ctx context.Context, s SettingsReader, c SignInConfig) error {
	if c.SessionIdleTTLMin < 0 || c.SessionIdleTTLMin > 24*60 {
		return errors.New("session_idle_ttl_min must be 0..1440")
	}
	if c.SessionAbsoluteTTLHr < 1 || c.SessionAbsoluteTTLHr > 24*30 {
		return errors.New("session_absolute_ttl_hr must be 1..720")
	}
	if c.SessionRememberMeDays < 0 || c.SessionRememberMeDays > 365 {
		return errors.New("session_remember_me_days must be 0..365")
	}
	if c.SessionMaxPerUser < 0 || c.SessionMaxPerUser > 200 {
		return errors.New("session_max_per_user must be 0..200 (0 = unlimited)")
	}
	if c.PasswordForbidReuseCount < 0 || c.PasswordForbidReuseCount > 24 {
		return errors.New("password_forbid_reuse_count must be 0..24")
	}
	set := func(k, v string) error { return s.Set(ctx, k, v) }
	if err := set(AllowLocalPasswordKey, boolStr(c.AllowLocalPassword)); err != nil {
		return err
	}
	if err := set(AllowSelfRegisterKey, boolStr(c.AllowSelfRegister)); err != nil {
		return err
	}
	if err := set(AutoCreateOnSSOKey, boolStr(c.AutoCreateOnSSO)); err != nil {
		return err
	}
	if err := set(RequireTFAForAdminKey, boolStr(c.RequireTFAForAdmin)); err != nil {
		return err
	}
	if err := set(SessionIdleTTLMinKey, strconv.Itoa(c.SessionIdleTTLMin)); err != nil {
		return err
	}
	if err := set(SessionAbsoluteTTLHrKey, strconv.Itoa(c.SessionAbsoluteTTLHr)); err != nil {
		return err
	}
	if err := set(SessionRememberMeDaysKey, strconv.Itoa(c.SessionRememberMeDays)); err != nil {
		return err
	}
	if err := set(SessionMaxPerUserKey, strconv.Itoa(c.SessionMaxPerUser)); err != nil {
		return err
	}
	return set(PasswordForbidReuseCountKey, strconv.Itoa(c.PasswordForbidReuseCount))
}
