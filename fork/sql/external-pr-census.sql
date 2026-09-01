WITH live AS (
    SELECT link.*
    FROM external_pull_request_link AS link
    JOIN workspace ON workspace.id = link.workspace_id
    JOIN issue ON issue.id = link.issue_id AND issue.workspace_id = link.workspace_id
), dispositions AS (
    SELECT
        id,
        workspace_id,
        issue_id,
        state,
        CASE
            WHEN provider = 'ags'
             AND link_confidence = 'authoritative'
             AND external_repo IS NOT NULL
             AND position('/' IN external_repo) > 0
             AND external_number > 0
             AND merge_provider IS NOT NULL
             AND merge_repo IS NOT NULL
             AND merge_number IS NOT NULL
             AND merge_url IS NOT NULL
            THEN 'keep_strict'
            ELSE 'preserve_read_only'
        END AS disposition,
        CASE
            WHEN merge_provider IS NULL OR merge_repo IS NULL OR merge_number IS NULL OR merge_url IS NULL
            THEN 'missing_merge_authority'
            ELSE NULL
        END AS reason
    FROM live
), summary AS (
    SELECT
        count(*) AS live,
        count(*) FILTER (WHERE disposition = 'keep_strict') AS keep_strict,
        count(*) FILTER (WHERE disposition = 'preserve_read_only') AS preserve_read_only
    FROM dispositions
), orphan_summary AS (
    SELECT count(*) AS excluded_orphan_historical
    FROM external_pull_request_link AS link
    LEFT JOIN workspace ON workspace.id = link.workspace_id
    LEFT JOIN issue ON issue.id = link.issue_id AND issue.workspace_id = link.workspace_id
    WHERE workspace.id IS NULL OR issue.id IS NULL
)
SELECT jsonb_pretty(jsonb_build_object(
    'schema', 'multica.external-pr-census.v1',
    'generated_at', to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
    'summary', jsonb_build_object(
        'live', summary.live,
        'keep_strict', summary.keep_strict,
        'preserve_read_only', summary.preserve_read_only,
        'excluded_orphan_historical', orphan_summary.excluded_orphan_historical
    ),
    'rows', COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'id', id,
            'workspace_id', workspace_id,
            'issue_id', issue_id,
            'state', state,
            'disposition', disposition,
            'reason', reason
        ) ORDER BY id)
        FROM dispositions
    ), '[]'::jsonb)
))
FROM summary CROSS JOIN orphan_summary;
