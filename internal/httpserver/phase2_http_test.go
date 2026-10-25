package httpserver_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/authz"
)

func (h *harness) postJSON(path, token string, body any) *http.Response {
	h.t.Helper()
	return h.request(http.MethodPost, path, token, body)
}

func (h *harness) createSite(orgID uuid.UUID, token, name string) (uuid.UUID, string, bool) {
	h.t.Helper()
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites", orgID), token, map[string]string{"name": name})
	if resp.StatusCode != http.StatusCreated {
		return uuid.Nil, "", false
	}
	body := decode(h.t, resp)
	return uuid.MustParse(body["id"].(string)), body["name"].(string), true
}

func (h *harness) createContractor(orgID uuid.UUID, token, name, email string) uuid.UUID {
	h.t.Helper()
	body := map[string]string{"name": name}
	if email != "" {
		body["contactEmail"] = email
	}
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/contractors", orgID), token, body)
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("create contractor = %d", resp.StatusCode)
	}
	return uuid.MustParse(decode(h.t, resp)["id"].(string))
}

func (h *harness) createEngagement(orgID uuid.UUID, token string, contractorID, siteID uuid.UUID, from string, until *string) (int, map[string]any) {
	h.t.Helper()
	body := map[string]any{"contractorId": contractorID.String(), "siteId": siteID.String(), "activeFrom": from}
	if until != nil {
		body["activeUntil"] = *until
	}
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/engagements", orgID), token, body)
	return resp.StatusCode, decodeIfPossible(h.t, resp)
}

func decodeIfPossible(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	if resp.StatusCode == http.StatusNoContent {
		return map[string]any{}
	}
	return decode(t, resp)
}

func date(t time.Time) string { return t.UTC().Format("2006-01-02") }

func ptr(s string) *string { return &s }

func TestPhase2AuthenticationRequired(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, _ := h.seedOrg("Org")
	for _, path := range []string{
		"/v1/organizations/" + orgID.String() + "/sites",
		"/v1/organizations/" + orgID.String() + "/contractors",
		"/v1/organizations/" + orgID.String() + "/engagements",
	} {
		if resp := h.request(http.MethodGet, path, "", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s no token = %d", path, resp.StatusCode)
		}
		if resp := h.request(http.MethodGet, path, "not-a-token", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s bad token = %d", path, resp.StatusCode)
		}
	}
}

func TestPhase2SiteCRUDAndRBAC(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	reviewer := "reviewer"
	h.addAs(orgID, owner, reviewer, authz.Reviewer)
	ownerToken, managerToken, reviewerToken := h.token(owner), h.token(manager), h.token(reviewer)

	siteID, name, ok := h.createSite(orgID, ownerToken, "  Factory A ")
	if !ok || name != "Factory A" {
		t.Fatalf("owner create name=%q", name)
	}
	if _, _, ok := h.createSite(orgID, managerToken, "Factory B"); !ok {
		t.Fatal("manager create failed")
	}
	if resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites", orgID), reviewerToken, map[string]string{"name": "X"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reviewer create = %d", resp.StatusCode)
	}
	// Reviewer can read.
	if resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sites/%s", orgID, siteID), reviewerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("reviewer read = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sites", orgID), reviewerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("reviewer list = %d", resp.StatusCode)
	}
	// Reviewer cannot patch.
	if resp := h.request(http.MethodPatch, fmt.Sprintf("/v1/organizations/%s/sites/%s", orgID, siteID), reviewerToken, map[string]string{"name": "Y"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reviewer patch = %d", resp.StatusCode)
	}
	// Manager patch, deactivate, reactivate.
	resp := h.request(http.MethodPatch, fmt.Sprintf("/v1/organizations/%s/sites/%s", orgID, siteID), managerToken, map[string]string{"name": "Factory A2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manager patch = %d", resp.StatusCode)
	}
	if body := decode(t, resp); body["name"] != "Factory A2" {
		t.Fatalf("name = %v", body["name"])
	}
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites/%s/deactivate", orgID, siteID), managerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deactivate = %d", resp.StatusCode)
	}
	if body := decode(t, resp); body["isActive"] != false {
		t.Fatalf("isActive = %v", body["isActive"])
	}
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites/%s/reactivate", orgID, siteID), managerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reactivate = %d", resp.StatusCode)
	}
}

