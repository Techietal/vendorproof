package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr      = "127.0.0.1:8080"
	defaultMaxBodyBytes  = 64 << 10
	defaultRequestTimout = 15 * time.Second
)

// Config is the fully validated runtime configuration. It is built once at
// process start; handlers never read environment variables directly.
type Config struct {
	HTTPAddr        string
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration
	MaxBodyBytes    int64
	LogLevel        slog.Level
	Database        Database
	OIDC            OIDC
}

// Database holds PostgreSQL pool and readiness settings.
type Database struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	ConnectTimeout  time.Duration
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	HealthTimeout   time.Duration
}

// OIDC identifies the trusted token issuer and how its tokens are verified.
type OIDC struct {
	Issuer            string
	Audience          string
	JWKSURL           string
	AllowedAlgorithms []string
	RequiredTokenType string
	MinJWKSRefresh    time.Duration
	MaxJWKSRefresh    time.Duration
	JWKSFetchTimeout  time.Duration
}

// Load reads and validates configuration from the environment.
func Load() (Config, error) {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	httpAddr := strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if httpAddr == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			port = "8080"
		}
		httpAddr = "127.0.0.1:" + port
	}

	cfg := Config{
		HTTPAddr:        httpAddr,
		ShutdownTimeout: durationEnv("SHUTDOWN_TIMEOUT", 15*time.Second, fail),
		RequestTimeout:  durationEnv("REQUEST_TIMEOUT", defaultRequestTimout, fail),
		MaxBodyBytes:    int64(intEnv("MAX_BODY_BYTES", defaultMaxBodyBytes, fail)),
		LogLevel:        levelEnv(fail),
		Database: Database{
			URL:             strings.TrimSpace(os.Getenv("DATABASE_URL")),
			MaxConns:        int32(intEnv("DB_MAX_CONNS", 10, fail)),
			MinConns:        int32(intEnv("DB_MIN_CONNS", 2, fail)),
			ConnectTimeout:  durationEnv("DB_CONNECT_TIMEOUT", 5*time.Second, fail),
			MaxConnLifetime: durationEnv("DB_MAX_CONN_LIFETIME", 30*time.Minute, fail),
			MaxConnIdleTime: durationEnv("DB_MAX_CONN_IDLE_TIME", 5*time.Minute, fail),
			HealthTimeout:   durationEnv("DB_HEALTH_TIMEOUT", 2*time.Second, fail),
		},
		OIDC: OIDC{
			Issuer:            strings.TrimRight(strings.TrimSpace(os.Getenv("OIDC_ISSUER")), "/"),
			Audience:          strings.TrimSpace(os.Getenv("OIDC_AUDIENCE")),
			JWKSURL:           strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")),
			AllowedAlgorithms: csvEnv("OIDC_ALLOWED_ALGORITHMS", []string{"RS256", "ES256"}),
			RequiredTokenType: strings.TrimSpace(os.Getenv("OIDC_REQUIRED_TOKEN_TYPE")),
			MinJWKSRefresh:    durationEnv("OIDC_MIN_JWKS_REFRESH", time.Minute, fail),
			MaxJWKSRefresh:    durationEnv("OIDC_MAX_JWKS_REFRESH", time.Hour, fail),
			JWKSFetchTimeout:  durationEnv("OIDC_JWKS_TIMEOUT", 5*time.Second, fail),
		},
	}
	if cfg.OIDC.RequiredTokenType == "" {
		cfg.OIDC.RequiredTokenType = "at+jwt"
	}

	if cfg.Database.URL == "" {
		fail("DATABASE_URL is required")
	}
	if cfg.OIDC.Issuer == "" {
		fail("OIDC_ISSUER is required")
	}
	if cfg.OIDC.Audience == "" {
		fail("OIDC_AUDIENCE is required")
	}
	if cfg.OIDC.JWKSURL == "" {
		// An OIDC discovery document would be the alternative; requiring the
		// JWKS URL explicitly avoids silently trusting discovery metadata.
		fail("OIDC_JWKS_URL is required")
	}

	if cfg.Database.URL != "" {
		if err := validatePostgresURL(cfg.Database.URL); err != nil {
			fail("DATABASE_URL: %v", err)
		}
	}
	if cfg.OIDC.Issuer != "" {
		if err := validateHTTPURL(cfg.OIDC.Issuer); err != nil {
			fail("OIDC_ISSUER: %v", err)
		}
	}
	if cfg.OIDC.JWKSURL != "" {
		if err := validateHTTPURL(cfg.OIDC.JWKSURL); err != nil {
			fail("OIDC_JWKS_URL: %v", err)
		}
	}
	if cfg.OIDC.Audience != "" && len(cfg.OIDC.Audience) > 512 {
		fail("OIDC_AUDIENCE must be at most 512 characters")
	}

	for _, alg := range cfg.OIDC.AllowedAlgorithms {
		if !supportedAlgorithm(alg) {
			fail("OIDC_ALLOWED_ALGORITHMS: unsupported algorithm %q", alg)
		}
	}
	if len(cfg.OIDC.AllowedAlgorithms) == 0 {
		fail("OIDC_ALLOWED_ALGORITHMS cannot be empty")
	}
	switch cfg.OIDC.RequiredTokenType {
	case "at+jwt", "application/at+jwt", "JWT", "Bearer", "any":
	default:
		fail("OIDC_REQUIRED_TOKEN_TYPE %q is not supported", cfg.OIDC.RequiredTokenType)
	}

	if cfg.Database.MaxConns < 1 {
		fail("DB_MAX_CONNS must be at least 1")
	}
	if cfg.Database.MinConns < 0 || cfg.Database.MinConns > cfg.Database.MaxConns {
		fail("DB_MIN_CONNS must be between 0 and DB_MAX_CONNS")
	}
	if cfg.MaxBodyBytes < 1 {
		fail("MAX_BODY_BYTES must be positive")
	}
	if cfg.OIDC.MinJWKSRefresh <= 0 || cfg.OIDC.MaxJWKSRefresh < cfg.OIDC.MinJWKSRefresh {
		fail("OIDC JWKS refresh bounds are invalid")
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func durationEnv(name string, def time.Duration, fail func(string, ...any)) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		fail("%s must be a positive duration (for example 10s)", name)
		return def
	}
	return d
}

func intEnv(name string, def int, fail func(string, ...any)) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		fail("%s must be an integer", name)
		return def
	}
	return n
}

func levelEnv(fail func(string, ...any)) slog.Level {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	switch raw {
	case "", "info":
		return slog.LevelInfo
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		fail("LOG_LEVEL must be debug, info, warn, or error")
		return slog.LevelInfo
	}
}

func csvEnv(name string, def []string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func validatePostgresURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return errors.New("scheme must be postgres or postgresql")
	}
	if u.Host == "" {
		return errors.New("host is required")
	}
	if strings.Trim(u.Path, "/") == "" {
		return errors.New("database name is required")
	}
	return nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("host is required")
	}
	if u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("must not contain credentials, query, or fragment")
	}
	return nil
}

func supportedAlgorithm(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA":
		return true
	default:
		return false
	}
}
