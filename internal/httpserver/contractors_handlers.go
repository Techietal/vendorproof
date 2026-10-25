package httpserver

import (
	"net/http"
	"time"

	"vendorproof/internal/contractors"
	"vendorproof/internal/httpx"
	"vendorproof/internal/optional"
)

type contractorDTO struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organizationId"`
	Name           string  `json:"name"`
	ContactEmail   *string `json:"contactEmail"`
	IsActive       bool    `json:"isActive"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

func contractorResponse(c contractors.Contractor) contractorDTO {
	return contractorDTO{
		ID: c.ID.String(), OrganizationID: c.OrganizationID.String(), Name: c.Name,
		ContactEmail: c.ContactEmail, IsActive: c.IsActive,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) createContractor(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	var body struct {
		Name         string `json:"name"`
		ContactEmail string `json:"contactEmail"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	c, err := s.deps.Contractors.Create(r.Context(), actor(r), orgID, body.Name, body.ContactEmail)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+orgID.String()+"/contractors/"+c.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, contractorResponse(c))
}

func (s *Server) listContractors(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	active, ok := queryBool(w, r, "isActive")
	if !ok {
		return
	}
	items, next, err := s.deps.Contractors.List(r.Context(), actor(r), orgID,
		contractors.Filter{Active: active, Search: r.URL.Query().Get("q")}, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]contractorDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, contractorResponse(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"contractors": dtos, "nextCursor": emptyToNil(next)})
}

func (s *Server) getContractor(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	contractorID, ok := pathUUID(w, r, "contractorId")
	if !ok {
		return
	}
	c, err := s.deps.Contractors.Get(r.Context(), actor(r), orgID, contractorID)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, contractorResponse(c))
}

func (s *Server) updateContractor(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	contractorID, ok := pathUUID(w, r, "contractorId")
	if !ok {
		return
	}
	var body struct {
		Name         optional.Value[string] `json:"name"`
		ContactEmail optional.Value[string] `json:"contactEmail"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	c, err := s.deps.Contractors.Update(r.Context(), actor(r), orgID, contractorID, contractors.Patch{
		Name: body.Name, ContactEmail: body.ContactEmail,
	})
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, contractorResponse(c))
}

func (s *Server) deactivateContractor(w http.ResponseWriter, r *http.Request) {
	s.contractorTransition(w, r, false)
}

func (s *Server) reactivateContractor(w http.ResponseWriter, r *http.Request) {
	s.contractorTransition(w, r, true)
}

func (s *Server) contractorTransition(w http.ResponseWriter, r *http.Request, activate bool) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	contractorID, ok := pathUUID(w, r, "contractorId")
	if !ok {
		return
	}
	var c contractors.Contractor
	var err error
	if activate {
		c, err = s.deps.Contractors.Reactivate(r.Context(), actor(r), orgID, contractorID)
	} else {
		c, err = s.deps.Contractors.Deactivate(r.Context(), actor(r), orgID, contractorID)
	}
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, contractorResponse(c))
}