func TestPhase2SiteValidationAndUnknownFields(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Org")
	token := h.token(owner)
	path := "/v1/organizations/" + orgID.String() + "/sites"
	if resp := h.postJSON(path, token, map[string]string{"name": "  "}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank = %d", resp.StatusCode)
	}
	if resp := h.postJSON(path, token, map[string]any{"name": "A", "extra": 1}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodPost, path, token, `{"name":`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("truncated = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodGet, "/v1/organizations/not-a-uuid/sites", token, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad uuid = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sites/%s", orgID, uuid.New()), token, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing = %d", resp.StatusCode)
	}
}

func TestPhase2Contractors(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	reviewer := "reviewer"
	h.addAs(orgID, owner, reviewer, authz.Reviewer)
	ownerToken, managerToken, reviewerToken := h.token(owner), h.token(manager), h.token(reviewer)

	ownerCreated := h.createContractor(orgID, ownerToken, "Owner Created", "")
	if ownerCreated == uuid.Nil {
		t.Fatal("owner create failed")
	}
	contractorID := h.createContractor(orgID, managerToken, "ABC Security", "Contact@Example.COM")
	if contractorID == uuid.Nil {
		t.Fatal("manager create failed")
	}
	resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/contractors/%s", orgID, contractorID), ownerToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d", resp.StatusCode)
	}
	body := decode(t, resp)
	if body["contactEmail"] != "Contact@example.com" {
		t.Fatalf("email = %v", body["contactEmail"])
	}
	if resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/contractors", orgID), reviewerToken, map[string]string{"name": "X"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reviewer create = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodPatch, fmt.Sprintf("/v1/organizations/%s/contractors/%s", orgID, contractorID), reviewerToken, map[string]string{"name": "Y"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reviewer patch = %d", resp.StatusCode)
	}
	if resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/contractors", orgID), ownerToken, map[string]string{"name": "Bad", "contactEmail": "nope"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid email = %d", resp.StatusCode)
	}
	// Omitting contactEmail preserves it; explicit null clears it.
	resp = h.request(http.MethodPatch, fmt.Sprintf("/v1/organizations/%s/contractors/%s", orgID, contractorID), ownerToken, map[string]string{"name": "Renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", resp.StatusCode)
	}
	if body := decode(t, resp); body["contactEmail"] != "Contact@example.com" {
		t.Fatalf("omitted email lost: %v", body["contactEmail"])
	}
	resp = h.request(http.MethodPatch, fmt.Sprintf("/v1/organizations/%s/contractors/%s", orgID, contractorID), ownerToken, `{"contactEmail":null}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear = %d", resp.StatusCode)
	}
	if body := decode(t, resp); body["contactEmail"] != nil {
		t.Fatalf("email = %v", body["contactEmail"])
	}
}

func TestPhase2EngagementFlow(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	reviewer := "reviewer"
	h.addAs(orgID, owner, reviewer, authz.Reviewer)
	ownerToken, managerToken, reviewerToken := h.token(owner), h.token(manager), h.token(reviewer)

	contractorID := h.createContractor(orgID, ownerToken, "ABC Security", "")
	siteID, _, _ := h.createSite(orgID, ownerToken, "Factory A")
	siteB, _, _ := h.createSite(orgID, ownerToken, "Factory B")

	status, body := h.createEngagement(orgID, managerToken, contractorID, siteID, "2026-01-01", ptr("2026-06-01"))
	if status != http.StatusCreated {
		t.Fatalf("create = %d %+v", status, body)
	}
	engagementID := body["id"].(string)
	if body["state"] != "ended" {
		t.Fatalf("state = %v", body["state"])
	}
	// Multi-site.
	if status, _ := h.createEngagement(orgID, managerToken, contractorID, siteB, "2026-01-01", nil); status != http.StatusCreated {
		t.Fatalf("second site = %d", status)
	}
	// Overlap.
	if status, _ := h.createEngagement(orgID, managerToken, contractorID, siteID, "2026-05-01", ptr("2026-08-01")); status != http.StatusConflict {
		t.Fatalf("overlap = %d", status)
	}
	// Adjacent.
	if status, _ := h.createEngagement(orgID, managerToken, contractorID, siteID, "2026-06-01", ptr("2026-09-01")); status != http.StatusCreated {
		t.Fatalf("adjacent = %d", status)
	}
	// Reviewer read, not write.
	if resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/engagements/%s", orgID, engagementID), reviewerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("reviewer read = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodPatch, fmt.Sprintf("/v1/organizations/%s/engagements/%s", orgID, engagementID), reviewerToken, map[string]string{"activeUntil": "2026-07-01"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reviewer patch = %d", resp.StatusCode)
	}
	// Close.
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/engagements/%s/close", orgID, engagementID), managerToken, map[string]string{"activeUntil": "2026-06-01"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("close = %d", resp.StatusCode)
	}
	// Historical record remains.
	if resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/engagements/%s", orgID, engagementID), ownerToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("history = %d", resp.StatusCode)
	}
}

func TestPhase2EngagementValidationAndCrossTenant(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgA, _, ownerA := h.seedOrg("A")
	orgB, _, ownerB := h.seedOrg("B")
	ownerAToken, ownerBToken := h.token(ownerA), h.token(ownerB)

	contractorA := h.createContractor(orgA, ownerAToken, "CA", "")
	siteA, _, _ := h.createSite(orgA, ownerAToken, "SA")
	contractorB := h.createContractor(orgB, ownerBToken, "CB", "")
	siteB, _, _ := h.createSite(orgB, ownerBToken, "SB")

	// Cross-tenant parents.
	if status, _ := h.createEngagement(orgA, ownerAToken, contractorB, siteA, "2026-01-01", nil); status != http.StatusNotFound {
		t.Fatalf("foreign contractor = %d", status)
	}
	if status, _ := h.createEngagement(orgA, ownerAToken, contractorA, siteB, "2026-01-01", nil); status != http.StatusNotFound {
		t.Fatalf("foreign site = %d", status)
	}
	// Invalid interval and date.
	if status, _ := h.createEngagement(orgA, ownerAToken, contractorA, siteA, "2026-02-01", ptr("2026-02-01")); status != http.StatusBadRequest {
		t.Fatalf("equal = %d", status)
	}
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/engagements", orgA), ownerAToken,
		map[string]any{"contractorId": contractorA.String(), "siteId": siteA.String(), "activeFrom": "2026-02-30"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("impossible = %d", resp.StatusCode)
	}
	// Cross-tenant engagement ID.
	status, body := h.createEngagement(orgB, ownerBToken, contractorB, siteB, "2026-01-01", nil)
	if status != http.StatusCreated {
		t.Fatal(status)
	}
	if resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/engagements/%s", orgA, body["id"].(string)), ownerAToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign engagement = %d", resp.StatusCode)
	}
}

func TestPhase2DeactivationConflictAndLifecycle(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Org")
	token := h.token(owner)
	contractorID := h.createContractor(orgID, token, "ABC", "")
	siteID, _, _ := h.createSite(orgID, token, "Factory A")
	today := date(time.Now())
	yesterday := date(time.Now().AddDate(0, 0, -1))
	if status, _ := h.createEngagement(orgID, token, contractorID, siteID, yesterday, nil); status != http.StatusCreated {
		t.Fatal(status)
	}
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites/%s/deactivate", orgID, siteID), token, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("site deactivate = %d", resp.StatusCode)
	}
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/contractors/%s/deactivate", orgID, contractorID), token, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("contractor deactivate = %d", resp.StatusCode)
	}
	// Close, then deactivate.
	future := date(time.Now().AddDate(0, 0, 1))
	resp = h.postJSON(fmt.Sprintf("/v1/organizations/%s/engagements/%s/close", orgID,
		decode(t, h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/engagements", orgID), token, nil))["engagements"].([]any)[0].(map[string]any)["id"].(string)),
		token, map[string]string{"activeUntil": today})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("close = %d", resp.StatusCode)
	}
	if resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites/%s/deactivate", orgID, siteID), token, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("site deactivate after close = %d", resp.StatusCode)
	}
	// Inactive site cannot receive a new engagement.
	contractor2 := h.createContractor(orgID, token, "Other", "")
	if status, _ := h.createEngagement(orgID, token, contractor2, siteID, future, nil); status != http.StatusConflict {
		t.Fatalf("inactive site = %d", status)
	}
}

func TestPhase2RevokedManagerAndAuditFailure(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Org")
	manager := "manager"
	h.addAs(orgID, owner, manager, authz.Manager)
	managerToken := h.token(manager)
	if _, _, ok := h.createSite(orgID, managerToken, "First"); !ok {
		t.Fatal("manager write failed")
	}
	// Revoke through the existing organization API.
	var memberships map[string]any
	body := decode(t, h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/memberships", orgID), h.token(owner), nil))
	memberships = body
	for _, item := range memberships["memberships"].([]any) {
		m := item.(map[string]any)
		if m["identity"].(map[string]any)["subject"] == manager {
			resp := h.request(http.MethodDelete, fmt.Sprintf("/v1/organizations/%s/memberships/%s", orgID, m["id"].(string)), h.token(owner), nil)
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("revoke = %d", resp.StatusCode)
			}
		}
	}
	if resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites", orgID), managerToken, map[string]string{"name": "Second"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked manager = %d", resp.StatusCode)
	}
	// Audit failure rolls back the write and returns 500.
	h.pool.Exec(t.Context(), `ALTER TABLE audit_events RENAME TO audit_events_off`)
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites", orgID), h.token(owner), map[string]string{"name": "Lost"})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("audit failure = %d", resp.StatusCode)
	}
	if got := len(decode(t, h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sites", orgID), h.token(owner), nil))["sites"].([]any)); got != 1 {
		t.Fatalf("sites = %d", got)
	}
	if _, err := h.pool.Exec(t.Context(), `ALTER TABLE audit_events_off RENAME TO audit_events`); err != nil {
		t.Fatal(err)
	}
}

func TestPhase2PaginationDeterministic(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Org")
	token := h.token(owner)
	for i := 0; i < 3; i++ {
		h.createSite(orgID, token, fmt.Sprintf("Site %d", i))
	}
	path := fmt.Sprintf("/v1/organizations/%s/sites?limit=2", orgID)
	body := decode(t, h.request(http.MethodGet, path, token, nil))
	first := body["sites"].([]any)
	if len(first) != 2 || body["nextCursor"] == nil {
		t.Fatalf("first page = %d", len(first))
	}
	next := body["nextCursor"].(string)
	body = decode(t, h.request(http.MethodGet, path+"&cursor="+next, token, nil))
	second := body["sites"].([]any)
	if len(second) != 1 || body["nextCursor"] != nil {
		t.Fatalf("second page = %d next %v", len(second), body["nextCursor"])
	}
	seen := map[string]bool{}
	for _, item := range append(first, second...) {
		id := item.(map[string]any)["id"].(string)
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 3 {
		t.Fatalf("unique = %d", len(seen))
	}
}

func TestPhase2FilteredListsStayTenantScoped(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgA, _, ownerA := h.seedOrg("A")
	orgB, _, ownerB := h.seedOrg("B")
	h.createSite(orgA, h.token(ownerA), "Same")
	h.createSite(orgB, h.token(ownerB), "Same")
	active := "true"
	resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sites?isActive=%s&q=Same", orgA, active), h.token(ownerA), nil)
	body := decode(t, resp)
	if len(body["sites"].([]any)) != 1 {
		t.Fatalf("items = %d", len(body["sites"].([]any)))
	}
	if resp := h.request(http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sites?isActive=maybe", orgA), h.token(ownerA), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad filter = %d", resp.StatusCode)
	}
}

func TestPhase2OversizedBodyAndRequestID(t *testing.T) {
	h := newHarness(t, 32)
	orgID, _, owner := h.seedOrg("Org")
	resp := h.postJSON(fmt.Sprintf("/v1/organizations/%s/sites", orgID), h.token(owner), map[string]string{"name": strings.Repeat("x", 100)})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("large = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Fatal("missing request id")
	}
	body := decode(t, resp)
	if body["requestId"] == "" || body["status"].(float64) != http.StatusRequestEntityTooLarge {
		t.Fatalf("problem = %v", body)
	}
}
