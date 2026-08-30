-- name: CompleteIssueFromPullRequest :one
-- One terminal materialization authority for GitHub, native VCS, and
-- provider-neutral external PR callbacks. The metadata predicate is exact and
-- repeated in the same statement as the write so a policy mutation between a
-- handler read and this update fails closed.
UPDATE issue AS i
SET status = 'done', updated_at = now()
WHERE i.id = sqlc.arg('id')
  AND i.workspace_id = sqlc.arg('workspace_id')
  AND i.status NOT IN ('done', 'cancelled')
  AND i.parent_issue_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM issue child WHERE child.parent_issue_id = i.id
  )
  AND (
      NOT (i.metadata ? 'external_pr_completion_policy')
      OR (
          jsonb_typeof(i.metadata -> 'external_pr_completion_policy') = 'string'
          AND i.metadata ->> 'external_pr_completion_policy' IN ('', 'leaf_child_only')
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM github_pull_request pr
      JOIN issue_pull_request link ON link.pull_request_id = pr.id
      WHERE link.issue_id = i.id
        AND NOT link.reference_only
        AND pr.state IN ('open', 'draft')
      UNION ALL
      SELECT 1
      FROM vcs_pull_request pr
      JOIN issue_vcs_pull_request link ON link.pull_request_id = pr.id
      WHERE link.issue_id = i.id
        AND NOT link.reference_only
        AND pr.state IN ('open', 'draft')
      UNION ALL
      SELECT 1
      FROM external_pull_request_link pr
      WHERE pr.workspace_id = i.workspace_id
        AND pr.issue_id = i.id
        AND pr.link_confidence = 'authoritative'
        AND pr.state IN ('open', 'draft')
  )
  AND EXISTS (
      SELECT 1
      FROM github_pull_request pr
      JOIN issue_pull_request link ON link.pull_request_id = pr.id
      WHERE link.issue_id = i.id
        AND NOT link.reference_only
        AND link.close_intent
        AND pr.state = 'merged'
      UNION ALL
      SELECT 1
      FROM vcs_pull_request pr
      JOIN issue_vcs_pull_request link ON link.pull_request_id = pr.id
      WHERE link.issue_id = i.id
        AND NOT link.reference_only
        AND link.close_intent
        AND pr.state = 'merged'
      UNION ALL
      SELECT 1
      FROM external_pull_request_link pr
      WHERE pr.workspace_id = i.workspace_id
        AND pr.issue_id = i.id
        AND pr.link_confidence = 'authoritative'
        AND pr.completion_intent
        AND pr.state = 'merged'
  )
RETURNING i.*;
