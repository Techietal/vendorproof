// Command devidp is a local, development-only OIDC-style token issuer. It
// signs real RS256 access tokens and serves a real JWKS so the API always
// verifies signatures; there is no authentication bypass. Not for production.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const defaultKeyFile = ".dev/idp-key.pem"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devidp:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: devidp <serve|token> [flags]")
	}
	switch os.Args[1] {
	case "serve":
		return serve(os.Args[2:])
	case "token":
		return token(os.Args[2:])
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:9000", "listen address")
	issuer := fs.String("issuer", "", "issuer URL (default http://<addr>)")
	keyFile := fs.String("key-file", defaultKeyFile, "signing key file (created if absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *issuer == "" {
		*issuer = "http://" + *addr
	}
	key, err := loadOrCreateKey(*keyFile)
	if err != nil {
		return err
	}
	kid := keyID(&key.PublicKey)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}})
	})
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": *issuer, "jwks_uri": *issuer + "/.well-known/jwks.json",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	fmt.Printf("DEV ONLY. issuer=%s jwks=%s/.well-known/jwks.json\n", *issuer, *issuer)
	fmt.Printf("set OIDC_ISSUER=%s OIDC_JWKS_URL=%s/.well-known/jwks.json\n", *issuer, *issuer)
	return http.ListenAndServe(*addr, mux)
}

func token(args []string) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	issuer := fs.String("issuer", "http://127.0.0.1:9000", "issuer URL")
	audience := fs.String("audience", "vendorproof-api", "audience")
	subject := fs.String("subject", "", "subject claim (required)")
	role := fs.String("role", "owner", "local role claim (informational)")
	ttl := fs.Duration("ttl", time.Hour, "token lifetime")
	typ := fs.String("typ", "at+jwt", `JOSE typ header; use "JWT" to mint an ID-token-style token`)
	keyFile := fs.String("key-file", defaultKeyFile, "signing key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *subject == "" {
		return errors.New("--subject is required")
	}
	key, err := loadOrCreateKey(*keyFile)
	if err != nil {
		return err
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": *issuer, "aud": *audience, "sub": *subject, "role": *role,
		"iat": now.Unix(), "nbf": now.Add(-5 * time.Second).Unix(), "exp": now.Add(*ttl).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = keyID(&key.PublicKey)
	tok.Header["typ"] = *typ
	signed, err := tok.SignedString(key)
	if err != nil {
		return err
	}
	fmt.Println(signed)
	return nil
}

func loadOrCreateKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, errors.New("key file is not PEM")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("key file is not RSA")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "generated development key at", path)
	return key, nil
}

func keyID(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:8])
}
