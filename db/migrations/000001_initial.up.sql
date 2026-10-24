-- First-pass relational model. Application identity and authorization APIs are not
-- implemented by this starter. All tenant-owned objects include organization_id.
CREATE TABLE organizations (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (btrim(name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE memberships (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    identity_subject text NOT NULL,
    role text NOT NULL CHECK (role IN ('owner', 'manager', 'reviewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, identity_subject)
);

CREATE TABLE sites (
    id uuid NOT NULL,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (btrim(name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    UNIQUE (organization_id, id)
);

CREATE TABLE contractors (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (btrim(name) <> ''),
    contact_email text,
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE TABLE engagements (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    contractor_id uuid NOT NULL,
    site_id uuid NOT NULL,
    active_from date NOT NULL,
    active_until date,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, contractor_id) REFERENCES contractors(organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, site_id) REFERENCES sites(organization_id, id) ON DELETE RESTRICT,
    CHECK (active_until IS NULL OR active_until > active_from)
);

CREATE TABLE requirement_versions (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    requirement_key uuid NOT NULL,
    version integer NOT NULL CHECK (version > 0),
    name text NOT NULL CHECK (btrim(name) <> ''),
    cadence text NOT NULL CHECK (cadence IN ('monthly', 'expiry')),
    due_day integer CHECK (due_day BETWEEN 1 AND 31),
    effective_from date NOT NULL,
    effective_until date,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, requirement_key, version),
    CHECK (effective_until IS NULL OR effective_until > effective_from),
    CHECK ((cadence = 'monthly' AND due_day IS NOT NULL) OR (cadence = 'expiry' AND due_day IS NULL))
);

CREATE TABLE requirement_assignments (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    engagement_id uuid NOT NULL,
    requirement_version_id uuid NOT NULL,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, engagement_id, requirement_version_id),
    FOREIGN KEY (organization_id, engagement_id) REFERENCES engagements(organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, requirement_version_id) REFERENCES requirement_versions(organization_id, id) ON DELETE RESTRICT
);

CREATE TABLE obligations (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    engagement_id uuid NOT NULL,
    requirement_version_id uuid NOT NULL,
    period_start date NOT NULL,
    period_end date NOT NULL,
    due_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, engagement_id, requirement_version_id, period_start),
    FOREIGN KEY (organization_id, engagement_id) REFERENCES engagements(organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, requirement_version_id) REFERENCES requirement_versions(organization_id, id) ON DELETE RESTRICT,
    CHECK (period_end > period_start)
);

CREATE TABLE documents (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    object_key text NOT NULL UNIQUE,
    file_name text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    sha256 char(64),
    scan_status text NOT NULL DEFAULT 'pending' CHECK (scan_status IN ('pending', 'clean', 'blocked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE TABLE submissions (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    obligation_id uuid NOT NULL,
    document_id uuid NOT NULL,
    attempt integer NOT NULL CHECK (attempt >= 1),
    state text NOT NULL DEFAULT 'awaiting_review' CHECK (state IN ('awaiting_review', 'needs_correction', 'accepted')),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, obligation_id, attempt),
    FOREIGN KEY (organization_id, obligation_id) REFERENCES obligations(organization_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (organization_id, document_id) REFERENCES documents(organization_id, id) ON DELETE RESTRICT
);

CREATE TABLE reviews (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    submission_id uuid NOT NULL,
    identity_subject text NOT NULL,
    decision text NOT NULL CHECK (decision IN ('accepted', 'needs_correction')),
    notes text,
    reviewed_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, submission_id) REFERENCES submissions(organization_id, id) ON DELETE RESTRICT
);

CREATE TABLE notification_deliveries (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    event_key text NOT NULL,
    destination text NOT NULL,
    status text NOT NULL CHECK (status IN ('queued', 'sent', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, event_key, destination)
);

CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    actor_subject text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id uuid NOT NULL,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX obligations_due ON obligations(organization_id, due_at);
CREATE INDEX obligations_engagement_period ON obligations(organization_id, engagement_id, period_start);
CREATE INDEX submissions_by_obligation ON submissions(organization_id, obligation_id, submitted_at DESC);
CREATE INDEX audit_events_by_org_time ON audit_events(organization_id, created_at DESC);
