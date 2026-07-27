-- A workspace delete cascades through member after the parent workspace row
-- is no longer a valid target. Do not recreate authority for that teardown
-- path; ordinary member mutations still advance the server-owned epoch.
CREATE FUNCTION advance_workspace_workload_membership_epoch()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_workspace_id UUID;
BEGIN
    affected_workspace_id := COALESCE(NEW.workspace_id, OLD.workspace_id);

    IF EXISTS (SELECT 1 FROM workspace WHERE id = affected_workspace_id) THEN
        -- The unique index from migration 217 makes this upsert safe for
        -- concurrent member mutations while preserving server-owned identity.
        INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
        VALUES (affected_workspace_id, affected_workspace_id, 1, 'multica.workspace.default.v1')
        ON CONFLICT (workspace_id) DO UPDATE
        SET membership_epoch = workspace_workload_authority.membership_epoch + 1,
            updated_at = now();
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE TRIGGER workspace_workload_authority_on_member_change
AFTER INSERT OR UPDATE OF role OR DELETE ON member
FOR EACH ROW
EXECUTE FUNCTION advance_workspace_workload_membership_epoch();
