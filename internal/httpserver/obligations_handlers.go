package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/httpx"
	"vendorproof/internal/obligations"
)

type obligationDTO struct {
	ID                   string `json:"id"`
	OrganizationID       string `json:"organizationId"`
	EngagementID         string `json:"engagementId"`
	RequirementKey       string `json:"requirementKey"`
	RequirementVersionID string `json:"requirementVersionId"`
	PeriodStart          string `json:"periodStart"`
	PeriodEnd            string `json:"periodEnd"`
	CoverageStart        string `json:"coverageStart"`
	CoverageEnd          string `json:"coverageEnd"`
	DueAt                string `json:"dueAt"`
	State                string `json:"state"`
	ContractorOverdue    bool   `json:"contractorOverdue"`
	CreatedAt            string `json:"createdAt"`
}

func obligationResponse(ob obligations.Obligation, status obligations.DisplayStatus) obligationDTO {
	dto := obligationDTO{
		ID:                   ob.ID.String(),
		OrganizationID:       ob.OrganizationID.String(),
		EngagementID:         ob.EngagementID.String(),
		RequirementKey:       ob.RequirementKey.String(),
		RequirementVersionID: ob.RequirementVersionID.String(),
		PeriodStart:          ob.PeriodStart.Format("2006-01-02"),
		PeriodEnd:            ob.PeriodEnd.Format("2006-01-02"),
		CoverageStart:        ob.CoverageStart.Format("2006-01-02"),
		CoverageEnd:          ob.CoverageEnd.Format("2006-01-02"),
		DueAt:                ob.DueAt.UTC().Format(time.RFC3339),
		State:                string(status.ReviewState),
		ContractorOverdue:    status.ContractorOverdue,
		CreatedAt:            ob.CreatedAt.UTC().Format(time.RFC3339),
	}
	return dto
}

func (s *Server) generateObligations(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	var body struct {
		Year   int  `json:"year"`
		Month  int  `json:"month"`
		DryRun bool `json:"dryRun"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.WriteDecodeError(w, r, err)
		return
	}
	if body.Year < 2000 || body.Year > 2200 {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{"year": "must be between 2000 and 2200"})
		return
	}
	if body.Month < 1 || body.Month > 12 {
		httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
			map[string]string{"month": "must be between 1 and 12"})
		return
	}

	result, err := s.deps.Obligations.GenerateForMonth(r.Context(), actor(r), orgID, body.Year, body.Month, body.DryRun)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{
		"periodStart": result.PeriodStart.Format("2006-01-02"),
		"periodEnd":   result.PeriodEnd.Format("2006-01-02"),
		"created":     result.Created,
		"existing":    result.Existing,
		"conflicts":   result.Conflicts,
		"skipped":     result.Skipped,
	})
}

func (s *Server) listObligations(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	f := obligations.ListFilter{
		AsOf: time.Now().UTC(),
	}
	if v := r.URL.Query().Get("asOf"); v != "" {
		date, err := parseDateSafe(v)
		if err != nil {
			httpx.WriteProblemWithErrors(w, r, http.StatusBadRequest, "Validation failed.",
				map[string]string{"asOf": "must be a valid YYYY-MM-DD date"})
			return
		}
		f.AsOf = date
	}
	if cid := r.URL.Query().Get("contractorId"); cid != "" {
		if id, err := uuid.Parse(cid); err == nil {
			f.ContractorID = &id
		}
	}
	if sid := r.URL.Query().Get("siteId"); sid != "" {
		if id, err := uuid.Parse(sid); err == nil {
			f.SiteID = &id
		}
	}
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
	if rvid := r.URL.Query().Get("requirementVersionId"); rvid != "" {
		if id, err := uuid.Parse(rvid); err == nil {
			f.RequirementVersionID = &id
		}
	}
	if ps := r.URL.Query().Get("period"); ps != "" {
		f.PeriodStart = &ps
	}
	if da := r.URL.Query().Get("dueAfter"); da != "" {
		f.DueAfter = &da
	}
	if db := r.URL.Query().Get("dueBefore"); db != "" {
		f.DueBefore = &db
	}
	if state := r.URL.Query().Get("state"); state != "" {
		rs := obligations.ReviewState(state)
		f.State = &rs
	}
	if ov := r.URL.Query().Get("overdueOnly"); ov != "" {
		if b, err := strconv.ParseBool(ov); err == nil {
			f.OverdueOnly = b
		}
	}

	items, statuses, next, err := s.deps.Obligations.List(r.Context(), actor(r), orgID, f, limit, cursor)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	dtos := make([]obligationDTO, 0, len(items))
	for i, item := range items {
		dtos = append(dtos, obligationResponse(item, statuses[i]))
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"obligations": dtos, "nextCursor": emptyToNil(next)})
}

func (s *Server) getObligation(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "organizationId")
	if !ok {
		return
	}
	obligationID, ok := pathUUID(w, r, "obligationId")
	if !ok {
		return
	}
	ob, status, err := s.deps.Obligations.Get(r.Context(), actor(r), orgID, obligationID)
	if err != nil {
		s.writeServiceError(w, r, err, "conflict")
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, obligationResponse(ob, status))
}

func parseDateSafe(raw string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, nil
	}
	return time.Time{}, formatErr()
}

func formatErr() error { return &simpleError{} }

type simpleError struct{}

func (e *simpleError) Error() string { return "parse error" }
