CREATE INDEX CONCURRENTLY coordination_scope_workspace_root_idx
    ON coordination_scope (workspace_id, root_issue_id);
