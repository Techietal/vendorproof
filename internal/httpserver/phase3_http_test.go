package httpserver_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"vendorproof/internal/authz"
)

func TestPhase3RequirementsCRUD(t *testing.T) {
	h := newHarness(t, 1<<20)

	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	reviewer := "reviewer"
	h.addAs(orgID, owner, reviewer, authz.Reviewer)

	managerToken := h.token(manager)
	reviewerToken := h.token(reviewer)

	// Reviewer cannot create requirements
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirements", orgID), reviewerToken, map[string]any{
		"name": "Attendance", "cadence": "monthly", "effectiveFrom": "2026-01-01", "timezone": "UTC", "dueDay": 15,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reviewer create: status=%d", resp.StatusCode)
	}

	// Manager creates a requirement family
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirements", orgID), managerToken, map[string]any{
		"name": "Attendance Register", "cadence": "monthly", "effectiveFrom": "2026-01-01", "timezone": "UTC", "dueDay": 15,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("manager create requirement: status=%d", resp.StatusCode)
	}
	created := decode(t, resp)
	requirementKey := uuid.MustParse(created["requirementKey"].(string))

	// List requirements
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/requirements", orgID), managerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list requirements: status=%d", resp.StatusCode)
	}
	list := decode(t, resp)
	reqs := list["requirements"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 requirement, got %d", len(reqs))
	}

	// Get versions for the requirement
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/requirements/%s", orgID, requirementKey), managerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get requirement versions: status=%d", resp.StatusCode)
	}
	versions := decode(t, resp)["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(versions))
	}

	// Publish a new version
	pubResp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirements/%s/versions", orgID, requirementKey), managerToken, map[string]any{
		"name": "Attendance Register v2", "cadence": "monthly", "effectiveFrom": "2026-06-01", "effectiveUntil": "2026-12-31", "timezone": "America/New_York", "dueDay": 20,
	})
	if pubResp.StatusCode != http.StatusCreated {
		t.Fatalf("publish version: status=%d", pubResp.StatusCode)
	}

	// Reviewer can view
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/requirements", orgID), reviewerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reviewer list: status=%d", resp.StatusCode)
	}
}

func TestPhase3AssignmentCRUD(t *testing.T) {
	h := newHarness(t, 1<<20)

	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	reviewer := "reviewer"
	h.addAs(orgID, owner, reviewer, authz.Reviewer)

	managerToken := h.token(manager)
	reviewerToken := h.token(reviewer)

	// Create a contractor and site
	contractorID := h.createContractor(orgID, managerToken, "Acme", "acme@test.com")
	siteID, _, _ := h.createSite(orgID, managerToken, "Factory")

	// Create an engagement
	status, engageResult := h.createEngagement(orgID, managerToken, contractorID, siteID, "2026-01-01", nil)
	engagementID := uuid.MustParse(engageResult["id"].(string))
	if status != http.StatusCreated {
		t.Fatalf("create engagement: status=%d", status)
	}

	// Create a requirement family
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirements", orgID), managerToken, map[string]any{
		"name": "Compliance", "cadence": "monthly", "effectiveFrom": "2026-01-01", "timezone": "UTC", "dueDay": 10,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create requirement: status=%d", resp.StatusCode)
	}
	versionID := decode(t, resp)["id"].(string)

	// Create an assignment
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirement-assignments", orgID), managerToken, map[string]any{
		"engagementId":         engagementID.String(),
		"requirementVersionId": versionID,
		"effectiveFrom":        "2026-01-01",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create assignment: status=%d", resp.StatusCode)
	}
	assignment := decode(t, resp)
	assignmentID := uuid.MustParse(assignment["id"].(string))

	// Reviewer can view
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/requirement-assignments/%s", orgID, assignmentID), reviewerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reviewer get assignment: status=%d", resp.StatusCode)
	}

	// List assignments
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/requirement-assignments", orgID), managerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list assignments: status=%d", resp.StatusCode)
	}
	list := decode(t, resp)
	assignments := list["assignments"].([]any)
	if len(assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(assignments))
	}
}

