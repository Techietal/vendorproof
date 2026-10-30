package requirements

import (
	"testing"
	"time"
)

func TestParseCadence(t *testing.T) {
	tests := []struct {
		input    string
		wantErr  bool
		expected Cadence
	}{
		{"monthly", false, CadenceMonthly},
		{"expiry", false, CadenceExpiry},
		{"annual", true, Cadence("")},
		{"", true, Cadence("")},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseCadence(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCadence(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("ParseCadence(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestPublishInputNormalize(t *testing.T) {
	t.Run("valid monthly", func(t *testing.T) {
		dug := 15
		input := PublishInput{
			Name:              "Attendance Register",
			Cadence:           "monthly",
			DueDay:            &dug,
			EffectiveFrom:     "2026-01-01",
			ReportingTimezone: "UTC",
		}
		result, err := input.Normalize()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Name != "Attendance Register" {
			t.Errorf("Name = %q", result.Name)
		}
		if result.Cadence != CadenceMonthly {
			t.Errorf("Cadence = %v", result.Cadence)
		}
		if result.DueDay != 15 {
			t.Errorf("DueDay = %d", result.DueDay)
		}
		if result.Loc.String() != "UTC" {
			t.Errorf("Loc = %v", result.Loc)
		}
	})

	t.Run("valid expiry without due day", func(t *testing.T) {
		input := PublishInput{
			Name:              "One-off Requirement",
			Cadence:           "expiry",
			EffectiveFrom:     "2026-01-01",
			ReportingTimezone: "America/New_York",
		}
		result, err := input.Normalize()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.DueDay != 0 {
			t.Errorf("DueDay = %d, want 0 for expiry", result.DueDay)
		}
	})

	t.Run("missing timezone", func(t *testing.T) {
		dug := 15
		input := PublishInput{
			Name:              "Test",
			Cadence:           "monthly",
			DueDay:            &dug,
			EffectiveFrom:     "2026-01-01",
			ReportingTimezone: "",
		}
		_, err := input.Normalize()
		if err == nil {
			t.Fatal("expected error for missing timezone")
		}
	})

	t.Run("monthly missing due day", func(t *testing.T) {
		input := PublishInput{
			Name:              "Test",
			Cadence:           "monthly",
			EffectiveFrom:     "2026-01-01",
			ReportingTimezone: "UTC",
		}
		_, err := input.Normalize()
		if err == nil {
			t.Fatal("expected error for missing due day")
		}
	})

	t.Run("effective until before effective from", func(t *testing.T) {
		dug := 15
		input := PublishInput{
			Name:              "Test",
			Cadence:           "monthly",
			DueDay:            &dug,
			EffectiveFrom:     "2026-06-01",
			EffectiveUntil:    strPtr("2026-01-01"),
			ReportingTimezone: "UTC",
		}
		_, err := input.Normalize()
		if err == nil {
			t.Fatal("expected error for invalid effective_until")
		}
	})
}

func TestNormalizedPublishEffectiveFrom(t *testing.T) {
	dug := 15
	input := PublishInput{
		Name:              "Test",
		Cadence:           "monthly",
		DueDay:            &dug,
		EffectiveFrom:     "2026-01-15",
		ReportingTimezone: "UTC",
	}
	result, err := input.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	// Effective from should be normalized to midnight UTC
	expected := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if !result.EffectiveFrom.Equal(expected) {
		t.Errorf("EffectiveFrom = %v, want %v", result.EffectiveFrom, expected)
	}
}

func strPtr(s string) *string {
	return &s
}
