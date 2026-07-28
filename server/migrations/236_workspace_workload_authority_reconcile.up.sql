-- Reconcile the server-owned Team authority projection without rewriting the
-- fork/v0.4.8 ledger. Workspace and member writers are held while existing rows
-- are backfilled and both lifecycle triggers are installed.
--
-- A bounded lock timeout prevents an unbounded deployment lock queue. On a busy
-- database this migration fails before changing the ledger and is safe to retry
-- after the owning operator drains writes.
BEGIN;
SET LOCAL lock_timeout = '5s';

LOCK TABLE workspace IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE member IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE IF NOT EXISTS workspace_workload_authority (
    workspace_id UUID NOT NULL,
    team_identity_id UUID NOT NULL,
    membership_epoch BIGINT NOT NULL CHECK (membership_epoch > 0),
    policy_class TEXT NOT NULL CHECK (policy_class = 'multica.workspace.default.v1'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE workspace_workload_authority
    DROP CONSTRAINT IF EXISTS workspace_workload_authority_workspace_id_fkey,
    DROP CONSTRAINT IF EXISTS workspace_workload_authority_team_identity_id_fkey;

INSERT INTO workspace_workload_authority (
    workspace_id, team_identity_id, membership_epoch, policy_class
)
SELECT w.id, w.id, 1, 'multica.workspace.default.v1'
FROM workspace w
WHERE NOT EXISTS (
    SELECT 1
    FROM workspace_workload_authority authority
    WHERE authority.workspace_id = w.id
);

CREATE OR REPLACE FUNCTION ensure_workspace_workload_authority()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO workspace_workload_authority (
        workspace_id, team_identity_id, membership_epoch, policy_class
    )
    SELECT NEW.id, NEW.id, 1, 'multica.workspace.default.v1'
    WHERE NOT EXISTS (
        SELECT 1 FROM workspace_workload_authority WHERE workspace_id = NEW.id
    );
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS workspace_workload_authority_on_workspace_create ON workspace;
CREATE TRIGGER workspace_workload_authority_on_workspace_create
AFTER INSERT ON workspace
FOR EACH ROW
EXECUTE FUNCTION ensure_workspace_workload_authority();

-- Migration 237 builds the workspace-id unique index concurrently and therefore
-- cannot share this transaction. Install a temporary member trigger before
-- releasing the member lock so changes during that index build are retained.
-- It updates only rows backfilled above; migration 238 atomically replaces it
-- with the ON CONFLICT lifecycle function once uniqueness is available.
CREATE OR REPLACE FUNCTION advance_existing_workspace_workload_membership_epoch()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_workspace_id UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        affected_workspace_id := OLD.workspace_id;
    ELSE
        affected_workspace_id := NEW.workspace_id;
    END IF;

    UPDATE workspace_workload_authority
    SET membership_epoch = membership_epoch + 1,
        updated_at = now()
    WHERE workspace_id = affected_workspace_id;

    RETURN COALESCE(NEW, OLD);
END;
$$;

DROP TRIGGER IF EXISTS workspace_workload_authority_on_member_change ON member;
CREATE TRIGGER workspace_workload_authority_on_member_change
AFTER INSERT OR UPDATE OF role OR DELETE ON member
FOR EACH ROW
EXECUTE FUNCTION advance_existing_workspace_workload_membership_epoch();

COMMIT;
