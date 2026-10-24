// Package testjwks runs a real local JWKS endpoint and mints signed tokens.
// It exercises the production verifier over HTTP rather than stubbing it.
package testjwks

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Signer is a development identity provider for tests.
type Signer struct {
	Server *httptest.Server
	Issuer string

	mu      sync.Mutex
	key     *rsa.PrivateKey
	kid     string
	served  map[string]*rsa.PublicKey
	fetches int
}

// New starts a JWKS server with a fresh RSA key. Cleanup is automatic.
func New(t testing.TB) *Signer {
	t.Helper()
	s := &Signer{served: map[string]*rsa.PublicKey{}}
	s.rotate()
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	s.Issuer = s.Server.URL
	t.Cleanup(s.Server.Close)
	return s
}

func (s *Signer) handle(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	keys := make([]map[string]string, 0, len(s.served))
	for kid, pub := range s.served {
		keys = append(keys, map[string]string{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=1")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

func (s *Signer) rotate() {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(der)
	s.key = key
	s.kid = hex.EncodeToString(sum[:8])
	s.served[s.kid] = &key.PublicKey
}

// URL returns the JWKS URL.
func (s *Signer) URL() string { return s.Server.URL + "/jwks" }

// Kid returns the current key ID.
func (s *Signer) Kid() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kid
}

// CurrentKey returns the active signing key.
func (s *Signer) CurrentKey() *rsa.PrivateKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key
}

// Rotate replaces the served key set with a new key.
func (s *Signer) Rotate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.served = map[string]*rsa.PublicKey{}
	s.rotate()
}

// RemoveServed drops the current key from the JWKS without changing the
// signing key.
func (s *Signer) RemoveServed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.served = map[string]*rsa.PublicKey{}
}

// Fetches reports how many JWKS requests have been served.
func (s *Signer) Fetches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches
}

// Claims builds a valid claim set.
func Claims(subject string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": "", "aud": "vendorproof-api", "sub": subject,
		"iat": now.Unix(), "nbf": now.Add(-10 * time.Second).Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	}
}

// Sign mints a valid RS256 access token for the issuer.
func (s *Signer) Sign(claims jwt.MapClaims) string {
	if claims["iss"] == "" || claims["iss"] == nil {
		claims["iss"] = s.Issuer
	}
	s.mu.Lock()
	key, kid := s.key, s.kid
	s.mu.Unlock()
	return s.SignWith(claims, key, kid, "RS256", "at+jwt")
}

// SignAs mints a token with explicit signing key details.
func (s *Signer) SignAs(claims jwt.MapClaims, key *rsa.PrivateKey, kid, typ string) string {
	if claims["iss"] == "" || claims["iss"] == nil {
		claims["iss"] = s.Issuer
	}
	return s.SignWith(claims, key, kid, "RS256", typ)
}

// SignWith mints a token with a chosen algorithm.
func (s *Signer) SignWith(claims jwt.MapClaims, key *rsa.PrivateKey, kid, alg, typ string) string {
	tok := jwt.NewWithClaims(jwt.GetSigningMethod(alg), claims)
	tok.Header["kid"] = kid
	if typ != "" {
		tok.Header["typ"] = typ
	}
	signed, err := tok.SignedString(key)
	if err != nil {
		panic(err)
	}
	return signed
}

// NewUnservedKey returns a key that the JWKS endpoint does not publish.
func NewUnservedKey(t testing.TB) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(der)
	return key, hex.EncodeToString(sum[:8]) + "-unserved"
}
