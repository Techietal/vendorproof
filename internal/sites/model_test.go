package sites

import (
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	if got, err := NormalizeName("  Factory A "); err != nil || got != "Factory A" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := NormalizeName("   "); err == nil {
		t.Fatal("blank accepted")
	}
	if _, err := NormalizeName("A\nB"); err == nil {
		t.Fatal("control accepted")
	}
	if _, err := NormalizeName(strings.Repeat("x", 201)); err == nil {
		t.Fatal("overlength accepted")
	}
	if _, err := NormalizeName(strings.Repeat("é", 200)); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNameField(t *testing.T) {
	if _, err := ValidateNameField(nil); err == nil {
		t.Fatal("null accepted")
	}
	blank := " "
	if _, err := ValidateNameField(&blank); err == nil {
		t.Fatal("blank accepted")
	}
	name := " Warehouse C "
	if got, err := ValidateNameField(&name); err != nil || got != "Warehouse C" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFilterNormalize(t *testing.T) {
	f := Filter{Search: "  " + strings.Repeat("x", 201)}
	if err := f.Normalize(); err == nil {
		t.Fatal("long search accepted")
	}
	f = Filter{Search: " Factory "}
	if err := f.Normalize(); err != nil || f.Search != "Factory" {
		t.Fatalf("search = %q err %v", f.Search, err)
	}
}
