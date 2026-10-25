package contractors

import (
	"testing"

	"vendorproof/internal/optional"
)

func TestNormalizePatch(t *testing.T) {
	if _, err := (Patch{}).NormalizePatch(); err == nil {
		t.Fatal("empty patch accepted")
	}
	if _, err := (Patch{Name: optional.Value[string]{Set: true}}).NormalizePatch(); err == nil {
		t.Fatal("null name accepted")
	}
	name := "  ABC Security "
	p, err := (Patch{Name: optional.Value[string]{Set: true, Value: &name}}).NormalizePatch()
	if err != nil || *p.Name.Value != "ABC Security" {
		t.Fatalf("name = %v %v", p.Name.Value, err)
	}

	p, err = (Patch{ContactEmail: optional.Value[string]{Set: true}}).NormalizePatch()
	if err != nil || p.ContactEmail.Value != nil {
		t.Fatalf("null email = %+v %v", p, err)
	}
	blank := "   "
	p, err = (Patch{ContactEmail: optional.Value[string]{Set: true, Value: &blank}}).NormalizePatch()
	if err != nil || p.ContactEmail.Value != nil {
		t.Fatalf("blank email = %+v %v", p, err)
	}
	bad := "not-an-email"
	if _, err := (Patch{ContactEmail: optional.Value[string]{Set: true, Value: &bad}}).NormalizePatch(); err == nil {
		t.Fatal("invalid email accepted")
	}
	email := "x@Example.com"
	p, err = (Patch{ContactEmail: optional.Value[string]{Set: true, Value: &email}}).NormalizePatch()
	if err != nil || *p.ContactEmail.Value != "x@example.com" {
		t.Fatalf("email = %v %v", p.ContactEmail.Value, err)
	}
}
