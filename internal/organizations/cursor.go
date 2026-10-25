package organizations

import (
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/pagination"
)

type cursor = pagination.Cursor

func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	return pagination.Encode(createdAt, id)
}

func decodeCursor(raw string) (*cursor, error) {
	return pagination.Decode(raw)
}

func nextCursor(createdAt time.Time, id uuid.UUID) string {
	return pagination.Encode(createdAt, id)
}
