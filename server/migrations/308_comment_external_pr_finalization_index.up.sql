CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS comment_external_pr_finalization_key_uidx
    ON comment (finalization_key)
    WHERE finalization_key IS NOT NULL;
