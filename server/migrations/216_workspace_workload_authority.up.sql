-- Server-owned authority projection for signed delegated-session workloads.
--
-- The workspace UUID is the stable logical Team Identity. It is deliberately
-- independent of display names, roles, individual Agents, and Squad labels.
-- The policy class is assigned only by this server-side migration/trigger; it
-- is consumed by target-local AGS Team Bindings.
CREATE TABLE workspace_workload_authority (
    workspace_id UUID PRIMARY KEY REFERENCES workspace(id) ON DELETE CASCADE,
    team_identity_id UUID NOT NULL,
    membership_epoch BIGINT NOT NULL CHECK (membership_epoch > 0),
    policy_class TEXT NOT NULL CHECK (policy_class = 'multica.workspace.default.v1'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
SELECT id, id, 1, 'multica.workspace.default.v1'
FROM workspace;

CREATE FUNCTION ensure_workspace_workload_authority()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
    VALUES (NEW.id, NEW.id, 1, 'multica.workspace.default.v1');
    RETURN NEW;
END;
$$;

CREATE TRIGGER workspace_workload_authority_on_workspace_create
AFTER INSERT ON workspace
FOR EACH ROW
EXECUTE FUNCTION ensure_workspace_workload_authority();

CREATE FUNCTION advance_workspace_workload_membership_epoch()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_workspace_id UUID;
BEGIN
    affected_workspace_id := COALESCE(NEW.workspace_id, OLD.workspace_id);

    -- The upsert handles isolated test/setup writers while keeping the source
    -- invariant server-owned and independent from caller-provided identity.
    INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
    VALUES (affected_workspace_id, affected_workspace_id, 1, 'multica.workspace.default.v1')
    ON CONFLICT (workspace_id) DO UPDATE
    SET membership_epoch = workspace_workload_authority.membership_epoch + 1,
        updated_at = now();

    RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE TRIGGER workspace_workload_authority_on_member_change
AFTER INSERT OR UPDATE OF role OR DELETE ON member
FOR EACH ROW
EXECUTE FUNCTION advance_workspace_workload_membership_epoch();
