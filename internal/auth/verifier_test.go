package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"vendorproof/internal/config"
	"vendorproof/internal/testutil/testjwks"
)

const audience = "vendorproof-api"

func newTestVerifier(t *testing.T, signer *testjwks.Signer, mutate func(*config.OIDC)) *Verifier {
	t.Helper()
	cfg := config.OIDC{
		Issuer: signer.Issuer, Audience: audience, JWKSURL: signer.URL(),
		AllowedAlgorithms: []string{"RS256"}, RequiredTokenType: "at+jwt",
		MinJWKSRefresh: 5 * time.Millisecond, MaxJWKSRefresh: 50 * time.Millisecond,
		JWKSFetchTimeout: 2 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerifyValid(t *testing.T) {
	signer := testjwks.New(t)
	v := newTestVerifier(t, signer, nil)
	token := signer.Sign(testjwks.Claims("user-1"))
	got, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Issuer != signer.Issuer || got.Subject != "user-1" {
		t.Fatalf("identity = %+v", got)
	}
	if signer.Fetches() != 1 {
		t.Fatalf("fetches = %d, want 1", signer.Fetches())
	}
	// A second verification must use the cache.
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if signer.Fetches() != 1 {
		t.Fatalf("cache miss: fetches = %d", signer.Fetches())
	}
}

func TestVerifyAcceptsNormalizedIssuerAndAlternateATJWTType(t *testing.T) {
	signer := testjwks.New(t)
	v := newTestVerifier(t, signer, nil)
	claims := testjwks.Claims("user-1")
	claims["iss"] = signer.Issuer + "/"
	token := signer.SignWith(claims, signer.CurrentKey(), signer.Kid(), "RS256", "application/at+jwt")
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejects(t *testing.T) {
	signer := testjwks.New(t)
	unserved, unservedKid := testjwks.NewUnservedKey(t)

	cases := map[string]func() string{
		"malformed": func() string { return "not-a-jwt" },
		"unsigned algorithm": func() string {
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, testjwks.Claims("user"))
			tok.Header["kid"] = signer.Kid()
			s, _ := tok.SignedString([]byte("secret"))
			return s
		},
		"expired": func() string {
			c := testjwks.Claims("user")
			c["exp"] = time.Now().Add(-5 * time.Minute).Unix()
			return signer.Sign(c)
		},
		"not yet valid": func() string {
			c := testjwks.Claims("user")
			c["nbf"] = time.Now().Add(5 * time.Minute).Unix()
			return signer.Sign(c)
		},
		"missing expiration": func() string {
			c := testjwks.Claims("user")
			delete(c, "exp")
			return signer.Sign(c)
		},
		"wrong audience": func() string {
			c := testjwks.Claims("user")
			c["aud"] = "another-api"
			return signer.Sign(c)
		},
		"audience array without match": func() string {
			c := testjwks.Claims("user")
			c["aud"] = []string{"a", "b"}
			return signer.Sign(c)
		},
		"wrong issuer": func() string {
			c := testjwks.Claims("user")
			c["iss"] = "https://evil.example.com"
			return signer.Sign(c)
		},
		"missing subject": func() string {
			c := testjwks.Claims("")
			return signer.Sign(c)
		},
		"control character subject": func() string {
			c := testjwks.Claims("user\nname")
			return signer.Sign(c)
		},
		"invalid signature": func() string {
			return signer.SignAs(testjwks.Claims("user"), unserved, signer.Kid(), "at+jwt")
		},
		"unknown key": func() string {
			return signer.SignAs(testjwks.Claims("user"), unserved, unservedKid, "at+jwt")
		},
		"rs512 when only rs256 allowed": func() string {
			return signer.SignWith(testjwks.Claims("user"), signer.CurrentKey(), signer.Kid(), "RS512", "at+jwt")
		},
		"id-token style typ": func() string {
			return signer.SignAs(testjwks.Claims("user"), signer.CurrentKey(), signer.Kid(), "JWT")
		},
		"missing typ": func() string {
			claims := testjwks.Claims("user")
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			tok.Header["kid"] = signer.Kid()
			s, _ := tok.SignedString(signer.CurrentKey())
			return s
		},
	}
	for name, mint := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := newTestVerifier(t, signer, nil).Verify(context.Background(), mint())
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestTypCanBeDisabled(t *testing.T) {
	signer := testjwks.New(t)
	v := newTestVerifier(t, signer, func(c *config.OIDC) { c.RequiredTokenType = "any" })
	token := signer.SignAs(testjwks.Claims("user"), signer.CurrentKey(), signer.Kid(), "")
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
}

func TestKeyRotation(t *testing.T) {
	signer := testjwks.New(t)
	v := newTestVerifier(t, signer, nil)
	oldToken := signer.Sign(testjwks.Claims("user"))
	if _, err := v.Verify(context.Background(), oldToken); err != nil {
		t.Fatal(err)
	}

	signer.Rotate()
	newToken := signer.Sign(testjwks.Claims("user"))
	// Unknown-kid refreshes are rate limited to minJWKSRefresh.
	time.Sleep(15 * time.Millisecond)
	if _, err := v.Verify(context.Background(), newToken); err != nil {
		t.Fatalf("new key after rotation: %v", err)
	}
	if _, err := v.Verify(context.Background(), oldToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("rotated-out key still accepted: %v", err)
	}
}

func TestCachedKeysSurviveProviderOutage(t *testing.T) {
	signer := testjwks.New(t)
	v := newTestVerifier(t, signer, func(c *config.OIDC) { c.MaxJWKSRefresh = time.Hour })
	token := signer.Sign(testjwks.Claims("user"))
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	signer.Server.Close()
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("cached key unusable during outage: %v", err)
	}
}

func TestColdCacheWithProviderDown(t *testing.T) {
	signer := testjwks.New(t)
	v := newTestVerifier(t, signer, nil)
	token := signer.Sign(testjwks.Claims("user"))
	signer.Server.Close()
	if _, err := v.Verify(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestExpiredCacheRefreshes(t *testing.T) {
	signer := testjwks.New(t)
	v := newTestVerifier(t, signer, func(c *config.OIDC) { c.MaxJWKSRefresh = 20 * time.Millisecond })
	token := signer.Sign(testjwks.Claims("user"))
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if got := signer.Fetches(); got < 2 {
		t.Fatalf("expired cache did not refresh: fetches = %d", got)
	}
}

func TestNewVerifierValidation(t *testing.T) {
	if _, err := NewVerifier(config.OIDC{}); err == nil {
		t.Fatal("empty config accepted")
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestOutageBackoffAndBoundedStaleness(t *testing.T) {
	signer := testjwks.New(t)
	clock := &fakeClock{t: time.Now()}
	cfg := config.OIDC{
		Issuer: signer.Issuer, Audience: audience, JWKSURL: signer.URL(),
		AllowedAlgorithms: []string{"RS256"}, RequiredTokenType: "at+jwt",
		MinJWKSRefresh: time.Millisecond, MaxJWKSRefresh: 20 * time.Millisecond,
		StaleKeyGrace: time.Minute, JWKSFetchTimeout: time.Second,
	}
	v, err := NewVerifierWithClock(cfg, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the outage backoff long enough to remain active during the test.
	v.keys.retryBase = 10 * time.Minute
	v.keys.retryMax = 10 * time.Minute

	claims := testjwks.Claims("user")
	claims["exp"] = clock.Now().Add(2 * time.Hour).Unix()
	token := signer.Sign(claims)
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if signer.Fetches() != 1 {
		t.Fatalf("initial fetches = %d", signer.Fetches())
	}

	clock.Advance(30 * time.Millisecond) // cache expired
	signer.Fail(true)

	// Concurrent requests after expiry must produce exactly one fetch attempt
	// while continuing to verify with bounded stale keys.
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Verify(context.Background(), token)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("stale verification failed: %v", err)
		}
	}
	if got := signer.Fetches(); got != 2 {
		t.Fatalf("fetches during outage = %d, want 1 attempt", got)
	}

	// Beyond the stale window, verification fails; backoff still suppresses
	// one fetch per token.
	clock.Advance(61 * time.Second)
	for i := 0; i < 5; i++ {
		if _, err := v.Verify(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("stale key accepted: %v", err)
		}
	}
	if got := signer.Fetches(); got != 2 {
		t.Fatalf("fetches after stale expiry = %d, want 2", got)
	}

	// Recovery after backoff refreshes and resets the window.
	clock.Advance(10 * time.Minute)
	signer.Fail(false)
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if got := signer.Fetches(); got != 3 {
		t.Fatalf("fetches after recovery = %d, want 3", got)
	}
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
}
