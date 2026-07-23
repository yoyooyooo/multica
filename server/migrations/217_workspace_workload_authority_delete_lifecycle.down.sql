CREATE OR REPLACE FUNCTION advance_workspace_workload_membership_epoch()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_workspace_id UUID;
BEGIN
    affected_workspace_id := COALESCE(NEW.workspace_id, OLD.workspace_id);

    INSERT INTO workspace_workload_authority (workspace_id, team_identity_id, membership_epoch, policy_class)
    VALUES (affected_workspace_id, affected_workspace_id, 1, 'multica.workspace.default.v1')
    ON CONFLICT (workspace_id) DO UPDATE
    SET membership_epoch = workspace_workload_authority.membership_epoch + 1,
        updated_at = now();

    RETURN COALESCE(NEW, OLD);
END;
$$;
