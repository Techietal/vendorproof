package engagements

import (
	"testing"
	"time"

	"vendorproof/internal/optional"
)

func date(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func ptr(d time.Time) *time.Time { return &d }

func TestStateAtBoundaries(t *testing.T) {
	from := date("2026-01-01")
	until := date("2026-06-01")
	cases := []struct {
		name  string
		eval  time.Time
		until *time.Time
		want  State
	}{
		{"before", from.AddDate(0, 0, -1), &until, Scheduled},
		{"on start", from, &until, Active},
		{"inside", date("2026-03-01"), &until, Active},
		{"day before end", until.AddDate(0, 0, -1), &until, Active},
		{"on exclusive end", until, &until, Ended},
		{"after end", until.AddDate(0, 0, 1), &until, Ended},
		{"open before", from.AddDate(0, 0, -1), nil, Scheduled},
		{"open on start", from, nil, Active},
		{"open later", date("2030-01-01"), nil, Active},
	}
	for _, tc := range cases {
		if got := StateAt(tc.eval, from, tc.until); got != tc.want {
			t.Errorf("%s: %s", tc.name, got)
		}
	}
}

func TestValidatePeriod(t *testing.T) {
	from := date("2026-01-01")
	if err := ValidatePeriod(from, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePeriod(from, ptr(date("2026-01-02"))); err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"2026-01-01", "2025-12-31"} {
		if err := ValidatePeriod(from, ptr(date(end))); err == nil {
			t.Errorf("end %s accepted", end)
		}
	}
}

func TestParseState(t *testing.T) {
	for _, raw := range []string{"scheduled", "active", "ended"} {
		if _, err := ParseState(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if state, err := ParseState(""); err != nil || state != nil {
		t.Fatalf("empty = %v %v", state, err)
	}
	if _, err := ParseState("current"); err == nil {
		t.Fatal("invalid state accepted")
	}
}

func TestNormalizePatch(t *testing.T) {
	if _, err := (Patch{}).NormalizePatch(); err == nil {
		t.Fatal("empty patch accepted")
	}
	p, err := Patch{ActiveUntil: opt(nil)}.NormalizePatch()
	if err != nil || !p.ActiveUntil.Set || p.ActiveUntil.Value != nil {
		t.Fatalf("null end = %+v %v", p, err)
	}
	if _, err := (Patch{ActiveFrom: opt(str("2026-02-30"))}).NormalizePatch(); err == nil {
		t.Fatal("impossible date accepted")
	}
}

func str(s string) *string { return &s }

func opt(v *string) optional.Value[string] { return optional.Value[string]{Set: true, Value: v} }
