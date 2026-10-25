package validate

import (
	"strings"
	"testing"
	"time"

	"vendorproof/internal/apperr"
)

func TestName(t *testing.T) {
	if got, err := Name("  Factory A  "); err != nil || got != "Factory A" {
		t.Fatalf("got %q err %v", got, err)
	}
	if got, err := Name("Équipe – Ünit 1"); err != nil || got == "" {
		t.Fatalf("unicode rejected: %q %v", got, err)
	}
	for name, raw := range map[string]string{"empty": "", "spaces": "   ", "control": "A\nB", "nul": "A\x00B"} {
		if _, err := Name(raw); !errIsValidation(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Name(strings.Repeat("é", 200)); err != nil {
		t.Fatalf("200 rune name rejected: %v", err)
	}
	if _, err := Name(strings.Repeat("é", 201)); !errIsValidation(err) {
		t.Fatalf("201 rune name accepted: %v", err)
	}
}

func TestEmail(t *testing.T) {
	email, err := Email(" Contact@Example.COM ")
	if err != nil || email == nil || *email != "Contact@example.com" {
		t.Fatalf("got %v err %v", email, err)
	}
	// Local-part case is preserved deliberately.
	if email, _ := Email("Mixed.Case@example.com"); email == nil || *email != "Mixed.Case@example.com" {
		t.Fatalf("local part changed: %v", email)
	}
	if email, err := Email("  "); err != nil || email != nil {
		t.Fatalf("blank = %v %v", email, err)
	}
	for _, raw := range []string{
		"plain", "@example.com", "a@", "a@@b.com", "a b@example.com", "a@example",
		"a@.example.com", "a@example..com", "a@" + strings.Repeat("x", 400) + ".com",
	} {
		if _, err := Email(raw); !errIsValidation(err) {
			t.Errorf("%q accepted (%v)", raw, err)
		}
	}
}

func TestDate(t *testing.T) {
	got, err := Date("activeFrom", "2026-02-28")
	if err != nil || got.Format(DateLayout) != "2026-02-28" {
		t.Fatalf("got %v err %v", got, err)
	}
	for _, raw := range []string{"2026-02-30", "2026-2-3", "2026-02-03T00:00:00Z", "", "not-a-date"} {
		if _, err := Date("activeFrom", raw); !errIsValidation(err) {
			t.Errorf("%q accepted (%v)", raw, err)
		}
	}
}

func TestTodayIsUTC(t *testing.T) {
	loc := time.FixedZone("X", 14*3600)
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, loc) // 2026-10-24 11:00 UTC
	if got := Today(now).Format(DateLayout); got != "2026-10-24" {
		t.Fatalf("today = %s", got)
	}
}

func TestLikePatternEscapes(t *testing.T) {
	if got := LikePattern("a%b_c\\d"); got != `%a\%b\_c\\d%` {
		t.Fatalf("pattern = %q", got)
	}
}

func errIsValidation(err error) bool {
	var invalid *apperr.ValidationError
	return err != nil && (asValidation(err, &invalid) || err == apperr.ErrValidation)
}

func asValidation(err error, target **apperr.ValidationError) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*apperr.ValidationError); ok {
		*target = e
		return true
	}
	return false
}
