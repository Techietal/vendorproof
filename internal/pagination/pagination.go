// Package pagination provides bounded keyset paging shared by list endpoints.
// Cursors are anchored to a timestamp plus a UUID tie-breaker so rows created
// in the same instant cannot be duplicated or skipped between pages.
package pagination

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/apperr"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
	maxCursorLen    = 512
)

// Cursor is one opaque page anchor.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// Page normalizes and validates paging inputs.
func Page(limit int, cursor string) (int, string, error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return 0, "", apperr.Validation("limit", fmt.Sprintf("must be between 1 and %d", MaxPageSize))
	}
	cursor = strings.TrimSpace(cursor)
	if len(cursor) > maxCursorLen {
		return 0, "", apperr.Validation("cursor", "is too long")
	}
	return limit, cursor, nil
}

// Encode serializes an anchor.
func Encode(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Decode parses an opaque anchor.
func Decode(raw string) (*Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, apperr.Validation("cursor", "is invalid")
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 {
		return nil, apperr.Validation("cursor", "is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, apperr.Validation("cursor", "is invalid")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, apperr.Validation("cursor", "is invalid")
	}
	return &Cursor{CreatedAt: createdAt, ID: id}, nil
}

// Args returns the nullable anchor arguments used by SQL.
func (c *Cursor) Args() (any, any) {
	if c == nil {
		return nil, nil
	}
	return c.CreatedAt, c.ID
}
