package pagination

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPage(t *testing.T) {
	if limit, _, err := Page(0, ""); err != nil || limit != DefaultPageSize {
		t.Fatalf("default = %d %v", limit, err)
	}
	if _, _, err := Page(MaxPageSize+1, ""); err == nil {
		t.Fatal("oversize limit accepted")
	}
	if _, _, err := Page(1, "  "); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 25, 1, 2, 3, 4, time.UTC)
	id := uuid.New()
	cur, err := Decode(Encode(at, id))
	if err != nil || cur == nil {
		t.Fatal(err)
	}
	if !cur.CreatedAt.Equal(at) || cur.ID != id {
		t.Fatalf("round trip = %+v", cur)
	}
	if a, b := (*Cursor)(nil).Args(); a != nil || b != nil {
		t.Fatal("nil cursor args")
	}
	if _, err := Decode("!!!"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if cur, err := Decode(""); err != nil || cur != nil {
		t.Fatalf("empty = %v %v", cur, err)
	}
}