func TestPhase3ObligationGeneration(t *testing.T) {
	h := newHarness(t, 1<<20)

	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	reviewer := "reviewer"
	h.addAs(orgID, owner, reviewer, authz.Reviewer)

	managerToken := h.token(manager)
	reviewerToken := h.token(reviewer)

	// Set up: contractor, site, engagement, requirement, assignment
	contractorID := h.createContractor(orgID, managerToken, "Acme", "acme@test.com")
	siteID, _, _ := h.createSite(orgID, managerToken, "Factory")
	status, engageResult := h.createEngagement(orgID, managerToken, contractorID, siteID, "2026-01-01", nil)
	engagementID := uuid.MustParse(engageResult["id"].(string))
	if status != http.StatusCreated {
		t.Fatalf("create engagement: status=%d", status)
	}

	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirements", orgID), managerToken, map[string]any{
		"name": "Monthly Report", "cadence": "monthly", "effectiveFrom": "2026-01-01", "timezone": "UTC", "dueDay": 15,
	})
	versionID := decode(t, resp)["id"].(string)

	h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirement-assignments", orgID), managerToken, map[string]any{
		"engagementId":         engagementID.String(),
		"requirementVersionId": versionID,
		"effectiveFrom":        "2026-01-01",
	})

	// Generate obligations for January 2026
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/obligations/generate", orgID), managerToken, map[string]any{
		"year": 2026, "month": 1, "dryRun": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate obligations: status=%d", resp.StatusCode)
	}
	genResult := decode(t, resp)
	if genResult["created"].(float64) != 1 {
		t.Fatalf("expected 1 created obligation, got %v", genResult["created"])
	}

	// Idempotency: re-generate should find existing obligation
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/obligations/generate", orgID), managerToken, map[string]any{
		"year": 2026, "month": 1, "dryRun": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate again: status=%d", resp.StatusCode)
	}
	genResult = decode(t, resp)
	if genResult["existing"].(float64) != 1 {
		t.Fatalf("expected 1 existing obligation, got %v", genResult["existing"])
	}

	// List obligations
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/obligations", orgID), reviewerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list obligations: status=%d", resp.StatusCode)
	}
	list := decode(t, resp)
	obligations := list["obligations"].([]any)
	if len(obligations) != 1 {
		t.Fatalf("expected 1 obligation, got %d", len(obligations))
	}
	ob := obligations[0].(map[string]any)
	if ob["state"].(string) != "awaiting_submission" {
		t.Fatalf("expected awaiting_submission, got %s", ob["state"].(string))
	}

	// Reviewer cannot generate
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/obligations/generate", orgID), reviewerToken, map[string]any{
		"year": 2026, "month": 1, "dryRun": false,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reviewer generate: status=%d", resp.StatusCode)
	}
}

func TestPhase3ObligationDryRun(t *testing.T) {
	h := newHarness(t, 1<<20)

	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	managerToken := h.token(manager)

	// Set up engagement, requirement, assignment
	contractorID := h.createContractor(orgID, managerToken, "Acme", "acme@test.com")
	siteID, _, _ := h.createSite(orgID, managerToken, "Factory")
	status, engageResult := h.createEngagement(orgID, managerToken, contractorID, siteID, "2026-01-01", nil)
	engagementID := uuid.MustParse(engageResult["id"].(string))
	if status != http.StatusCreated {
		t.Fatalf("create engagement: status=%d", status)
	}

	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirements", orgID), managerToken, map[string]any{
		"name": "Test", "cadence": "monthly", "effectiveFrom": "2026-01-01", "timezone": "UTC", "dueDay": 15,
	})
	versionID := decode(t, resp)["id"].(string)
	h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirement-assignments", orgID), managerToken, map[string]any{
		"engagementId":         engagementID.String(),
		"requirementVersionId": versionID,
		"effectiveFrom":        "2026-01-01",
	})

	// Dry run
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/obligations/generate", orgID), managerToken, map[string]any{
		"year": 2026, "month": 1, "dryRun": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry run: status=%d", resp.StatusCode)
	}
	genResult := decode(t, resp)
	if int(genResult["created"].(float64)) != 1 {
		t.Fatalf("expected 1 would-create, got %v", genResult["created"])
	}
	if int(genResult["existing"].(float64)) != 0 {
		t.Fatalf("expected 0 existing, got %v", genResult["existing"])
	}

	// Verify no obligations in DB
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/obligations", orgID), managerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list obligations: status=%d", resp.StatusCode)
	}
	obligations := decode(t, resp)["obligations"].([]any)
	if len(obligations) != 0 {
		t.Fatalf("expected 0 obligations after dry run, got %d", len(obligations))
	}
}

func TestPhase3ObligationStateTransition(t *testing.T) {
	h := newHarness(t, 1<<20)

	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	managerToken := h.token(manager)

	// Set up
	contractorID := h.createContractor(orgID, managerToken, "Acme", "acme@test.com")
	siteID, _, _ := h.createSite(orgID, managerToken, "Factory")
	status, engageResult := h.createEngagement(orgID, managerToken, contractorID, siteID, "2026-01-01", nil)
	engagementID := uuid.MustParse(engageResult["id"].(string))
	if status != http.StatusCreated {
		t.Fatalf("create engagement: status=%d", status)
	}

	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirements", orgID), managerToken, map[string]any{
		"name": "Quarterly Report", "cadence": "monthly", "effectiveFrom": "2026-03-15", "timezone": "UTC", "dueDay": 10,
	})
	versionID := decode(t, resp)["id"].(string)
	h.postJSON(fmt.Sprintf("/v1/organizations/%s/requirement-assignments", orgID), managerToken, map[string]any{
		"engagementId":         engagementID.String(),
		"requirementVersionId": versionID,
		"effectiveFrom":        "2026-03-15",
	})

	// Generate obligations for March 2026
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/obligations/generate", orgID), managerToken, map[string]any{
		"year": 2026, "month": 3, "dryRun": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate: status=%d", resp.StatusCode)
	}

	// List obligations and check status
	resp = h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/obligations", orgID), managerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list obligations: status=%d", resp.StatusCode)
	}
	list := decode(t, resp)
	obligations := list["obligations"].([]any)
	if len(obligations) != 1 {
		t.Fatalf("expected 1 obligation, got %d", len(obligations))
	}
	ob := obligations[0].(map[string]any)
	if ob["state"].(string) != "awaiting_submission" {
		t.Fatalf("expected awaiting_submission, got %s", ob["state"].(string))
	}
	if due := ob["dueAt"].(string); !strings.HasPrefix(due, "2026-") {
		t.Fatalf("expected due date in 2026, got %s", due)
	}
}
