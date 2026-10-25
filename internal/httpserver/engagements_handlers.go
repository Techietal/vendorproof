package httpserver

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/engagements"
	"vendorproof/internal/httpx"
	"vendorproof/internal/validate"
)

type engagementDTO struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organizationId"`
	ContractorID   string  `json:"contractorId"`
	SiteID         string  `json:"siteId"`
	ActiveFrom     string  `json:"activeFrom"`
	ActiveUntil    *string `json:"activeUntil"`
	State          string  `json:"state"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

func engagementResponse(e engagements.Engagement, state engagements.State) engagementDTO {
	dto := engagementDTO{
		ID: e.ID.String(), OrganizationID: e.OrganizationID.String(),
		ContractorID: e.ContractorID.String(), SiteID: e.SiteID.String(),
		ActiveFrom: engagements.FormatDate(e.ActiveFrom),
		State:      string(state),
		CreatedAt:  e.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  e.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if e.ActiveUntil != nil {
		v := engagements.FormatDate(*e.ActiveUntil)
		dto.ActiveUntil = &v
	}
	return dto
}

func (s *Server) createEngagement(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	var body struct {
		ContractorID string  `json:"contractorId"`
		SiteID       string  `json:"siteId"`
		ActiveFrom   string  `json:"activeFrom"`
		ActiveUntil  *string `json:"activeUntil"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	contractorID, siteID, from, until, fields := parseEngagementInput(body.ContractorID, body.SiteID, body.ActiveFrom, body.ActiveUntil)
	if len(fields) > 0 {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.", fields)
		return
	}
	created, err := s.deps.Engagements.Create(r.Context(), actor(r), orgID, engagements.Creation{
		ContractorID: contractorID, SiteID: siteID, ActiveFrom: from, ActiveUntil: until,
	})
	if err != nil {
		s.writeServiceError(w, r, err, "Engagement period overlaps an existing engagement for this contractor and site.")
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+orgID.String()+"/engagements/"+created.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, engagementResponse(created, engagements.StateAt(validate.Today(s.now()), created.ActiveFrom, created.ActiveUntil)))
}

func parseEngagementInput(rawContractor, rawSite, rawFrom string, rawUntil *string) (uuid.UUID, uuid.UUID, time.Time, *time.Time, map[string]string) {
	fields := map[string]string{}
	contractorID, err := uuid.Parse(rawContractor)
	if err != nil {
		fields["contractorId"] = "must be a UUID"
	}
	siteID, err := uuid.Parse(rawSite)
	if err != nil {
		fields["siteId"] = "must be a UUID"
	}
	from, err := validate.Date("activeFrom", rawFrom)
	if err != nil {
		fields["activeFrom"] = "must be a valid YYYY-MM-DD date"
	}
	var until *time.Time
	if rawUntil != nil {
		parsed, err := validate.Date("activeUntil", *rawUntil)
		if err != nil {
			fields["activeUntil"] = "must be a valid YYYY-MM-DD date"
		} else {
			until = &parsed
		}
	}
	if len(fields) > 0 {
		return uuid.Nil, uuid.Nil, time.Time{}, nil, fields
	}
	if until != nil && !until.After(from) {
		return uuid.Nil, uuid.Nil, time.Time{}, nil, map[string]string{"activeUntil": "must be later than activeFrom"}
	}
	return contractorID, siteID, from, until, nil
}

func (s *Server) listEngagements(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	contractorID, ok := queryUUID(w, r, "contractorId")
	if !ok {
		return
	}
	siteID, ok := queryUUID(w, r, "siteId")
	if !ok {
		return
	}
	state, err := engagements.ParseState(r.URL.Query().Get("state"))
	if err != nil {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{"state": "must be scheduled, active, or ended"})
		return
	}
	asOf, ok := queryAsOf(w, r)
	if !ok {
		return
	}
	items, states, eval, next, err := s.deps.Engagements.List(r.Context(), actor(r), orgID, engagements.Filter{
		ContractorID: contractorID, SiteID: siteID, State: state, AsOf: asOf,
	}, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]engagementDTO, 0, len(items))
	for i, item := range items {
		dtos = append(dtos, engagementResponse(item, states[i]))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"engagements": dtos,
		"asOf":        engagements.FormatDate(eval),
		"nextCursor":  emptyToNil(next),
	})
}

func (s *Server) getEngagement(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	engagementID, ok := pathUUID(w, r, "engagementId")
	if !ok {
		return
	}
	e, err := s.deps.Engagements.Get(r.Context(), actor(r), orgID, engagementID)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, engagementResponse(e, engagements.StateAt(validate.Today(s.now()), e.ActiveFrom, e.ActiveUntil)))
}

func (s *Server) correctEngagement(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	engagementID, ok := pathUUID(w, r, "engagementId")
	if !ok {
		return
	}
	var body engagements.Patch
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	e, err := s.deps.Engagements.Correct(r.Context(), actor(r), orgID, engagementID, body)
	if err != nil {
		s.writeServiceError(w, r, err, "Corrected period overlaps another engagement for this contractor and site.")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, engagementResponse(e, engagements.StateAt(validate.Today(s.now()), e.ActiveFrom, e.ActiveUntil)))
}

func (s *Server) closeEngagement(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	engagementID, ok := pathUUID(w, r, "engagementId")
	if !ok {
		return
	}
	var body struct {
		ActiveUntil string `json:"activeUntil"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	e, err := s.deps.Engagements.Close(r.Context(), actor(r), orgID, engagementID, body.ActiveUntil)
	if err != nil {
		s.writeServiceError(w, r, err, "The engagement is already closed with a different date; use PATCH to correct it.")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, engagementResponse(e, engagements.StateAt(validate.Today(s.now()), e.ActiveFrom, e.ActiveUntil)))
}
