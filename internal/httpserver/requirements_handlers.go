package httpserver

import (
	"net/http"
	"time"

	"vendorproof/internal/httpx"
	"vendorproof/internal/requirements"
)

type requirementVersionDTO struct {
	ID                string  `json:"id"`
	OrganizationID    string  `json:"organizationId"`
	RequirementKey    string  `json:"requirementKey"`
	Version           int     `json:"version"`
	Name              string  `json:"name"`
	Cadence           string  `json:"cadence"`
	DueDay            *int    `json:"dueDay,omitempty"`
	EffectiveFrom     string  `json:"effectiveFrom"`
	EffectiveUntil    *string `json:"effectiveUntil,omitempty"`
	ReportingTimezone string  `json:"timezone"`
	CreatedAt         string  `json:"createdAt"`
}

func versionDTO(rv requirements.RequirementVersion) requirementVersionDTO {
	dto := requirementVersionDTO{
		ID:                rv.ID.String(),
		OrganizationID:    rv.OrganizationID.String(),
		RequirementKey:    rv.RequirementKey.String(),
		Version:           rv.Version,
		Name:              rv.Name,
		Cadence:           string(rv.Cadence),
		EffectiveFrom:     rv.EffectiveFrom.Format("2006-01-02"),
		ReportingTimezone: rv.ReportingTimezone,
		CreatedAt:         rv.CreatedAt.UTC().Format(time.RFC3339),
	}
	if rv.EffectiveUntil != nil {
		v := rv.EffectiveUntil.Format("2006-01-02")
		dto.EffectiveUntil = &v
	}
	if rv.Cadence == requirements.CadenceMonthly && rv.DueDay > 0 {
		dto.DueDay = &rv.DueDay
	}
	return dto
}

func (s *Server) createRequirement(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	var body map[string]any
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	// Parse required fields.
	name, ok := body["name"].(string)
	if !ok {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{"name": "is required"})
		return
	}
	cadence, ok := body["cadence"].(string)
	if !ok {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{"cadence": "is required"})
		return
	}
	input := requirements.CreateInput{
		Name:              name,
		Cadence:           cadence,
		ReportingTimezone: "UTC",
	}
	if tz, ok := body["timezone"].(string); ok && tz != "" {
		input.ReportingTimezone = tz
	}
	if from, ok := body["effectiveFrom"].(string); ok {
		input.EffectiveFrom = from
	}
	if until, ok := body["effectiveUntil"].(string); ok && until != "" {
		input.EffectiveUntil = &until
	}
	if dueDay, ok := body["dueDay"]; ok {
		if f, ok := dueDay.(float64); ok {
			d := int(f)
			input.DueDay = &d
		}
	}

	rv, err := s.deps.Requirements.Create(r.Context(), actor(r), orgID, input)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+orgID.String()+"/requirements/"+rv.RequirementKey.String())
	httpx.WriteJSON(w, r, http.StatusCreated, versionDTO(rv))
}

func (s *Server) listRequirements(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, next, err := s.deps.Requirements.ListFamilies(r.Context(), actor(r), orgID, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]map[string]any, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, map[string]any{
			"id":             item.ID.String(),
			"organizationId": item.OrganizationID.String(),
			"createdAt":      item.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"requirements": dtos, "nextCursor": emptyToNil(next)})
}

func (s *Server) getRequirement(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	requirementKey, ok := pathUUID(w, r, "requirementKey")
	if !ok {
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, next, err := s.deps.Requirements.ListVersions(r.Context(), actor(r), orgID, requirementKey, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]requirementVersionDTO, 0, len(items))
	for _, v := range items {
		dtos = append(dtos, versionDTO(v))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"versions":   dtos,
		"nextCursor": emptyToNil(next),
	})
}

func (s *Server) publishVersion(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	requirementKey, ok := pathUUID(w, r, "requirementKey")
	if !ok {
		return
	}
	var body map[string]any
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	input := requirements.PublishInput{}
	if name, ok := body["name"].(string); ok {
		input.Name = name
	}
	if cadence, ok := body["cadence"].(string); ok {
		input.Cadence = cadence
	}
	if tz, ok := body["timezone"].(string); ok && tz != "" {
		input.ReportingTimezone = tz
	}
	if from, ok := body["effectiveFrom"].(string); ok {
		input.EffectiveFrom = from
	}
	if until, ok := body["effectiveUntil"].(string); ok && until != "" {
		input.EffectiveUntil = &until
	}
	if dueDay, ok := body["dueDay"]; ok {
		if f, ok := dueDay.(float64); ok {
			d := int(f)
			input.DueDay = &d
		}
	}

	rv, err := s.deps.Requirements.PublishVersionByKey(r.Context(), actor(r), orgID, requirementKey, input)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+orgID.String()+"/requirement-versions/"+rv.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, versionDTO(rv))
}

func (s *Server) getRequirementVersion(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	versionID, ok := pathUUID(w, r, "versionId")
	if !ok {
		return
	}
	rv, err := s.deps.Requirements.GetVersion(r.Context(), actor(r), orgID, versionID)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, versionDTO(rv))
}
