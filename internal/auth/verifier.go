package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"vendorproof/internal/config"
	"vendorproof/internal/identity"
)

// ErrInvalidToken is returned for every authentication failure. The concrete
// reason is wrapped for logs but is safe to expose at debug level only.
var ErrInvalidToken = errors.New("invalid access token")

// Verifier validates access tokens from one trusted OIDC issuer.
type Verifier struct {
	issuer      string
	audience    string
	requiredTyp string
	allowedAlgs map[string]bool
	keys        *keyCache
	parser      *jwt.Parser
	now         func() time.Time
}

// NewVerifier builds a verifier with a bounded, caching JWKS client.
func NewVerifier(cfg config.OIDC) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.Audience == "" || cfg.JWKSURL == "" {
		return nil, errors.New("issuer, audience, and JWKS URL are required")
	}
	if len(cfg.AllowedAlgorithms) == 0 {
		return nil, errors.New("at least one signing algorithm is required")
	}
	allowed := make(map[string]bool, len(cfg.AllowedAlgorithms))
	for _, alg := range cfg.AllowedAlgorithms {
		allowed[alg] = true
	}
	client := &http.Client{Timeout: cfg.JWKSFetchTimeout}
	now := time.Now
	v := &Verifier{
		issuer:      identity.NormalizeIssuer(cfg.Issuer),
		audience:    cfg.Audience,
		requiredTyp: cfg.RequiredTokenType,
		allowedAlgs: allowed,
		keys:        newKeyCache(cfg.JWKSURL, client, allowed, cfg.MinJWKSRefresh, cfg.MaxJWKSRefresh, now),
		now:         now,
	}
	v.parser = jwt.NewParser(
		jwt.WithValidMethods(cfg.AllowedAlgorithms),
		jwt.WithAudience(cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(now),
	)
	return v, nil
}

// Verify checks a raw bearer token and returns the issuer-qualified identity.
func (v *Verifier) Verify(ctx context.Context, raw string) (identity.Identity, error) {
	claims := jwt.MapClaims{}
	token, err := v.parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		alg := token.Method.Alg()
		if !v.allowedAlgs[alg] {
			return nil, fmt.Errorf("algorithm %q is not allowed", alg)
		}
		if typ, ok := token.Header["typ"].(string); ok {
			if !v.tokenTypeAllowed(typ) {
				return nil, fmt.Errorf("token type %q is not accepted", typ)
			}
		} else if v.requiredTyp != "" && v.requiredTyp != "any" {
			return nil, errors.New("token has no typ header")
		}
		kid, _ := token.Header["kid"].(string)
		key, err := v.keys.key(ctx, kid)
		if err != nil {
			return nil, err
		}
		if !keyMatchesAlgorithm(alg, key) {
			return nil, errors.New("signing key does not match algorithm")
		}
		return key, nil
	})
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if !token.Valid {
		return identity.Identity{}, fmt.Errorf("%w: token is not valid", ErrInvalidToken)
	}

	issuer, err := claims.GetIssuer()
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	subject, err := claims.GetSubject()
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if identity.NormalizeIssuer(issuer) != v.issuer {
		return identity.Identity{}, fmt.Errorf("%w: issuer mismatch", ErrInvalidToken)
	}
	if !identity.ValidateSubject(subject) {
		return identity.Identity{}, fmt.Errorf("%w: invalid subject claim", ErrInvalidToken)
	}
	return identity.Identity{Issuer: v.issuer, Subject: subject}, nil
}

func (v *Verifier) tokenTypeAllowed(typ string) bool {
	switch v.requiredTyp {
	case "", "any":
		return true
	case "Bearer", "JWT":
		return strings.EqualFold(typ, v.requiredTyp)
	case "at+jwt", "application/at+jwt":
		return typ == "at+jwt" || typ == "application/at+jwt"
	default:
		return typ == v.requiredTyp
	}
}
