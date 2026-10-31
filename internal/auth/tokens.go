package auth

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenKind names what a signed token is for. Every kind has its own HMAC
// key and audience, so a token minted for one purpose can't be replayed as
// another (an MFA-pending token as an access token, a WS ticket as a
// refresh token, ...).
type TokenKind string

const (
	KindAccess      TokenKind = "access"
	KindRefresh     TokenKind = "refresh"
	KindMFAPending  TokenKind = "mfa-pending"
	KindWSTicket    TokenKind = "ws-ticket"
	KindOIDCState   TokenKind = "oidc-state"
	KindSAMLState   TokenKind = "saml-state"
	KindOAuth2State TokenKind = "oauth2-state"
)

var tokenKinds = []TokenKind{
	KindAccess, KindRefresh, KindMFAPending, KindWSTicket,
	KindOIDCState, KindSAMLState, KindOAuth2State,
}

const tokenIssuer = "dockmesh"

func audience(kind TokenKind) string { return "dockmesh:" + string(kind) }

// TypedClaims is embedded by every claims struct signed with TokenKeys.
type TypedClaims struct {
	Typ TokenKind `json:"typ"`
	jwt.RegisteredClaims
}

func (c *TypedClaims) typed() *TypedClaims { return c }

type typedToken interface {
	jwt.Claims
	typed() *TypedClaims
}

// TokenKeys holds one HMAC key per token kind, all derived from the
// installation's secret, so no new key material has to be stored.
type TokenKeys struct {
	keys map[TokenKind][]byte
}

func NewTokenKeys(secret []byte) (*TokenKeys, error) {
	if len(secret) < 32 {
		return nil, errors.New("token secret must be at least 32 bytes")
	}
	k := &TokenKeys{keys: make(map[TokenKind][]byte, len(tokenKinds))}
	for _, kind := range tokenKinds {
		key, err := hkdf.Key(sha256.New, secret, nil, "dockmesh token key: "+string(kind), 32)
		if err != nil {
			return nil, err
		}
		k.keys[kind] = key
	}
	return k, nil
}

// Sign stamps typ, aud, iss and iat on claims and signs them with kind's
// key. ExpiresAt is set from ttl unless the caller already set it.
func (k *TokenKeys) Sign(kind TokenKind, ttl time.Duration, claims typedToken) (string, error) {
	key, ok := k.keys[kind]
	if !ok {
		return "", fmt.Errorf("unknown token kind %q", kind)
	}
	now := time.Now()
	tc := claims.typed()
	tc.Typ = kind
	tc.Issuer = tokenIssuer
	tc.Audience = jwt.ClaimStrings{audience(kind)}
	tc.IssuedAt = jwt.NewNumericDate(now)
	if tc.ExpiresAt == nil {
		tc.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
}

// Parse verifies token as kind into claims. Anything signed for another
// kind, with another key, or without typ/aud/exp fails with ErrInvalidToken.
func (k *TokenKeys) Parse(kind TokenKind, token string, claims typedToken) error {
	key, ok := k.keys[kind]
	if !ok {
		return fmt.Errorf("unknown token kind %q", kind)
	}
	_, err := jwt.ParseWithClaims(token, claims,
		func(*jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(audience(kind)),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if claims.typed().Typ != kind {
		return ErrInvalidToken
	}
	return nil
}

// isPreUpgradeRefresh reports whether token looks like a refresh token from
// before typed tokens: the old format had sub "refresh" and no typ claim.
// The signature is deliberately not checked; the answer only picks the
// error message for a token that is rejected either way.
func isPreUpgradeRefresh(token string) bool {
	var c struct {
		Typ string `json:"typ"`
		jwt.RegisteredClaims
	}
	if _, _, err := jwt.NewParser().ParseUnverified(token, &c); err != nil {
		return false
	}
	return c.Typ == "" && c.Subject == "refresh"
}
