package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// This expression is the sole source-revision authority for a persisted link.
// The receipt key is request-owned after T017, so the persisted terminal fact
// itself supplies the stable revision: link identity + terminal/completion
// fields. Keep this expression in sync with the source sweep SQL.
const externalPRLinkEffectiveRevisionExpr = `md5(concat_ws(chr(31), id::text, state, COALESCE(merged_sha, ''), link_confidence, CASE WHEN completion_intent THEN 'true' ELSE 'false' END))`

// externalPRWorkIdentity identifies the durable work row created for a
// persisted link revision. It is carried into the completion finalization
// intent so a post-commit retry can find the exact replayable closure.
type externalPRWorkIdentity struct {
	ID             pgtype.UUID
	SourceRevision string
}

// enqueueExternalPRTerminalWork is the only continuation write seam. It is
// called by the existing external-PR fact transaction, after link/receipt/
// activity writes and before commit. No receiver or worker owns a second fact
// upsert path.
func (h *Handler) enqueueExternalPRTerminalWork(
	ctx context.Context,
	exec dbExecutor,
	workspaceID, issueID, linkID pgtype.UUID,
	provider, externalRepo string,
	externalNumber int32,
	sourceIdempotencyKey string,
) (externalPRWorkIdentity, error) {
	var out externalPRWorkIdentity
	if !linkID.Valid {
		return out, fmt.Errorf("external PR terminal work requires a persisted link identity")
	}

	if err := exec.QueryRow(ctx, `
SELECT `+externalPRLinkEffectiveRevisionExpr+`
FROM external_pull_request_link
WHERE workspace_id=$1 AND id=$2
FOR UPDATE`, workspaceID, linkID).Scan(&out.SourceRevision); err != nil {
		return out, fmt.Errorf("read persisted external PR source revision: %w", err)
	}
	out.SourceRevision = strings.TrimSpace(out.SourceRevision)
	if out.SourceRevision == "" {
		return out, fmt.Errorf("persisted external PR source revision is empty")
	}
	_, err := exec.Exec(ctx, `
INSERT INTO external_pr_reconcile_work (
    workspace_id, issue_id, link_id, kind, provider, external_repo,
    external_number, source_revision, source_idempotency_key, next_attempt_at
) VALUES ($1,$2,$3,'external_pr_terminal',$4,$5,$6,$7,$8,now())
ON CONFLICT (workspace_id, kind, link_id, source_revision) DO UPDATE SET
    provider=EXCLUDED.provider,
    external_repo=EXCLUDED.external_repo,
    external_number=EXCLUDED.external_number,
    source_idempotency_key=COALESCE(EXCLUDED.source_idempotency_key, external_pr_reconcile_work.source_idempotency_key),
    updated_at=now(),
    next_attempt_at=CASE
        WHEN external_pr_reconcile_work.state IN ('pending','retry_wait')
            THEN LEAST(external_pr_reconcile_work.next_attempt_at, now())
        ELSE external_pr_reconcile_work.next_attempt_at
    END`,
		workspaceID, issueID, linkID, provider, externalRepo, externalNumber,
		out.SourceRevision, nilIfBlank(sourceIdempotencyKey))
	if err != nil {
		return out, err
	}
	if err := exec.QueryRow(ctx, `
SELECT id
FROM external_pr_reconcile_work
WHERE workspace_id=$1 AND kind='external_pr_terminal'
  AND link_id=$2 AND source_revision=$3`, workspaceID, linkID, out.SourceRevision).Scan(&out.ID); err != nil {
		return out, fmt.Errorf("read external PR terminal work identity: %w", err)
	}
	return out, nil
}
