-- Server-owned authority projection for signed delegated-session workloads.
--
-- The workspace UUID is the stable logical Team Identity. It is deliberately
-- independent of display names, roles, individual Agents, and Squad labels.
-- The policy class is assigned only by this server-side migration/trigger; it
-- is consumed by target-local AGS Team Bindings.
--
-- Keep workspace writers out until the backfill and creation trigger commit as
-- one unit. The migration-runner advisory lock only serializes runners, not
-- application INSERTs; SHARE ROW EXCLUSIVE conflicts with their ROW EXCLUSIVE
-- lock. A writer that commits before this lock is acquired is backfilled, and a
-- writer that arrives later waits for the trigger.
BEGIN;

LOCK TABLE workspace IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE workspace_workload_authority (
    -- This is intentionally a plain UUID. Workspace teardown deletes this row
    -- explicitly in the application transaction; migrations must not add FKs
    -- or cascades.
    workspace_id UUID NOT NULL,
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

COMMIT;
