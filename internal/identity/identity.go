// Package identity defines the authenticated principal. OIDC subjects are only
// unique within an issuer, so the stable identity is the (issuer, subject) pair.
package identity

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxIssuerLen  = 2048
	MaxSubjectLen = 255
)

// Identity is a verified, issuer-qualified subject.
type Identity struct {
	Issuer  string
	Subject string
}

// Valid reports whether the identity is structurally usable.
func (i Identity) Valid() bool {
	return i.Issuer != "" && i.Subject != "" &&
		len(i.Issuer) <= MaxIssuerLen && len(i.Subject) <= MaxSubjectLen
}

// NormalizeIssuer trims trailing slashes to match the configured issuer form.
func NormalizeIssuer(issuer string) string {
	return strings.TrimRight(strings.TrimSpace(issuer), "/")
}

// ValidateSubject rejects blank, over-long, or control-character subjects.
func ValidateSubject(subject string) bool {
	if subject == "" || len(subject) > MaxSubjectLen || !utf8.ValidString(subject) {
		return false
	}
	if strings.TrimSpace(subject) != subject {
		return false
	}
	for _, r := range subject {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
