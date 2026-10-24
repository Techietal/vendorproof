package identity

import "testing"

func TestValidateSubject(t *testing.T) {
	for _, valid := range []string{"user-1", "auth0|abc", "a b"} {
		if !ValidateSubject(valid) {
			t.Errorf("%q rejected", valid)
		}
	}
	for _, invalid := range []string{"", " leading", "trailing ", "line\nbreak", "\t"} {
		if ValidateSubject(invalid) {
			t.Errorf("%q accepted", invalid)
		}
	}
}

func TestNormalizeIssuerAndValid(t *testing.T) {
	if got := NormalizeIssuer(" https://idp/ "); got != "https://idp" {
		t.Fatalf("got %q", got)
	}
	if !(Identity{Issuer: "https://idp", Subject: "s"}).Valid() {
		t.Fatal("valid identity rejected")
	}
	if (Identity{Issuer: "", Subject: "s"}).Valid() {
		t.Fatal("blank issuer accepted")
	}
}
