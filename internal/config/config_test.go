package config

import (
	"strings"
	"testing"
	"time"
)

func setValid(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv("OIDC_ISSUER", "https://idp.example.com/")
	t.Setenv("OIDC_AUDIENCE", "vendorproof-api")
	t.Setenv("OIDC_JWKS_URL", "https://idp.example.com/.well-known/jwks.json")
}

func TestLoadValid(t *testing.T) {
	setValid(t)
	t.Setenv("DB_MAX_CONNS", "7")
	t.Setenv("OIDC_ALLOWED_ALGORITHMS", "RS256, ES256")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf("HTTPAddr = %q, want loopback default", cfg.HTTPAddr)
	}
	if cfg.OIDC.Issuer != "https://idp.example.com" {
		t.Errorf("issuer not normalized: %q", cfg.OIDC.Issuer)
	}
	if cfg.Database.MaxConns != 7 || cfg.OIDC.RequiredTokenType != "at+jwt" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.RequestTimeout != 15*time.Second || cfg.MaxBodyBytes != 64<<10 {
		t.Errorf("unexpected limits: %+v", cfg)
	}
}

func TestLoadPortCompatibility(t *testing.T) {
	setValid(t)
	t.Setenv("PORT", "9090")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]func(*testing.T){
		"missing database": func(t *testing.T) { t.Setenv("DATABASE_URL", "") },
		"bad database scheme": func(t *testing.T) {
			t.Setenv("DATABASE_URL", "mysql://u:p@localhost/db")
		},
		"missing issuer": func(t *testing.T) { t.Setenv("OIDC_ISSUER", "") },
		"issuer not URL": func(t *testing.T) { t.Setenv("OIDC_ISSUER", "idp") },
		"issuer with query": func(t *testing.T) {
			t.Setenv("OIDC_ISSUER", "https://idp.example.com?x=1")
		},
		"missing audience": func(t *testing.T) { t.Setenv("OIDC_AUDIENCE", "") },
		"missing jwks":     func(t *testing.T) { t.Setenv("OIDC_JWKS_URL", "") },
		"bad jwks scheme":  func(t *testing.T) { t.Setenv("OIDC_JWKS_URL", "ftp://x/y") },
		"bad algorithm":    func(t *testing.T) { t.Setenv("OIDC_ALLOWED_ALGORITHMS", "HS256") },
		"empty algorithms": func(t *testing.T) { t.Setenv("OIDC_ALLOWED_ALGORITHMS", ",,") },
		"bad duration":     func(t *testing.T) { t.Setenv("REQUEST_TIMEOUT", "fast") },
		"bad body limit":   func(t *testing.T) { t.Setenv("MAX_BODY_BYTES", "-1") },
		"bad log level":    func(t *testing.T) { t.Setenv("LOG_LEVEL", "loud") },
		"min conns above max": func(t *testing.T) {
			t.Setenv("DB_MAX_CONNS", "2")
			t.Setenv("DB_MIN_CONNS", "3")
		},
		"bad token type": func(t *testing.T) { t.Setenv("OIDC_REQUIRED_TOKEN_TYPE", "id+token") },
		"refresh bounds inverted": func(t *testing.T) {
			t.Setenv("OIDC_MIN_JWKS_REFRESH", "2h")
			t.Setenv("OIDC_MAX_JWKS_REFRESH", "1h")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			setValid(t)
			mutate(t)
			if _, err := Load(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadAggregatesErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("OIDC_JWKS_URL", "")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"DATABASE_URL", "OIDC_ISSUER", "OIDC_AUDIENCE", "OIDC_JWKS_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %s: %v", want, err)
		}
	}
}
