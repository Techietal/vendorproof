// Package validate normalizes and checks shared business-input shapes.
package validate

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"vendorproof/internal/apperr"
)

const (
	MaxNameLen  = 200
	MaxEmailLen = 320
	maxLocalLen = 64
	DateLayout  = "2006-01-02"
)

// Name trims and validates a display name used by sites and contractors.
func Name(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", apperr.Validation("name", "must not be blank")
	}
	if utf8.RuneCountInString(name) > MaxNameLen {
		return "", apperr.Validation("name", "must be at most 200 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", apperr.Validation("name", "must not contain control characters")
		}
	}
	return name, nil
}

// Email trims and validates an optional contact address. The domain is
// lowercased; the local part keeps its case (local-part case equivalence is
// not assumed). An empty value means "not supplied".
func Email(raw string) (*string, error) {
	email := strings.TrimSpace(raw)
	if email == "" {
		return nil, nil
	}
	if len(email) > MaxEmailLen {
		return nil, apperr.Validation("contactEmail", "must be at most 320 bytes")
	}
	for _, r := range email {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return nil, apperr.Validation("contactEmail", "must not contain spaces or control characters")
		}
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at != strings.LastIndexByte(email, '@') {
		return nil, apperr.Validation("contactEmail", "must contain exactly one @")
	}
	local, domain := email[:at], email[at+1:]
	if len(local) > maxLocalLen {
		return nil, apperr.Validation("contactEmail", "local part is too long")
	}
	if domain == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") ||
		strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return nil, apperr.Validation("contactEmail", "domain is invalid")
	}
	tld := domain[strings.LastIndexByte(domain, '.')+1:]
	if len(tld) < 2 {
		return nil, apperr.Validation("contactEmail", "domain is invalid")
	}
	normalized := local + "@" + strings.ToLower(domain)
	return &normalized, nil
}

// Date parses an exact YYYY-MM-DD calendar date without timezone conversion.
func Date(field, raw string) (time.Time, error) {
	date, err := time.Parse(DateLayout, raw)
	if err != nil || raw != date.Format(DateLayout) {
		return time.Time{}, apperr.Validation(field, "must be a valid YYYY-MM-DD date")
	}
	return date, nil
}

// LoadLocation validates an IANA timezone identifier.
func LoadLocation(raw string) (*time.Location, error) {
	if raw == "" {
		return nil, apperr.Validation("timezone", "must not be blank")
	}
	loc, err := time.LoadLocation(raw)
	if err != nil {
		return nil, apperr.Validation("timezone", "must be a valid IANA timezone")
	}
	return loc, nil
}

// Today returns the documented server-side UTC calendar date.
func Today(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Search trims an optional name filter and bounds it.
func Search(raw string) (string, error) {
	search := strings.TrimSpace(raw)
	if utf8.RuneCountInString(search) > MaxNameLen {
		return "", apperr.Validation("q", "must be at most 200 characters")
	}
	return search, nil
}

// LikePattern escapes LIKE metacharacters in a search term.
func LikePattern(term string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(term) + "%"
}
