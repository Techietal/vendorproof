package httpserver

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/assignments"
	"vendorproof/internal/httpx"
)

type assignmentDTO struct {
	ID                   string  `json:"id"`
	OrganizationID       string  `json:"organizationId"`
	EngagementID         string  `json:"engagementId"`
	RequirementKey       string  `json:"requirementKey"`
	RequirementVersionID string  `json:"requirementVersionId"`
	EffectiveFrom        string  `json:"effectiveFrom"`
	EffectiveUntil       *string `json:"effectiveUntil,omitempty"`
	AssignedAt           string  `json:"assignedAt"`
}

func assignmentResponse(a assignments.Assignment) assignmentDTO {
	dto := assignmentDTO{
		ID:                   a.ID.String(),
		OrganizationID:       a.OrganizationID.String(),
		EngagementID:         a.EngagementID.String(),
		RequirementKey:       a.RequirementKey.String(),
		RequirementVersionID: a.RequirementVersionID.String(),
		EffectiveFrom:        a.EffectiveFrom.Format("2006-01-02"),
		AssignedAt:           a.AssignedAt.UTC().Format(time.RFC3339),
	}
	if a.EffectiveUntil != nil {
		v := a.EffectiveUntil.Format("2006-01-02")
		dto.EffectiveUntil = &v
	}
	return dto
}

func (s *Server) createAssignment(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	var body assignments.CreateInput
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	a, err := s.deps.Assignments.Create(r.Context(), actor(r), orgID, body)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+orgID.String()+"/requirement-assignments/"+a.ID.String())
	httpx.WriteJSON(w, r, http.StatusCreated, assignmentResponse(a))
}

func (s *Server) listAssignments(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	f := assignments.Filter{}
	if eid := r.URL.Query().Get("engagementId"); eid != "" {
		if id, err := uuid.Parse(eid); err == nil {
			f.EngagementID = &id
		}
	}
	if rk := r.URL.Query().Get("requirementKey"); rk != "" {
		if id, err := uuid.Parse(rk); err == nil {
			f.RequirementKey = &id
		}
	}
	items, next, err := s.deps.Assignments.List(r.Context(), actor(r), orgID, f, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]assignmentDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, assignmentResponse(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"assignments": dtos, "nextCursor": emptyToNil(next)})
}

func (s *Server) getAssignment(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	assignmentID, ok := pathUUID(w, r, "assignmentId")
	if !ok {
		return
	}
	a, err := s.deps.Assignments.Get(r.Context(), actor(r), orgID, assignmentID)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, assignmentResponse(a))
}

func (s *Server) endAssignment(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	assignmentID, ok := pathUUID(w, r, "assignmentId")
	if !ok {
		return
	}
	var body struct {
		EffectiveUntil string `json:"effectiveUntil"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	a, err := s.deps.Assignments.End(r.Context(), actor(r), orgID, assignmentID, body.EffectiveUntil)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, assignmentResponse(a))
}

func (s *Server) transitionAssignment(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	assignmentID, ok := pathUUID(w, r, "assignmentId")
	if !ok {
		return
	}
	var body assignments.TransitionInput
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	ended, created, err := s.deps.Assignments.Transition(r.Context(), actor(r), orgID, assignmentID, body)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"ended":   assignmentResponse(ended),
		"created": assignmentResponse(created),
	})
}
