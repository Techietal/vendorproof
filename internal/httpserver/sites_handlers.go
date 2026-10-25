package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/engagements"
	"vendorproof/internal/httpx"
	"vendorproof/internal/optional"
	"vendorproof/internal/sites"
	"vendorproof/internal/validate"
)

type siteDTO struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
	IsActive       bool   `json:"isActive"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

func siteResponse(s sites.Site) siteDTO {
	return siteDTO{
		ID: s.ID.String(), OrganizationID: s.OrganizationID.String(), Name: s.Name,
		IsActive:  s.IsActive,
		CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request) {
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
	site, err := s.deps.Sites.Create(r.Context(), actor(r), orgID, body.Name)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+orgID.String()+"/sites/"+site.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, siteResponse(site))
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
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
	items, next, err := s.deps.Sites.List(r.Context(), actor(r), orgID,
		sites.Filter{Active: active, Search: r.URL.Query().Get("q")}, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]siteDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, siteResponse(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"sites": dtos, "nextCursor": emptyToNil(next)})
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	siteID, ok := pathUUID(w, r, "siteId")
	if !ok {
		return
	}
	site, err := s.deps.Sites.Get(r.Context(), actor(r), orgID, siteID)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, siteResponse(site))
}

func (s *Server) updateSite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	siteID, ok := pathUUID(w, r, "siteId")
	if !ok {
		return
	}
	var body struct {
		Name optional.Value[string] `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	if !body.Name.Set {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{"name": "is required"})
		return
	}
	site, err := s.deps.Sites.Update(r.Context(), actor(r), orgID, siteID, body.Name.Value)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, siteResponse(site))
}

func (s *Server) deactivateSite(w http.ResponseWriter, r *http.Request) {
	s.siteTransition(w, r, false)
}

func (s *Server) reactivateSite(w http.ResponseWriter, r *http.Request) {
	s.siteTransition(w, r, true)
}

func (s *Server) siteTransition(w http.ResponseWriter, r *http.Request, activate bool) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	siteID, ok := pathUUID(w, r, "siteId")
	if !ok {
		return
	}
	var site sites.Site
	var err error
	if activate {
		site, err = s.deps.Sites.Reactivate(r.Context(), actor(r), orgID, siteID)
	} else {
		site, err = s.deps.Sites.Deactivate(r.Context(), actor(r), orgID, siteID)
	}
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, siteResponse(site))
}

// queryBool parses an optional boolean query parameter.
func queryBool(w http.ResponseWriter, r *http.Request, name string) (*bool, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{name: "must be true or false"})
		return nil, false
	}
	return &value, true
}

// queryUUID parses an optional UUID query filter.
func queryUUID(w http.ResponseWriter, r *http.Request, name string) (*uuid.UUID, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{name: "must be a UUID"})
		return nil, false
	}
	return &id, true
}

// queryAsOf parses the documented evaluation-date query parameter, defaulting
// to the server UTC calendar date.
func queryAsOf(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	raw := r.URL.Query().Get("asOf")
	if raw == "" {
		return validate.Today(time.Now()), true
	}
	date, err := validate.Date("asOf", raw)
	if err != nil {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{"asOf": "must be a valid YYYY-MM-DD date"})
		return time.Time{}, false
	}
	return date, true
}

func stateOf(eval time.Time, e engagements.Engagement) engagements.State {
	return engagements.StateAt(eval, e.ActiveFrom, e.ActiveUntil)
}
