-- Production rollback across the accepted floor restores the verified database
-- dump. This down file only supports disposable fresh-install tests.
BEGIN;
DROP TRIGGER IF EXISTS external_pr_workspace_delete ON workspace;
DROP TRIGGER IF EXISTS external_pr_issue_delete ON issue;
DROP FUNCTION IF EXISTS delete_external_pr_workspace_facts();
DROP FUNCTION IF EXISTS delete_external_pr_issue_facts();
DROP TABLE IF EXISTS external_pr_reconcile_work;
DROP TABLE IF EXISTS external_pull_request_receipt;
DROP TABLE IF EXISTS external_pull_request_link;
COMMIT;
