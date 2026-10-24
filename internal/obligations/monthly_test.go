package obligations

import (
	"errors"
	"testing"
	"time"
)

func location(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
func rule(loc *time.Location) MonthlyRule {
	return MonthlyRule{
		OrganizationID: "org-a", EngagementID: "engagement-1", RequirementVersionID: "req-v1",
		EffectiveFrom: time.Date(2025, 1, 1, 0, 0, 0, 0, loc), DueDay: 31,
	}
}
func TestGenerateMonthlyClampsDueDateAndPreservesPeriod(t *testing.T) {
	loc := location(t)
	got, err := GenerateMonthly(rule(loc), 2026, time.January, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got.PeriodStart.Format("2006-01-02") != "2026-01-01" || got.PeriodEnd.Format("2006-01-02") != "2026-02-01" {
		t.Fatalf("wrong period: %+v", got)
	}
	if got.DueAt.In(loc).Format("2006-01-02 15:04") != "2026-02-28 17:00" {
		t.Fatalf("wrong due date: %s", got.DueAt.In(loc))
	}
	if got.IdempotencyKey != "org-a/engagement-1/req-v1/2026-01" {
		t.Fatalf("wrong key: %q", got.IdempotencyKey)
	}
}
func TestLeapYearAndYearBoundary(t *testing.T) {
	loc := location(t)
	leap, err := GenerateMonthly(rule(loc), 2028, time.January, loc)
	if err != nil {
		t.Fatal(err)
	}
	if leap.DueAt.In(loc).Format("2006-01-02") != "2028-02-29" {
		t.Fatalf("wrong leap-year date: %s", leap.DueAt)
	}
	rollover, err := GenerateMonthly(rule(loc), 2026, time.December, loc)
	if err != nil {
		t.Fatal(err)
	}
	if rollover.DueAt.In(loc).Format("2006-01-02") != "2027-01-31" {
		t.Fatalf("wrong year rollover: %s", rollover.DueAt)
	}
}
func TestInactiveAndBoundaryRules(t *testing.T) {
	loc := location(t)
	r := rule(loc)
	r.EffectiveFrom = time.Date(2026, 2, 1, 0, 0, 0, 0, loc)
	if _, err := GenerateMonthly(r, 2026, time.January, loc); !errors.Is(err, ErrInactiveRule) {
		t.Fatalf("expected inactive: %v", err)
	}
	r.EffectiveFrom = time.Date(2026, 1, 15, 0, 0, 0, 0, loc)
	until := time.Date(2026, 2, 1, 0, 0, 0, 0, loc)
	r.EffectiveUntil = &until
	if _, err := GenerateMonthly(r, 2026, time.January, loc); err != nil {
		t.Fatalf("expected active: %v", err)
	}
	if _, err := GenerateMonthly(r, 2026, time.February, loc); !errors.Is(err, ErrInactiveRule) {
		t.Fatalf("expected inactive: %v", err)
	}
}
func TestInvalidRules(t *testing.T) {
	loc := location(t)
	for _, invalid := range []int{0, 32} {
		r := rule(loc)
		r.DueDay = invalid
		if _, err := GenerateMonthly(r, 2026, time.October, loc); err == nil {
			t.Errorf("dueDay=%d should fail", invalid)
		}
	}
	if _, err := GenerateMonthly(rule(loc), 2026, time.Month(13), loc); err == nil {
		t.Fatal("invalid month accepted")
	}
}
func TestInternalReviewIsNeverContractorOverdue(t *testing.T) {
	due := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := due.Add(24 * time.Hour)
	for _, tc := range []struct {
		state ReviewState
		want  bool
	}{
		{AwaitingSubmission, true}, {AwaitingReview, false}, {NeedsCorrection, true}, {Accepted, false},
	} {
		got, err := Status(tc.state, due, now)
		if err != nil {
			t.Fatal(err)
		}
		if got.ContractorOverdue != tc.want {
			t.Errorf("%s overdue=%v", tc.state, got.ContractorOverdue)
		}
	}
	exactlyDue, err := Status(AwaitingSubmission, due, due)
	if err != nil || exactlyDue.ContractorOverdue {
		t.Fatalf("incorrect boundary status: %+v %v", exactlyDue, err)
	}
}
