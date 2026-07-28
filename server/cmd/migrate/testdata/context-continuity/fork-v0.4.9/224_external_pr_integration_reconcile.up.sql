-- Reconcile provider-neutral external PR facts from clean main, the historical
-- 135 layout, or fork/v0.4.8 without rewriting either migration ledger.
-- Relationships are application-owned: remove the historical foreign keys and
-- leave dependent cleanup to issue/workspace transactions.
--
-- The bounded lock timeout makes the ACCESS EXCLUSIVE ALTER fail closed on a
-- busy deployment. The migration remains safely retryable after traffic is
-- drained; it must never build an unbounded lock queue.
BEGIN;
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS external_pull_request_link (
    id                       UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id             UUID NOT NULL,
    issue_id                 UUID NOT NULL,
    provider                 TEXT NOT NULL,
    external_repo            TEXT NOT NULL,
    external_number          INTEGER NOT NULL,
    external_url             TEXT,
    merge_provider           TEXT,
    merge_repo               TEXT,
    merge_number             INTEGER,
    merge_url                TEXT,
    merged_sha               TEXT,
    link_confidence          TEXT NOT NULL DEFAULT 'authoritative'
        CHECK (link_confidence IN ('authoritative', 'inferred')),
    completion_intent        BOOLEAN NOT NULL DEFAULT TRUE,
    state                    TEXT NOT NULL DEFAULT 'open'
        CHECK (state IN ('open', 'draft', 'closed', 'merged')),
    idempotency_key          TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS external_pull_request_receipt (
    workspace_id    UUID NOT NULL,
    idempotency_key TEXT NOT NULL,
    payload_hash    TEXT NOT NULL,
    issue_id        UUID NOT NULL,
    provider        TEXT NOT NULL,
    external_repo   TEXT NOT NULL,
    external_number INTEGER NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE external_pull_request_link
    DROP CONSTRAINT IF EXISTS external_pull_request_link_workspace_id_fkey,
    DROP CONSTRAINT IF EXISTS external_pull_request_link_issue_id_fkey;

-- Provider facts and issue status transitions use one transaction-scoped lock
-- key. Provider completion takes this lock in a separate statement before its
-- aggregate read; the triggers below make every fact writer participate even
-- when it does not attempt terminal materialization itself.
CREATE OR REPLACE FUNCTION lock_issue_completion_transition(target_issue_id UUID)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(target_issue_id::text, 88492131));
END;
$$;

CREATE OR REPLACE FUNCTION lock_issue_completion_on_issue_status()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM lock_issue_completion_transition(NEW.id);
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS issue_completion_lock_on_status ON issue;
CREATE TRIGGER issue_completion_lock_on_status
BEFORE UPDATE OF status ON issue
FOR EACH ROW
WHEN (OLD.status IS DISTINCT FROM NEW.status)
EXECUTE FUNCTION lock_issue_completion_on_issue_status();

CREATE OR REPLACE FUNCTION lock_issue_completion_on_external_pr_fact()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_issue_id UUID;
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM lock_issue_completion_transition(NEW.issue_id);
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM lock_issue_completion_transition(OLD.issue_id);
        RETURN OLD;
    END IF;
    FOR target_issue_id IN
        SELECT value
        FROM (VALUES (OLD.issue_id), (NEW.issue_id)) AS ids(value)
        GROUP BY value
        ORDER BY value
    LOOP
        PERFORM lock_issue_completion_transition(target_issue_id);
    END LOOP;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS external_pr_link_completion_lock ON external_pull_request_link;
CREATE TRIGGER external_pr_link_completion_lock
BEFORE INSERT OR UPDATE OR DELETE ON external_pull_request_link
FOR EACH ROW
EXECUTE FUNCTION lock_issue_completion_on_external_pr_fact();

CREATE OR REPLACE FUNCTION lock_issue_completion_on_github_link()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_issue_id UUID;
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM lock_issue_completion_transition(NEW.issue_id);
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM lock_issue_completion_transition(OLD.issue_id);
        RETURN OLD;
    END IF;
    FOR target_issue_id IN
        SELECT value
        FROM (VALUES (OLD.issue_id), (NEW.issue_id)) AS ids(value)
        GROUP BY value
        ORDER BY value
    LOOP
        PERFORM lock_issue_completion_transition(target_issue_id);
    END LOOP;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS issue_pull_request_completion_lock ON issue_pull_request;
CREATE TRIGGER issue_pull_request_completion_lock
BEFORE INSERT OR UPDATE OR DELETE ON issue_pull_request
FOR EACH ROW
EXECUTE FUNCTION lock_issue_completion_on_github_link();

CREATE OR REPLACE FUNCTION lock_issue_completion_on_github_pr_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_issue_id UUID;
BEGIN
    FOR target_issue_id IN
        SELECT issue_id
        FROM issue_pull_request
        WHERE pull_request_id = NEW.id
        ORDER BY issue_id
    LOOP
        PERFORM lock_issue_completion_transition(target_issue_id);
    END LOOP;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS github_pull_request_completion_lock ON github_pull_request;
CREATE TRIGGER github_pull_request_completion_lock
BEFORE UPDATE OF state ON github_pull_request
FOR EACH ROW
WHEN (OLD.state IS DISTINCT FROM NEW.state)
EXECUTE FUNCTION lock_issue_completion_on_github_pr_state();

CREATE OR REPLACE FUNCTION lock_issue_completion_on_vcs_link()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_issue_id UUID;
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM lock_issue_completion_transition(NEW.issue_id);
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM lock_issue_completion_transition(OLD.issue_id);
        RETURN OLD;
    END IF;
    FOR target_issue_id IN
        SELECT value
        FROM (VALUES (OLD.issue_id), (NEW.issue_id)) AS ids(value)
        GROUP BY value
        ORDER BY value
    LOOP
        PERFORM lock_issue_completion_transition(target_issue_id);
    END LOOP;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS issue_vcs_pull_request_completion_lock ON issue_vcs_pull_request;
CREATE TRIGGER issue_vcs_pull_request_completion_lock
BEFORE INSERT OR UPDATE OR DELETE ON issue_vcs_pull_request
FOR EACH ROW
EXECUTE FUNCTION lock_issue_completion_on_vcs_link();

CREATE OR REPLACE FUNCTION lock_issue_completion_on_vcs_pr_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_issue_id UUID;
BEGIN
    FOR target_issue_id IN
        SELECT issue_id
        FROM issue_vcs_pull_request
        WHERE pull_request_id = NEW.id
        ORDER BY issue_id
    LOOP
        PERFORM lock_issue_completion_transition(target_issue_id);
    END LOOP;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS vcs_pull_request_completion_lock ON vcs_pull_request;
CREATE TRIGGER vcs_pull_request_completion_lock
BEFORE UPDATE OF state ON vcs_pull_request
FOR EACH ROW
WHEN (OLD.state IS DISTINCT FROM NEW.state)
EXECUTE FUNCTION lock_issue_completion_on_vcs_pr_state();

COMMIT;
