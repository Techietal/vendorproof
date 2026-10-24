package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxJWKSBytes = 1 << 20

var (
	errUnknownKey = errors.New("token signing key is unknown")
	errStaleKeys  = errors.New("cached JWKS exceeded its stale-usage window")
)

const (
	defaultStaleKeyGrace = 15 * time.Minute
	defaultJWKSRetryBase = 5 * time.Second
	defaultJWKSRetryMax  = 5 * time.Minute
)

// keySet is an immutable snapshot of verified public keys.
type keySet map[string]any

// keyCache fetches and caches a JWKS.
//
// Cached keys are used until expiresAt, then for at most staleGrace longer
// while refreshes fail. Failed refreshes are retried with exponential backoff,
// so an outage cannot trigger one outbound request per incoming token. Fetches
// are single-flighted. After staleGrace a cached key is rejected even if the
// provider is still unreachable.
type keyCache struct {
	url         string
	client      *http.Client
	allowedAlgs map[string]bool
	minRefresh  time.Duration
	maxRefresh  time.Duration
	staleGrace  time.Duration
	retryBase   time.Duration
	retryMax    time.Duration
	now         func() time.Time

	fetchMu   sync.Mutex
	mu        sync.RWMutex
	keys      keySet
	lastFetch time.Time
	expiresAt time.Time
	nextRetry time.Time
	backoff   time.Duration
}

func newKeyCache(url string, client *http.Client, allowedAlgs map[string]bool, minRefresh, maxRefresh, staleGrace time.Duration, now func() time.Time) *keyCache {
	if staleGrace <= 0 {
		staleGrace = defaultStaleKeyGrace
	}
	return &keyCache{
		url: url, client: client, allowedAlgs: allowedAlgs,
		minRefresh: minRefresh, maxRefresh: maxRefresh, staleGrace: staleGrace,
		retryBase: defaultJWKSRetryBase, retryMax: defaultJWKSRetryMax, now: now,
	}
}

// key returns the key for kid. It refreshes when the cache is empty, expired,
// or does not contain kid, subject to backoff. A cached key remains usable
// only within expiry plus staleGrace.
func (c *keyCache) key(ctx context.Context, kid string) (any, error) {
	if kid == "" {
		return nil, errors.New("token has no kid header")
	}
	now := c.now()
	expired := c.isExpired(now)
	if expired {
		// Always attempt a normal rotation refresh; backoff makes this cheap
		// during an outage and stale keys keep verification working.
		fetchErr := c.refresh(ctx, false)
		if key, ok := c.lookupUsable(kid, now); ok {
			return key, nil
		}
		if c.hasKey(kid) {
			return nil, errStaleKeys
		}
		if fetchErr != nil {
			return nil, fmt.Errorf("refresh JWKS: %w", fetchErr)
		}
		return nil, errUnknownKey
	}
	if key, ok := c.lookupUsable(kid, now); ok {
		return key, nil
	}
	// Fresh cache, unknown kid: this may be a rotation.
	fetchErr := c.refresh(ctx, true)
	if key, ok := c.lookupUsable(kid, now); ok {
		return key, nil
	}
	if fetchErr != nil {
		return nil, fmt.Errorf("refresh JWKS: %w", fetchErr)
	}
	return nil, errUnknownKey
}

func (c *keyCache) isExpired(now time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !now.Before(c.expiresAt)
}

func (c *keyCache) lookupUsable(kid string, now time.Time) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok := c.keys[kid]
	if !ok || !now.Before(c.expiresAt.Add(c.staleGrace)) {
		return nil, false
	}
	return key, true
}

func (c *keyCache) hasKey(kid string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.keys[kid]
	return ok
}

// refresh fetches a new key set. forced controls whether the minimum interval
// applies. Backoff applies to every attempt after a failure.
func (c *keyCache) refresh(ctx context.Context, forced bool) error {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	now := c.now()
	c.mu.RLock()
	haveKeys := len(c.keys) > 0
	lastFetch := c.lastFetch
	nextRetry := c.nextRetry
	expired := !now.Before(c.expiresAt)
	c.mu.RUnlock()

	if haveKeys && !expired && !forced {
		return nil
	}
	if forced && !expired && now.Sub(lastFetch) < c.minRefresh {
		return nil
	}
	if now.Before(nextRetry) {
		return nil
	}

	keys, maxAge, err := c.fetch(ctx)
	if err != nil {
		c.mu.Lock()
		if c.backoff <= 0 {
			c.backoff = c.retryBase
		} else {
			c.backoff *= 2
			if c.backoff > c.retryMax {
				c.backoff = c.retryMax
			}
		}
		c.nextRetry = now.Add(c.backoff)
		c.mu.Unlock()
		return err
	}
	if maxAge <= 0 {
		maxAge = 5 * time.Minute
	}
	if maxAge < c.minRefresh {
		maxAge = c.minRefresh
	}
	if maxAge > c.maxRefresh {
		maxAge = c.maxRefresh
	}
	c.mu.Lock()
	c.keys = keys
	c.lastFetch = now
	c.expiresAt = now.Add(maxAge)
	c.backoff = 0
	c.nextRetry = time.Time{}
	c.mu.Unlock()
	return nil
}

