package assignments

import (
	"testing"

	"github.com/google/uuid"

	"vendorproof/internal/validate"
)

func TestCreateInputNormalize(t *testing.T) {
	engagementID := uuid.New()
	versionID := uuid.New()

	tests := []struct {
		name    string
		input   CreateInput
		wantErr bool
	}{
		{
			name: "valid active assignment",
			input: CreateInput{
				EngagementID:         engagementID.String(),
				RequirementVersionID: versionID.String(),
				EffectiveFrom:        "2026-01-01",
			},
			wantErr: false,
		},
		{
			name: "valid with effective until",
			input: CreateInput{
				EngagementID:         engagementID.String(),
				RequirementVersionID: versionID.String(),
				EffectiveFrom:        "2026-01-01",
				EffectiveUntil:       strPtr("2026-06-01"),
			},
			wantErr: false,
		},
		{
			name: "invalid engagement id",
			input: CreateInput{
				EngagementID:         "invalid",
				RequirementVersionID: versionID.String(),
				EffectiveFrom:        "2026-01-01",
			},
			wantErr: true,
		},
		{
			name: "invalid version id",
			input: CreateInput{
				EngagementID:         engagementID.String(),
				RequirementVersionID: "invalid",
				EffectiveFrom:        "2026-01-01",
			},
			wantErr: true,
		},
		{
			name: "effective until before effective from",
			input: CreateInput{
				EngagementID:         engagementID.String(),
				RequirementVersionID: versionID.String(),
				EffectiveFrom:        "2026-06-01",
				EffectiveUntil:       strPtr("2026-01-01"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.input.Normalize()
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if result.EngagementID != engagementID {
					t.Errorf("EngagementID = %v, want %v", result.EngagementID, engagementID)
				}
			}
		})
	}
}

func TestTransitionInputNormalize(t *testing.T) {
	versionID := uuid.New()

	t.Run("valid", func(t *testing.T) {
		input := TransitionInput{
			RequirementVersionID: versionID.String(),
			EffectiveFrom:        "2026-01-01",
		}
		result, err := input.Normalize()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequirementVersionID != versionID {
			t.Errorf("RequirementVersionID = %v, want %v", result.RequirementVersionID, versionID)
		}
	})

	t.Run("invalid version id", func(t *testing.T) {
		input := TransitionInput{
			RequirementVersionID: "invalid",
			EffectiveFrom:        "2026-01-01",
		}
		_, err := input.Normalize()
		if err == nil {
			t.Fatal("expected error for invalid version id")
		}
	})

	t.Run("invalid date", func(t *testing.T) {
		input := TransitionInput{
			RequirementVersionID: versionID.String(),
			EffectiveFrom:        "not-a-date",
		}
		_, err := input.Normalize()
		if err == nil {
			t.Fatal("expected error for invalid date")
		}
	})
}

func TestNormalizeDate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid date", "2026-01-15", false},
		{"invalid format", "01-15-2026", true},
		{"empty", "", true},
		{"invalid month", "2026-13-01", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validate.Date("effectiveFrom", tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Date() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func strPtr(s string) *string {
	return &s
}
