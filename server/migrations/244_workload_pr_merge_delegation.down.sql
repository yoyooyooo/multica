DROP TABLE workload_pr_merge_delegation_event;
DROP TABLE workload_pr_merge_delegation;
ALTER TABLE external_pull_request_link
    DROP COLUMN projection_facts_revision,
    DROP COLUMN delegated_merge_method,
    DROP COLUMN base_ref,
    DROP COLUMN expected_base_sha,
    DROP COLUMN expected_head_sha,
    DROP COLUMN provider_repository,
    DROP COLUMN provider_binding_revision,
    DROP COLUMN provider_binding_id,
    DROP COLUMN canonical_repository,
    DROP COLUMN canonical_repository_id,
    DROP COLUMN target_instance;
ALTER TABLE task_token DROP COLUMN execution_id;
ALTER TABLE agent_task_queue DROP COLUMN execution_id;
