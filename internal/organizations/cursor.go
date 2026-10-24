package organizations

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
)

type cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(raw string) (*cursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, validationErr("cursor", "is invalid")
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 {
		return nil, validationErr("cursor", "is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, validationErr("cursor", "is invalid")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, validationErr("cursor", "is invalid")
	}
	return &cursor{CreatedAt: createdAt, ID: id}, nil
}

func (c *cursor) args() (any, any) {
	if c == nil {
		return nil, nil
	}
	return c.CreatedAt, c.ID
}

func nextCursor(createdAt time.Time, id uuid.UUID) string {
	return encodeCursor(createdAt, id)
}
