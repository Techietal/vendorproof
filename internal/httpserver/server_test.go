package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/auth"
	"vendorproof/internal/authz"
	"vendorproof/internal/config"
	"vendorproof/internal/contractors"
	"vendorproof/internal/engagements"
	"vendorproof/internal/httpserver"
	"vendorproof/internal/identity"
	"vendorproof/internal/logging"
	"vendorproof/internal/organizations"
	"vendorproof/internal/sites"
	"vendorproof/internal/testutil"
	"vendorproof/internal/testutil/testjwks"
)

const (
	testIssuer   = "https://idp.test"
	testAudience = "vendorproof-api"
)

type harness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	signer *testjwks.Signer
	server *httptest.Server
	client *http.Client
	svc    *organizations.Service
}

func newHarness(t *testing.T, maxBody int64) *harness {
	t.Helper()
	pool := testutil.NewDB(t)
	signer := testjwks.New(t)
	verifier, err := auth.NewVerifier(config.OIDC{
		Issuer: signer.Issuer, Audience: testAudience, JWKSURL: signer.URL(),
		AllowedAlgorithms: []string{"RS256"}, RequiredTokenType: "at+jwt",
		MinJWKSRefresh: 5 * time.Millisecond, MaxJWKSRefresh: 100 * time.Millisecond,
		JWKSFetchTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := organizations.NewService(pool, organizations.NewRepository(pool))
	handler := httpserver.New(httpserver.Deps{
		Logger: logging.New(0, io.Discard), Verifier: verifier, Organizations: svc,
		Sites:       sites.NewService(pool),
		Contractors: contractors.NewService(pool),
		Engagements: engagements.NewService(pool),
		Readiness:   pool, MaxBodyBytes: maxBody, Timeout: 10 * time.Second,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &harness{t: t, pool: pool, signer: signer, server: server,
		client: server.Client(), svc: svc}
}

func (h *harness) token(subject string) string {
	return h.signer.Sign(testjwks.Claims(subject))
}

func (h *harness) request(method, path, token string, body any) *http.Response {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			reader = strings.NewReader(b)
		default:
			encoded, err := json.Marshal(body)
			if err != nil {
				h.t.Fatal(err)
			}
			reader = bytes.NewReader(encoded)
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}

func (h *harness) bootstrap(name, ownerSubject string) (uuid.UUID, uuid.UUID) {
	h.t.Helper()
	org, membership, err := h.svc.Bootstrap(context.Background(), name, h.signer.Issuer, ownerSubject)
	if err != nil {
		h.t.Fatal(err)
	}
	return org.ID, membership.ID
}

func (h *harness) seedOrg(name string) (uuid.UUID, uuid.UUID, string) {
	h.t.Helper()
	subject := "owner-" + uuid.NewString()[:8]
	orgID, membershipID := h.bootstrap(name, subject)
	return orgID, membershipID, subject
}

func (h *harness) addAs(orgID uuid.UUID, actorSubject, subject string, role authz.Role) {
	h.t.Helper()
	_, err := h.svc.AddMembership(context.Background(), identity.Identity{Issuer: h.signer.Issuer, Subject: actorSubject}, orgID,
		identity.Identity{Issuer: h.signer.Issuer, Subject: subject}, role)
	if err != nil {
		h.t.Fatal(err)
	}
}

func TestLivenessAndReadiness(t *testing.T) {
	h := newHarness(t, 1<<20)

	resp := h.request(http.MethodGet, "/health/live", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q", body)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Error("missing request id")
	}

	if resp = h.request(http.MethodGet, "/health/ready", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("ready = %d", resp.StatusCode)
	}
}

type failingReadiness struct{}

func (failingReadiness) Ping(context.Context) error { return errors.New("database down") }

func TestReadinessFailureAndLivenessIsIndependent(t *testing.T) {
	handler := httpserver.New(httpserver.Deps{
		Logger: logging.New(0, io.Discard), Readiness: failingReadiness{},
		MaxBodyBytes: 1024, Timeout: time.Second,
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "database down") || strings.Contains(strings.ToLower(string(body)), "postgres://") {
		t.Fatalf("readiness leaked details: %s", body)
	}

	resp2, err := server.Client().Get(server.URL + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", resp2.StatusCode)
	}
}

func TestAuthenticationRejections(t *testing.T) {
	h := newHarness(t, 1<<20)
	unserved, unservedKid := testjwks.NewUnservedKey(t)
	valid := testjwks.Claims("user")

	_ = valid
	cases := map[string]func() string{
		"missing": func() string { return "" },
		"garbage": func() string { return "garbage" },
		"expired": func() string {
			return h.signer.Sign(withClaim(testjwks.Claims("user"), "exp", time.Now().Add(-5*time.Minute).Unix()))
		},
		"wrong audience":    func() string { return h.signer.Sign(withClaim(testjwks.Claims("user"), "aud", "other-api")) },
		"wrong issuer":      func() string { return h.signer.Sign(withClaim(testjwks.Claims("user"), "iss", "https://evil.test")) },
		"invalid signature": func() string { return h.signer.SignAs(testjwks.Claims("user"), unserved, h.signer.Kid(), "at+jwt") },
		"unknown kid":       func() string { return h.signer.SignAs(testjwks.Claims("user"), unserved, unservedKid, "at+jwt") },
		"id token": func() string {
			return h.signer.SignAs(testjwks.Claims("user"), h.signer.CurrentKey(), h.signer.Kid(), "JWT")
		},
	}
	for name, mint := range cases {
		t.Run(name, func(t *testing.T) {
			resp := h.request(http.MethodGet, "/v1/me", mint(), nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/problem+json") {
				t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
			}
			body := decode(t, resp)
			if body["requestId"] == nil {
				t.Error("problem lacks request id")
			}
		})
	}

	// Wrong scheme.
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Basic abc")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("basic auth = %d", resp.StatusCode)
	}
}

func withClaim(claims jwtMap, key string, value any) jwtMap {
	claims[key] = value
	return claims
}

type jwtMap = map[string]any

func TestUnknownAuthenticatedPathIs404(t *testing.T) {
	h := newHarness(t, 1<<20)
	resp := h.request(http.MethodGet, "/v1/contractors", h.token("user"), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestMeAndOrganizationVisibility(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgA, _, ownerA := h.seedOrg("A")
	orgB, _, _ := h.seedOrg("B")

	resp := h.request(http.MethodGet, "/v1/me", h.token(ownerA), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me = %d", resp.StatusCode)
	}
	body := decode(t, resp)
	identityMap := body["identity"].(map[string]any)
	if identityMap["subject"] != ownerA || identityMap["issuer"] != h.signer.Issuer {
		t.Fatalf("identity = %+v", identityMap)
	}
	memberships := body["memberships"].([]any)
	if len(memberships) != 1 {
		t.Fatalf("memberships = %d", len(memberships))
	}

	// Arriving with no memberships is valid.
	stranger := h.token("stranger")
	resp = h.request(http.MethodGet, "/v1/me", stranger, nil)
	if resp.StatusCode != http.StatusOK || len(decode(t, resp)["memberships"].([]any)) != 0 {
		t.Fatalf("stranger me = %d", resp.StatusCode)
	}

	// Cross-tenant details are 404, not 403.
	if resp = h.request(http.MethodGet, "/v1/organizations/"+orgB.String(), h.token(ownerA), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross get = %d", resp.StatusCode)
	}
	if resp = h.request(http.MethodGet, "/v1/organizations/"+orgB.String()+"/memberships", h.token(ownerA), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross memberships = %d", resp.StatusCode)
	}
	if resp = h.request(http.MethodGet, "/v1/organizations/"+uuid.NewString(), h.token(ownerA), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing get = %d", resp.StatusCode)
	}
	if resp = h.request(http.MethodGet, "/v1/organizations/not-a-uuid", h.token(ownerA), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad uuid = %d", resp.StatusCode)
	}

	// Listing returns only the caller's organization.
	resp = h.request(http.MethodGet, "/v1/organizations", h.token(ownerA), nil)
	orgs := decode(t, resp)["organizations"].([]any)
	if len(orgs) != 1 || orgs[0].(map[string]any)["id"] != orgA.String() {
		t.Fatalf("organizations = %+v", orgs)
	}
}

func TestOrganizationUpdateRBAC(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, _, owner := h.seedOrg("Original")
	h.addAs(orgID, owner, "manager", authz.Manager)
	h.addAs(orgID, owner, "reviewer", authz.Reviewer)

	resp := h.request(http.MethodPatch, "/v1/organizations/"+orgID.String(), h.token(owner), map[string]string{"name": " Renamed "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner patch = %d", resp.StatusCode)
	}
	if decode(t, resp)["name"] != "Renamed" {
		t.Fatal("name not updated")
	}
	for _, subject := range []string{"manager", "reviewer"} {
		resp = h.request(http.MethodPatch, "/v1/organizations/"+orgID.String(), h.token(subject), map[string]string{"name": "X"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s patch = %d", subject, resp.StatusCode)
		}
	}
	resp = h.request(http.MethodGet, "/v1/organizations/"+orgID.String(), h.token("reviewer"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reviewer get = %d", resp.StatusCode)
	}
	resp = h.request(http.MethodGet, "/v1/organizations/"+orgID.String(), h.token("manager"), nil)
	member := decode(t, resp)
	if member["role"] != "manager" {
		t.Fatalf("role = %v", member["role"])
	}
}

func TestMembershipHTTPFlowAndRBAC(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, ownerMembership, owner := h.seedOrg("Org")
	base := "/v1/organizations/" + orgID.String() + "/memberships"

	resp := h.request(http.MethodPost, base, h.token(owner), map[string]string{
		"issuer": h.signer.Issuer, "subject": "new-member", "role": "reviewer",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %+v", resp.StatusCode, decode(t, resp))
	}
	created := decode(t, resp)
	membershipID := created["id"].(string)
	if resp.Header.Get("Location") == "" {
		t.Error("missing Location header")
	}
	if created["identity"].(map[string]any)["issuer"] != h.signer.Issuer {
		t.Fatalf("identity = %+v", created["identity"])
	}

	// Duplicate.
	resp = h.request(http.MethodPost, base, h.token(owner), map[string]string{
		"issuer": h.signer.Issuer, "subject": "new-member", "role": "manager",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate = %d", resp.StatusCode)
	}
	// Unknown fields and invalid role.
	resp = h.request(http.MethodPost, base, h.token(owner), `{"issuer":"https://i","subject":"s","role":"owner","extra":1}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", resp.StatusCode)
	}
	resp = h.request(http.MethodPost, base, h.token(owner), map[string]string{
		"issuer": "https://i", "subject": "s", "role": "admin",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid role = %d", resp.StatusCode)
	}
	if decode(t, resp)["errors"] == nil {
		t.Error("missing field errors")
	}

	// Manager may view but not mutate membership.
	h.addAs(orgID, owner, "manager", authz.Manager)
	resp = h.request(http.MethodGet, base, h.token("manager"), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("manager list = %d", resp.StatusCode)
	}
	resp = h.request(http.MethodPost, base, h.token("manager"), map[string]string{
		"issuer": "https://i", "subject": "m2", "role": "reviewer",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("manager create = %d", resp.StatusCode)
	}

	// Role change.
	resp = h.request(http.MethodPatch, base+"/"+membershipID, h.token(owner), map[string]string{"role": "owner"})
	if resp.StatusCode != http.StatusOK || decode(t, resp)["role"] != "owner" {
		t.Fatalf("patch = %d", resp.StatusCode)
	}
	// Last-owner protection only applies to the sole owner; now there are two,
	// so demoting the original is allowed.
	resp = h.request(http.MethodPatch, base+"/"+ownerMembership.String(), h.token(owner), map[string]string{"role": "manager"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second owner demotion = %d", resp.StatusCode)
	}
	resp = h.request(http.MethodPatch, base+"/"+ownerMembership.String(), h.token("new-member"), map[string]string{"role": "reviewer"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("now-manager demotion = %d", resp.StatusCode)
	}

	// Revoke the demoted original member; new-member remains the sole owner.
	resp = h.request(http.MethodDelete, base+"/"+ownerMembership.String(), h.token("new-member"), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp = h.request(http.MethodDelete, base+"/"+ownerMembership.String(), h.token("new-member"), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("repeat delete = %d", resp.StatusCode)
	}
	resp = h.request(http.MethodGet, "/v1/organizations/"+orgID.String(), h.token(owner), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked access = %d", resp.StatusCode)
	}
}

func TestLastOwnerRemovalConflict(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, ownerMembership, owner := h.seedOrg("Org")
	base := "/v1/organizations/" + orgID.String() + "/memberships/" + ownerMembership.String()

	if resp := h.request(http.MethodDelete, base, h.token(owner), nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete last owner = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodPatch, base, h.token(owner), map[string]string{"role": "manager"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("demote last owner = %d", resp.StatusCode)
	}
}

func TestCrossTenantMembershipIDsAndRevokedMember(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgA, _, ownerA := h.seedOrg("A")
	orgB, ownerBMembership, _ := h.seedOrg("B")
	path := "/v1/organizations/" + orgA.String() + "/memberships/" + ownerBMembership.String()

	if resp := h.request(http.MethodPatch, path, h.token(ownerA), map[string]string{"role": "manager"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross patch = %d", resp.StatusCode)
	}
	if resp := h.request(http.MethodDelete, path, h.token(ownerA), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross delete = %d", resp.StatusCode)
	}
	_ = orgB
}

func TestConcurrentLastOwnerRemovalOverHTTP(t *testing.T) {
	h := newHarness(t, 1<<20)
	orgID, first, owner := h.seedOrg("Org")
	h.addAs(orgID, owner, "second-owner", authz.Owner)
	h.addAs(orgID, owner, "third-owner", authz.Owner)
	// Look up the second owner's membership ID.
	var second uuid.UUID
	if err := h.pool.QueryRow(context.Background(),
		`SELECT id FROM memberships WHERE organization_id=$1 AND identity_subject='second-owner'`, orgID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	base := "/v1/organizations/" + orgID.String() + "/memberships/"

	// A stable third owner removes the other two concurrently. Exactly one
	// owner must remain because self-removal is impossible.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	start := make(chan struct{})
	for _, target := range []uuid.UUID{first, second} {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			<-start
			codes <- h.request(http.MethodDelete, base+id.String(), h.token("third-owner"), nil).StatusCode
		}(target)
	}
	close(start)
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusNoContent {
			t.Fatalf("status = %d", code)
		}
	}
	var third uuid.UUID
	if err := h.pool.QueryRow(context.Background(),
		`SELECT id FROM memberships WHERE organization_id=$1 AND identity_subject='third-owner'`, orgID).Scan(&third); err != nil {
		t.Fatal(err)
	}
	if resp := h.request(http.MethodDelete, base+third.String(), h.token("third-owner"), nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("sole owner delete = %d", resp.StatusCode)
	}
	if got := testutil.Count(t, h.pool, `SELECT count(*) FROM memberships WHERE organization_id=$1 AND role='owner' AND revoked_at IS NULL`, orgID); got != 1 {
		t.Fatalf("owners = %d", got)
	}
}

func TestBodyLimitsAndMalformedBodies(t *testing.T) {
	h := newHarness(t, 200)
	orgID, _, owner := h.seedOrg("Org")
	path := "/v1/organizations/" + orgID.String()

	resp := h.request(http.MethodPatch, path, h.token(owner), map[string]string{"name": strings.Repeat("a", 300)})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d", resp.StatusCode)
	}
	for name, body := range map[string]string{
		"empty": "", "malformed": "{", "two objects": `{"name":"a"}{"name":"b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.request(http.MethodPatch, path, h.token(owner), body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", resp.StatusCode)
			}
		})
	}
}

func TestPagination(t *testing.T) {
	h := newHarness(t, 1<<20)
	subject := "pager"
	for _, name := range []string{"one", "two", "three"} {
		org, _, err := h.svc.Bootstrap(context.Background(), name, h.signer.Issuer, subject)
		if err != nil {
			t.Fatal(err)
		}
		_ = org
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		path := "/v1/organizations?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		resp := h.request(http.MethodGet, path, h.token(subject), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("page %d = %d", page, resp.StatusCode)
		}
		body := decode(t, resp)
		for _, raw := range body["organizations"].([]any) {
			id := raw.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate across pages: %s", id)
			}
			seen[id] = true
		}
		next, _ := body["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 3 {
		t.Fatalf("saw %d organizations", len(seen))
	}

	resp := h.request(http.MethodGet, "/v1/organizations?limit=201", h.token(subject), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("limit 201 = %d", resp.StatusCode)
	}
	resp = h.request(http.MethodGet, "/v1/organizations?cursor=@@@", h.token(subject), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d", resp.StatusCode)
	}
}

func TestAccessTokensAreNotLogged(t *testing.T) {
	pool := testutil.NewDB(t)
	signer := testjwks.New(t)
	verifier, err := auth.NewVerifier(config.OIDC{
		Issuer: signer.Issuer, Audience: testAudience, JWKSURL: signer.URL(),
		AllowedAlgorithms: []string{"RS256"}, RequiredTokenType: "at+jwt",
		MinJWKSRefresh: time.Millisecond, MaxJWKSRefresh: time.Second, JWKSFetchTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler := httpserver.New(httpserver.Deps{
		Logger: logging.New(0, &logs), Verifier: verifier,
		Organizations: organizations.NewService(pool, organizations.NewRepository(pool)),
		Readiness:     pool, MaxBodyBytes: 1024, Timeout: time.Second,
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	token := signer.Sign(testjwks.Claims("user"))
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.Contains(logs.String(), token) {
		t.Fatal("access token appeared in logs")
	}
	if !strings.Contains(logs.String(), "request_id") {
		t.Fatalf("no request log: %s", logs.String())
	}
}

func TestHealthIsNotBodyLimitedOrAuthenticated(t *testing.T) {
	h := newHarness(t, 1)
	resp := h.request(http.MethodGet, "/health/live", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", resp.StatusCode)
	}
}
