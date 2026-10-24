package organizations

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeName(t *testing.T) {
	got, err := NormalizeName("  Acme Roofing  ")
	if err != nil || got != "Acme Roofing" {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, want := range map[string]string{
		"":                       "name",
		"   ":                    "name",
		strings.Repeat("a", 201): "name",
		"bad\nname":              "name",
	} {
		_, err := NormalizeName(name)
		var v *ValidationError
		if !errors.As(err, &v) || v.Fields[want] == "" {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := NormalizeName(strings.Repeat("é", 200)); err != nil {
		t.Errorf("200 multibyte characters rejected: %v", err)
	}
}

func TestNormalizeIdentity(t *testing.T) {
	got, err := NormalizeIdentity(" https://idp.example.com/ ", " user-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Issuer != "https://idp.example.com" || got.Subject != "user-1" {
		t.Fatalf("got %+v", got)
	}
	cases := []struct{ issuer, subject, field string }{
		{"", "user", "issuer"},
		{"idp.example.com", "user", "issuer"},
		{"ftp://idp", "user", "issuer"},
		{"https://idp", "", "subject"},
		{"https://idp", "bad\nsubject", "subject"},
		{"https://idp", strings.Repeat("s", 256), "subject"},
	}
	for _, tc := range cases {
		_, err := NormalizeIdentity(tc.issuer, tc.subject)
		var v *ValidationError
		if !errors.As(err, &v) || v.Fields[tc.field] == "" {
			t.Errorf("%q/%q: %v", tc.issuer, tc.subject, err)
		}
	}
}

func TestPage(t *testing.T) {
	if limit, _, err := Page(0, ""); err != nil || limit != DefaultPageSize {
		t.Fatalf("default: %d %v", limit, err)
	}
	for _, bad := range []int{-1, MaxPageSize + 1} {
		if _, _, err := Page(bad, ""); !errors.Is(err, ErrValidation) {
			t.Errorf("limit %d: %v", bad, err)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 123, time.UTC)
	id := uuid.New()
	decoded, err := decodeCursor(encodeCursor(at, id))
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.CreatedAt.Equal(at) || decoded.ID != id {
		t.Fatalf("round trip = %+v", decoded)
	}
	for _, bad := range []string{"!!!", "Zm9v", "Zm9v~YmFy"} {
		if _, err := decodeCursor(bad); !errors.Is(err, ErrValidation) {
			t.Errorf("%q accepted", bad)
		}
	}
	if c, err := decodeCursor(""); err != nil || c != nil {
		t.Fatalf("empty cursor = %v %v", c, err)
	}
}

func TestParseRole(t *testing.T) {
	for _, role := range []string{"owner", "manager", "reviewer"} {
		if _, err := ParseRole(role); err != nil {
			t.Error(err)
		}
	}
	if _, err := ParseRole("admin"); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v", err)
	}
}
