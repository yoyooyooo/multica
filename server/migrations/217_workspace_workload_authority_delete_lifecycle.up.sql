-- A workspace delete cascades through member after the parent workspace row
-- is no longer a valid foreign-key target. Do not recreate authority for that
-- teardown path; ordinary member mutations still advance the server-owned epoch.
CREATE OR REPLACE FUNCTION advance_workspace_workload_membership_epoch()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_workspace_id UUID;
BEGIN
    affected_workspace_id := COALESCE(NEW.workspace_id, OLD.workspace_id);

    IF EXISTS (SELECT 1 FROM workspace WHERE id = affected_workspace_id) THEN
        -- The upsert handles isolated test/setup writers while keeping the source
        -- invariant server-owned and independent from caller-provided identity.
        INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
        VALUES (affected_workspace_id, affected_workspace_id, 1, 'multica.workspace.default.v1')
        ON CONFLICT (workspace_id) DO UPDATE
        SET membership_epoch = workspace_workload_authority.membership_epoch + 1,
            updated_at = now();
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$;
