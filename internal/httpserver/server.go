// Package httpserver wires transport concerns: routing, middleware, and thin
// handlers. Business rules live in application services.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/authz"
	"vendorproof/internal/httpx"
	"vendorproof/internal/identity"
	"vendorproof/internal/organizations"
)

// TokenVerifier validates bearer access tokens.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (identity.Identity, error)
}

// OrganizationService is the HTTP-facing subset of the organization service.
type OrganizationService interface {
	List(ctx context.Context, actor identity.Identity, limit int, cursor string) ([]organizations.OrganizationWithRole, string, error)
	Me(ctx context.Context, actor identity.Identity, limit int, cursor string) ([]organizations.MembershipWithOrganization, string, error)
	Get(ctx context.Context, actor identity.Identity, orgID uuid.UUID) (organizations.Organization, authz.Role, error)
	Update(ctx context.Context, actor identity.Identity, orgID uuid.UUID, name string) (organizations.Organization, error)
	ListMemberships(ctx context.Context, actor identity.Identity, orgID uuid.UUID, limit int, cursor string) ([]organizations.Membership, string, error)
	AddMembershipByInput(ctx context.Context, actor identity.Identity, orgID uuid.UUID, issuer, subject, role string) (organizations.Membership, error)
	ChangeMembershipRole(ctx context.Context, actor identity.Identity, orgID, membershipID uuid.UUID, role string) (organizations.Membership, error)
	RevokeMembership(ctx context.Context, actor identity.Identity, orgID, membershipID uuid.UUID) error
}

// Readiness reports whether a required dependency is usable.
type Readiness interface {
	Ping(ctx context.Context) error
}

// Deps are explicit constructor dependencies.
type Deps struct {
	Logger        *slog.Logger
	Verifier      TokenVerifier
	Organizations OrganizationService
	Readiness     Readiness
	MaxBodyBytes  int64
	Timeout       time.Duration
}

// Server holds handler dependencies.
type Server struct {
	deps Deps
}

// New builds the complete handler stack.
func New(deps Deps) http.Handler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.MaxBodyBytes <= 0 {
		deps.MaxBodyBytes = 64 << 10
	}
	if deps.Timeout <= 0 {
		deps.Timeout = 15 * time.Second
	}
	s := &Server{deps: deps}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)

	protected := http.NewServeMux()
	protected.HandleFunc("GET /v1/me", s.me)
	protected.HandleFunc("GET /v1/organizations", s.listOrganizations)
	protected.HandleFunc("GET /v1/organizations/{organizationId}", s.getOrganization)
	protected.HandleFunc("PATCH /v1/organizations/{organizationId}", s.updateOrganization)
	protected.HandleFunc("GET /v1/organizations/{organizationId}/memberships", s.listMemberships)
	protected.HandleFunc("POST /v1/organizations/{organizationId}/memberships", s.addMembership)
	protected.HandleFunc("PATCH /v1/organizations/{organizationId}/memberships/{membershipId}", s.changeMembershipRole)
	protected.HandleFunc("DELETE /v1/organizations/{organizationId}/memberships/{membershipId}", s.revokeMembership)
	mux.Handle("/v1/", s.authenticate(protected))

	var handler http.Handler = mux
	handler = requestTimeout(deps.Timeout, handler)
	handler = bodyLimit(deps.MaxBodyBytes, handler)
	handler = securityHeaders(handler)
	handler = accessLog(deps.Logger, handler)
	handler = recoverPanic(deps.Logger, handler)
	handler = requestID(handler)
	return handler
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	body := map[string]string{"status": "ok"}
	if s.deps.Readiness == nil {
		status = http.StatusServiceUnavailable
		body["status"] = "unavailable"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.deps.Readiness.Ping(ctx); err != nil {
			status = http.StatusServiceUnavailable
			body["status"] = "unavailable"
			s.deps.Logger.WarnContext(r.Context(), "readiness failed", "error", err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ---- DTOs ----

type identityDTO struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

type organizationDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type membershipDTO struct {
	ID             string      `json:"id"`
	OrganizationID string      `json:"organizationId"`
	Identity       identityDTO `json:"identity"`
	Role           string      `json:"role"`
	CreatedAt      string      `json:"createdAt"`
	UpdatedAt      string      `json:"updatedAt"`
	RevokedAt      *string     `json:"revokedAt,omitempty"`
	Organization   string      `json:"organizationName,omitempty"`
}

func orgDTO(o organizations.Organization, role authz.Role) organizationDTO {
	return organizationDTO{
		ID: o.ID.String(), Name: o.Name, Role: string(role),
		CreatedAt: o.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: o.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func memberDTO(m organizations.Membership) membershipDTO {
	dto := membershipDTO{
		ID: m.ID.String(), OrganizationID: m.OrganizationID.String(),
		Identity:  identityDTO{Issuer: m.Identity.Issuer, Subject: m.Identity.Subject},
		Role:      string(m.Role),
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if m.RevokedAt != nil {
		v := m.RevokedAt.UTC().Format(time.RFC3339)
		dto.RevokedAt = &v
	}
	return dto
}

// ---- Handlers ----

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, next, err := s.deps.Organizations.Me(r.Context(), actor(r), limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]membershipDTO, 0, len(items))
	for _, item := range items {
		dto := memberDTO(item.Membership)
		dto.Organization = item.OrganizationName
		dtos = append(dtos, dto)
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"identity":    identityDTO{Issuer: actor(r).Issuer, Subject: actor(r).Subject},
		"memberships": dtos,
		"nextCursor":  emptyToNil(next),
	})
}

func (s *Server) listOrganizations(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, next, err := s.deps.Organizations.List(r.Context(), actor(r), limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]organizationDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, orgDTO(item.Organization, item.Role))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"organizations": dtos, "nextCursor": emptyToNil(next)})
}

