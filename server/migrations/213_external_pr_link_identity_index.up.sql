CREATE UNIQUE INDEX CONCURRENTLY idx_external_pr_link_identity
    ON external_pull_request_link(workspace_id, provider, external_repo, external_number);
