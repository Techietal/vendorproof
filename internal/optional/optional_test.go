package optional

import (
	"encoding/json"
	"testing"
)

func TestPresenceAndNull(t *testing.T) {
	var v struct {
		Name  Value[string] `json:"name"`
		Email Value[string] `json:"email"`
	}
	if err := json.Unmarshal([]byte(`{"name":"A","email":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Name.Set || v.Name.Value == nil || *v.Name.Value != "A" {
		t.Fatalf("name = %+v", v.Name)
	}
	if !v.Email.Set || v.Email.Value != nil {
		t.Fatalf("email = %+v", v.Email)
	}
}

func TestOmittedStaysUnset(t *testing.T) {
	var v struct {
		Name Value[string] `json:"name"`
	}
	if err := json.Unmarshal([]byte(`{}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Name.Set {
		t.Fatal("omitted field marked set")
	}
}

func TestTypeMismatch(t *testing.T) {
	var v struct {
		Name Value[string] `json:"name"`
	}
	if err := json.Unmarshal([]byte(`{"name":5}`), &v); err == nil {
		t.Fatal("number accepted as string")
	}
}
