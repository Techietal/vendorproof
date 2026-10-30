// Package audit records append-only, minimal administrative facts. Records are
// written inside the same transaction as the business change.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/database"
	"vendorproof/internal/identity"
)

const (
	ActionOrganizationCreated      = "organization.created"
	ActionOrganizationUpdated      = "organization.updated"
	ActionOrganizationBootstrapped = "organization.bootstrapped"
	ActionMembershipCreated        = "membership.created"
	ActionMembershipRoleChanged    = "membership.role_changed"
	ActionMembershipRevoked        = "membership.revoked"

	ActionSiteCreated           = "site.created"
	ActionSiteUpdated           = "site.updated"
	ActionSiteDeactivated       = "site.deactivated"
	ActionSiteReactivated       = "site.reactivated"
	ActionContractorCreated     = "contractor.created"
	ActionContractorUpdated     = "contractor.updated"
	ActionContractorDeactivated = "contractor.deactivated"
	ActionContractorReactivated = "contractor.reactivated"
	ActionEngagementCreated     = "engagement.created"
	ActionEngagementCorrected   = "engagement.corrected"
	ActionEngagementClosed      = "engagement.closed"

	ActionRequirementCreated                = "requirement.created"
	ActionRequirementVersionPublished       = "requirement.version_published"
	ActionRequirementAssignmentCreated      = "requirement_assignment.created"
	ActionRequirementAssignmentEnded        = "requirement_assignment.ended"
	ActionRequirementAssignmentTransitioned = "requirement_assignment.transitioned"
	ActionObligationGenerated               = "obligation.generated"

	ResourceOrganization = "organization"
	ResourceMembership   = "membership"
	ResourceSite         = "site"
	ResourceContractor   = "contractor"
	ResourceEngagement   = "engagement"
	ResourceRequirement  = "requirement"
	ResourceAssignment   = "requirement_assignment"
	ResourceObligation   = "obligation"
)

// Event is one audit row. Details must stay small and non-sensitive.
type Event struct {
	OrganizationID uuid.UUID
	Actor          identity.Identity
	Action         string
	ResourceType   string
	ResourceID     uuid.UUID
	Details        map[string]any
}

// Record inserts an event using the caller's transaction or pool.
func Record(ctx context.Context, q database.Querier, e Event) error {
	if e.OrganizationID == uuid.Nil || e.ResourceID == uuid.Nil ||
		e.Actor.Issuer == "" || e.Actor.Subject == "" || e.Action == "" || e.ResourceType == "" {
		return fmt.Errorf("incomplete audit event")
	}
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	_, err = q.Exec(ctx, `
		INSERT INTO audit_events
			(id, organization_id, actor_issuer, actor_subject, action, resource_type, resource_id, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		uuid.New(), e.OrganizationID, e.Actor.Issuer, e.Actor.Subject,
		e.Action, e.ResourceType, e.ResourceID, encoded, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}
