-- Replace migration 229's temporary update-only member trigger after migration
-- 230 has installed the unique workspace authority index. The member lock and
-- transactional DDL leave no trigger-free membership epoch window.
BEGIN;
SET LOCAL lock_timeout = '5s';
LOCK TABLE member IN SHARE ROW EXCLUSIVE MODE;

CREATE OR REPLACE FUNCTION advance_workspace_workload_membership_epoch()
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

    IF EXISTS (SELECT 1 FROM workspace WHERE id = affected_workspace_id) THEN
        INSERT INTO workspace_workload_authority (
            workspace_id, team_identity_id, membership_epoch, policy_class
        )
        VALUES (
            affected_workspace_id, affected_workspace_id, 1,
            'multica.workspace.default.v1'
        )
        ON CONFLICT (workspace_id) DO UPDATE
        SET membership_epoch = workspace_workload_authority.membership_epoch + 1,
            updated_at = now();
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;

DROP TRIGGER IF EXISTS workspace_workload_authority_on_member_change ON member;
CREATE TRIGGER workspace_workload_authority_on_member_change
AFTER INSERT OR UPDATE OF role OR DELETE ON member
FOR EACH ROW
EXECUTE FUNCTION advance_workspace_workload_membership_epoch();

DROP FUNCTION IF EXISTS advance_existing_workspace_workload_membership_epoch();

COMMIT;