func (s *Server) getOrganization(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	org, role, err := s.deps.Organizations.Get(r.Context(), actor(r), orgID)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, orgDTO(org, role))
}

func (s *Server) updateOrganization(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	org, err := s.deps.Organizations.Update(r.Context(), actor(r), orgID, body.Name)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, orgDTO(org, authz.Owner))
}

func (s *Server) listMemberships(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, next, err := s.deps.Organizations.ListMemberships(r.Context(), actor(r), orgID, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]membershipDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, memberDTO(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"memberships": dtos, "nextCursor": emptyToNil(next)})
}

func (s *Server) addMembership(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	var body struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
		Role    string `json:"role"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	m, err := s.deps.Organizations.AddMembershipByInput(r.Context(), actor(r), orgID, body.Issuer, body.Subject, body.Role)
	if err != nil {
		s.writeServiceError(w, r, err, "An active membership for that identity already exists.")
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+orgID.String()+"/memberships/"+m.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, memberDTO(m))
}

func (s *Server) changeMembershipRole(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	membershipID, ok := pathUUID(w, r, "membershipId")
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	m, err := s.deps.Organizations.ChangeMembershipRole(r.Context(), actor(r), orgID, membershipID, body.Role)
	if err != nil {
		s.writeServiceError(w, r, err, "The membership is already revoked.")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, memberDTO(m))
}

func (s *Server) revokeMembership(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	membershipID, ok := pathUUID(w, r, "membershipId")
	if !ok {
		return
	}
	if err := s.deps.Organizations.RevokeMembership(r.Context(), actor(r), orgID, membershipID); err != nil {
		s.writeServiceError(w, r, err, "The membership is already revoked.")
		return
	}
	httpx.WriteNoContent(w)
}

// ---- Helpers ----

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		httpx.WriteProblem(w, r, http.StatusNotFound, "Resource not found.")
		return uuid.Nil, false
	}
	return id, true
}

func pageParams(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	raw := r.URL.Query().Get("limit")
	limit := 0
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
				map[string]string{"limit": "must be an integer"})
			return 0, "", false
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), true
}

func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error, conflictDetail string) {
	var invalid *organizations.ValidationError
	switch {
	case errors.As(err, &invalid):
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.", invalid.Fields)
	case errors.Is(err, organizations.ErrValidation):
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Validation failed.")
	case errors.Is(err, organizations.ErrNotFound):
		httpx.WriteProblem(w, r, http.StatusNotFound, "Resource not found.")
	case errors.Is(err, organizations.ErrForbidden):
		httpx.WriteProblem(w, r, http.StatusForbidden, "Insufficient permissions for this operation.")
	case errors.Is(err, organizations.ErrLastOwner):
		httpx.WriteProblem(w, r, http.StatusConflict, organizations.ErrLastOwner.Error()+".")
	case errors.Is(err, organizations.ErrConflict):
		httpx.WriteProblem(w, r, http.StatusConflict, conflictDetail)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "The request could not complete in time.")
	default:
		s.deps.Logger.ErrorContext(r.Context(), "unhandled service error", "error", err)
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "An internal error occurred.")
	}
}

func emptyToNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
