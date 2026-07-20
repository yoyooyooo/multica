CREATE UNIQUE INDEX CONCURRENTLY coordination_scope_active_natural_idx
    ON coordination_scope (workspace_id, root_issue_id, workflow_profile_key)
    WHERE state = 'active';
