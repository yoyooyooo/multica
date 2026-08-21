-- T017: External PR core simplification.
-- Drop AGS projection/authority duplicate columns and link-row idempotency_key.
-- Keep External PR product fields, receipt table, and request-only target_instance fence in code.
BEGIN;
SET LOCAL lock_timeout = '5s';

ALTER TABLE external_pull_request_link
  DROP COLUMN IF EXISTS target_instance,
  DROP COLUMN IF EXISTS canonical_repository_id,
  DROP COLUMN IF EXISTS canonical_repository,
  DROP COLUMN IF EXISTS provider_binding_id,
  DROP COLUMN IF EXISTS provider_binding_revision,
  DROP COLUMN IF EXISTS provider_repository,
  DROP COLUMN IF EXISTS expected_head_sha,
  DROP COLUMN IF EXISTS expected_base_sha,
  DROP COLUMN IF EXISTS base_ref,
  DROP COLUMN IF EXISTS delegated_merge_method,
  DROP COLUMN IF EXISTS projection_facts_revision,
  DROP COLUMN IF EXISTS idempotency_key;

COMMIT;