func (c *keyCache) fetch(ctx context.Context) (keySet, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/jwk-set+json, application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJWKSBytes))
		return nil, 0, fmt.Errorf("JWKS endpoint returned %d", resp.StatusCode)
	}

	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes))
	if err := dec.Decode(&doc); err != nil {
		return nil, 0, fmt.Errorf("decode JWKS: %w", err)
	}

	keys := make(keySet, len(doc.Keys))
	for _, raw := range doc.Keys {
		kid, key, err := parseJWK(raw, c.allowedAlgs)
		if err != nil || kid == "" {
			continue // one unsupported key must not invalidate the set
		}
		if _, exists := keys[kid]; !exists {
			keys[kid] = key
		}
	}
	if len(keys) == 0 {
		return nil, 0, errors.New("JWKS contains no usable signing keys")
	}
	return keys, cacheMaxAge(resp.Header.Get("Cache-Control"), c.now()), nil
}

func cacheMaxAge(header string, now time.Time) time.Duration {
	for _, directive := range strings.Split(header, ",") {
		directive = strings.TrimSpace(directive)
		if v, ok := strings.CutPrefix(strings.ToLower(directive), "max-age="); ok {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return 5 * time.Minute
}

func parseJWK(raw json.RawMessage, allowed map[string]bool) (string, any, error) {
	var jwk struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return "", nil, err
	}
	if jwk.Use != "" && jwk.Use != "sig" {
		return "", nil, errors.New("key is not a signing key")
	}
	if jwk.Alg != "" && !allowed[jwk.Alg] {
		return "", nil, errors.New("key algorithm is not allowed")
	}

	switch jwk.Kty {
	case "RSA":
		n, err := b64uint(jwk.N)
		if err != nil {
			return "", nil, err
		}
		e, err := b64uint(jwk.E)
		if err != nil || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return "", nil, errors.New("invalid RSA exponent")
		}
		if n.BitLen() < 2048 {
			return "", nil, errors.New("RSA key is shorter than 2048 bits")
		}
		return jwk.Kid, &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch jwk.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return "", nil, errors.New("unsupported EC curve")
		}
		x, err := b64big(jwk.X)
		if err != nil {
			return "", nil, err
		}
		y, err := b64big(jwk.Y)
		if err != nil {
			return "", nil, err
		}
		if !curve.IsOnCurve(x, y) {
			return "", nil, errors.New("EC key is not on its curve")
		}
		return jwk.Kid, &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	case "OKP":
		if jwk.Crv != "Ed25519" {
			return "", nil, errors.New("unsupported OKP curve")
		}
		raw, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return "", nil, errors.New("invalid Ed25519 key")
		}
		return jwk.Kid, ed25519.PublicKey(raw), nil
	default:
		return "", nil, errors.New("unsupported key type")
	}
}

func b64uint(s string) (*big.Int, error) {
	if s == "" {
		return nil, errors.New("missing integer component")
	}
	return b64big(s)
}

func b64big(s string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("invalid base64url integer")
	}
	return new(big.Int).SetBytes(raw), nil
}

// keyMatchesAlgorithm enforces key-type/algorithm pairing in addition to the
// parser's valid-methods list.
func keyMatchesAlgorithm(alg string, key any) bool {
	if strings.HasPrefix(alg, "ES") {
		ec, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return false
		}
		want := map[string]elliptic.Curve{"ES256": elliptic.P256(), "ES384": elliptic.P384(), "ES512": elliptic.P521()}[alg]
		return want != nil && ec.Curve == want
	}
	if strings.HasPrefix(alg, "RS") || strings.HasPrefix(alg, "PS") {
		_, ok := key.(*rsa.PublicKey)
		return ok
	}
	if alg == "EdDSA" {
		_, ok := key.(ed25519.PublicKey)
		return ok
	}
	return false
}
