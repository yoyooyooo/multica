package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/scheduler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestExternalPRReconcileErrorIsRedactedAndBounded(t *testing.T) {
	secret := "Bearer very-secret-token"
	message := strings.Repeat("provider failed "+secret+" ", 100)
	got := redactExternalPRReconcileError(errors.New(message))
	if strings.Contains(got, secret) || len(got) > 512 {
		t.Fatalf("redacted error leaked secret or exceeded bound: %q", got)
	}
}

func bindExternalPRFinalizationToCurrentGeneration(t *testing.T, intentID, issueID string) {
	t.Helper()
	ctx := context.Background()
	var activityID string
	err := testPool.QueryRow(ctx, `
SELECT id
FROM activity_log
WHERE issue_id=$1 AND action='status_changed' AND details->>'to'=(SELECT status FROM issue WHERE id=$1)
ORDER BY created_at DESC, id DESC
LIMIT 1`, issueID).Scan(&activityID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := testPool.QueryRow(ctx, `
INSERT INTO activity_log (workspace_id, issue_id, actor_type, action, details)
SELECT workspace_id, id, 'system', 'status_changed', jsonb_build_object('from', status, 'to', status, 'source', 'test_fixture')
FROM issue WHERE id=$1
RETURNING id`, issueID).Scan(&activityID); err != nil {
			t.Fatalf("seed current status activity: %v", err)
		}
	} else if err != nil {
		t.Fatalf("load current status activity: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
UPDATE external_pr_reconcile_finalization f
SET status_activity_id=$1,
    intended_parent_id=(SELECT parent_issue_id FROM issue WHERE id=$2)
WHERE f.id=$3`, activityID, issueID, intentID); err != nil {
		t.Fatalf("bind finalization generation: %v", err)
	}
}

func cleanupExternalPRReconcileIssueFixtures(t *testing.T, parentID, childID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id=$1 OR issue_id=$2`, parentID, childID)
		_, _ = testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id=$1 OR issue_id=$2`, parentID, childID)
		_, _ = testPool.Exec(ctx, `DELETE FROM activity_log WHERE issue_id=$1 OR issue_id=$2`, parentID, childID)
		_, _ = testPool.Exec(ctx, `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1 OR issue_id=$2`, parentID, childID)
		_, _ = testPool.Exec(ctx, `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1 OR issue_id=$2`, parentID, childID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, childID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, parentID)
	})
}

func TestExternalPRFinalizationSourceSweepRecoversWithoutWorkAndIsIdempotent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "finalization sweep parent", "in_progress", "", nil)
	child := createExternalPRTestIssue(t, "finalization sweep child", "done", parent, int32Ptr(1))
	cleanupExternalPRReconcileIssueFixtures(t, parent, child)
	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id=$1 ORDER BY created_at ASC LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load finalization test agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type='agent', assignee_id=$1 WHERE id=$2`, agentID, parent); err != nil {
		t.Fatalf("assign finalization parent: %v", err)
	}
	intentID := uuid.New()
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, work_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    next_attempt_at, max_attempts
) VALUES ($1,$2,$3,NULL,'finalization-sweep-restart','external_pr_terminal_reconcile',
          'in_progress','done',$4,'{}',now(),4)`, intentID, testWorkspaceID, child, uuid.New()); err != nil {
		t.Fatalf("seed finalization intent: %v", err)
	}
	bindExternalPRFinalizationToCurrentGeneration(t, intentID.String(), child)
	if got, err := testHandler.reconcileDueExternalPRFinalizations(ctx, testHandler.Queries, "finalizer-sweep-a"); err != nil || got != 1 {
		t.Fatalf("first finalization sweep=(%d,%v), want one claim", got, err)
	}
	var state string
	if err := testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_finalization WHERE id=$1`, intentID).Scan(&state); err != nil {
		t.Fatalf("read finalization state: %v", err)
	}
	if state != "succeeded" {
		t.Fatalf("finalization state=%q, want succeeded", state)
	}
	var comments, tasks int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&comments); err != nil {
		t.Fatalf("count finalization comments: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM agent_task_queue WHERE trigger_comment_id IN (SELECT id FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%')`, parent).Scan(&tasks); err != nil {
		t.Fatalf("count finalization tasks: %v", err)
	}
	if comments != 1 || tasks != 1 {
		t.Fatalf("finalization side effects=(comments=%d,tasks=%d), want (1,1)", comments, tasks)
	}
	if got, err := testHandler.reconcileDueExternalPRFinalizations(ctx, testHandler.Queries, "finalizer-sweep-b"); err != nil || got != 0 {
		t.Fatalf("replay finalization sweep=(%d,%v), want no due rows", got, err)
	}
	var commentsAfter, tasksAfter int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&commentsAfter); err != nil {
		t.Fatalf("count replay comments: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM agent_task_queue WHERE trigger_comment_id IN (SELECT id FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%')`, parent).Scan(&tasksAfter); err != nil {
		t.Fatalf("count replay tasks: %v", err)
	}
	if commentsAfter != comments || tasksAfter != tasks {
		t.Fatalf("replay changed side effects from (%d,%d) to (%d,%d)", comments, tasks, commentsAfter, tasksAfter)
	}
}

func TestExternalPRReconcileDefersLinkedWorkUntilFinalizationTerminal(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "linked finalization wait", "done", "", nil)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM external_pull_request_receipt WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM external_pull_request_link WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM issue WHERE id=$1`, issue)
	})
	request := externalPRCompletionReq(testWorkspaceID, issue, 9908)
	request.IdempotencyKey = "linked-finalization-wait-" + issue
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, request); err != nil {
		t.Fatalf("seed linked finalization work: %v", err)
	}
	var workID, revision string
	if err := testPool.QueryRow(ctx, `
SELECT w.id, w.source_revision
FROM external_pr_reconcile_work w
WHERE w.issue_id=$1`, issue).Scan(&workID, &revision); err != nil {
		t.Fatalf("read linked finalization work: %v", err)
	}
	intentID := uuid.New()
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, work_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    next_attempt_at, max_attempts
) VALUES ($1,$2,$3,$4,$5,'external_pr_terminal_reconcile','todo','done',$6,'{}',now(),4)`,
		intentID, testWorkspaceID, issue, workID, revision, uuid.New()); err != nil {
		t.Fatalf("seed linked finalization intent: %v", err)
	}
	bindExternalPRFinalizationToCurrentGeneration(t, intentID.String(), issue)
	intentDBID := pgtype.UUID{Bytes: intentID, Valid: true}
	if _, err := testHandler.Queries.ClaimExternalPRFinalizationByID(ctx, db.ClaimExternalPRFinalizationByIDParams{
		ID: intentDBID, LeaseOwner: pgtype.Text{String: "held-finalizer", Valid: true}, Secs: 90,
	}); err != nil {
		t.Fatalf("hold linked finalization lease: %v", err)
	}
	job := ExternalPRReconcileJob(testPool, testHandler)
	if _, err := job.Handler(ctx, scheduler.HandlerInput{RunnerID: "waiting-work"}); err != nil {
		t.Fatalf("linked finalization wait tick: %v", err)
	}
	var workState string
	var attempt int
	if err := testPool.QueryRow(ctx, `SELECT state, attempt FROM external_pr_reconcile_work WHERE id=$1`, workID).Scan(&workState, &attempt); err != nil {
		t.Fatalf("read deferred linked work: %v", err)
	}
	if workState != "retry_wait" || attempt != 0 {
		t.Fatalf("deferred linked work=(%q,attempt=%d), want retry_wait/0", workState, attempt)
	}
	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_finalization SET lease_expires_at=now()-interval '1 second', next_attempt_at=now() WHERE id=$1`, intentID); err != nil {
		t.Fatalf("expire held finalization lease: %v", err)
	}
	if _, err := job.Handler(ctx, scheduler.HandlerInput{RunnerID: "terminal-finalizer"}); err != nil {
		t.Fatalf("terminal finalization convergence tick: %v", err)
	}
	var finalizationState string
	if err := testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_finalization WHERE id=$1`, intentID).Scan(&finalizationState); err != nil {
		t.Fatalf("read terminal finalization state: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_work WHERE id=$1`, workID).Scan(&workState); err != nil {
		t.Fatalf("read converged linked work: %v", err)
	}
	if finalizationState != "recorded" || workState != "recorded" {
		t.Fatalf("terminal linked states=(%q,%q), want recorded/recorded", finalizationState, workState)
	}
}

func TestExternalPRReconcileDoesNotRematerializeConsumedSourceRevisionAfterReopen(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "reopen consumed source parent", "in_progress", "", nil)
	child := createExternalPRTestIssue(t, "reopen consumed source child", "todo", parent, int32Ptr(1))
	cleanupExternalPRReconcileIssueFixtures(t, parent, child)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_receipt WHERE issue_id=$1 OR issue_id=$2`, parent, child)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1 OR issue_id=$2`, parent, child)
	})

	var finalizationIssueEvents int
	testHandler.Bus.Subscribe(protocol.EventIssueUpdated, func(event events.Event) {
		payload, ok := event.Payload.(map[string]any)
		if ok && payload["source"] == "external_pr_terminal_reconcile" && payload["status_changed"] == true {
			finalizationIssueEvents++
		}
	})
	request := externalPRCompletionReq(testWorkspaceID, child, 9907)
	request.IdempotencyKey = "reopen-consumed-source-" + child
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, request); err != nil {
		t.Fatalf("seed merged external PR fact: %v", err)
	}

	job := ExternalPRReconcileJob(testPool, testHandler)
	if _, err := job.Handler(ctx, scheduler.HandlerInput{RunnerID: "reopen-consumed-source-first"}); err != nil {
		t.Fatalf("first reconcile worker: %v", err)
	}
	assertIssueStatus(t, child, "done")

	var workID, finalizationID string
	var workState, finalizationState string
	if err := testPool.QueryRow(ctx, `
SELECT w.id::text, w.state, f.id::text, f.state
FROM external_pr_reconcile_work w
JOIN external_pr_reconcile_finalization f ON f.work_id=w.id
WHERE w.issue_id=$1`, child).Scan(&workID, &workState, &finalizationID, &finalizationState); err != nil {
		t.Fatalf("read first materialization rows: %v", err)
	}
	if workState != "succeeded" || finalizationState != "succeeded" {
		t.Fatalf("first materialization states=(%q,%q), want succeeded/succeeded", workState, finalizationState)
	}
	if finalizationIssueEvents != 1 {
		t.Fatalf("initial finalization issue events=%d, want one", finalizationIssueEvents)
	}
	var activitiesBefore, parentCommentsBefore int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM activity_log WHERE issue_id=$1`, child).Scan(&activitiesBefore); err != nil {
		t.Fatalf("count first child activities: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&parentCommentsBefore); err != nil {
		t.Fatalf("count first parent comments: %v", err)
	}

	// Simulate a finalizer retry window after the status transaction committed,
	// then reopen the Issue before the same work is retried.
	if _, err := testPool.Exec(ctx, `
UPDATE external_pr_reconcile_finalization
SET state='retry_wait', attempt=1, next_attempt_at=now(), completed_at=NULL,
    lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL,
    last_error_code='injected_retry', last_redacted_error='injected retry fixture'
WHERE id=$1`, finalizationID); err != nil {
		t.Fatalf("seed finalization retry_wait: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
UPDATE external_pr_reconcile_work
SET state='pending', next_attempt_at=now(), completed_at=NULL,
    lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL
WHERE id=$1`, workID); err != nil {
		t.Fatalf("requeue consumed source work: %v", err)
	}
	updateIssueStatusViaHandler(t, child, "in_progress")
	assertIssueStatus(t, child, "in_progress")
	issueEventsAfterReopen := finalizationIssueEvents

	if err := testHandler.finalizePullRequestCompletionIntent(ctx, parseUUID(finalizationID)); err != nil {
		t.Fatalf("reopen finalizer replay: %v", err)
	}
	if _, err := job.Handler(ctx, scheduler.HandlerInput{RunnerID: "reopen-consumed-source-retry"}); err != nil {
		t.Fatalf("retry reconcile worker: %v", err)
	}
	assertIssueStatus(t, child, "in_progress")
	if finalizationIssueEvents != issueEventsAfterReopen {
		t.Fatalf("reopen finalization published %d additional issue events", finalizationIssueEvents-issueEventsAfterReopen)
	}

	var activitiesAfter, parentCommentsAfter, intents int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM activity_log WHERE issue_id=$1`, child).Scan(&activitiesAfter); err != nil {
		t.Fatalf("count retried child activities: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&parentCommentsAfter); err != nil {
		t.Fatalf("count retried parent comments: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM external_pr_reconcile_finalization WHERE work_id=$1`, workID).Scan(&intents); err != nil {
		t.Fatalf("count source finalization intents: %v", err)
	}
	if activitiesAfter != activitiesBefore || parentCommentsAfter != parentCommentsBefore || intents != 1 {
		t.Fatalf("reopen retry effects=(activities=%d/%d,parent_comments=%d/%d,intents=%d), want unchanged activities/comments and one intent", activitiesAfter, activitiesBefore, parentCommentsAfter, parentCommentsBefore, intents)
	}
	if err := testPool.QueryRow(ctx, `
SELECT w.state, f.state
FROM external_pr_reconcile_work w
JOIN external_pr_reconcile_finalization f ON f.work_id=w.id
WHERE w.id=$1`, workID).Scan(&workState, &finalizationState); err != nil {
		t.Fatalf("read retried materialization states: %v", err)
	}
	if workState != "recorded" || finalizationState != "recorded" {
		t.Fatalf("reopen retry states=(%q,%q), want recorded/recorded", workState, finalizationState)
	}
}

func TestExternalPRFinalizationConcurrentSweepClaimsOnce(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "concurrent finalization parent", "in_progress", "", nil)
	child := createExternalPRTestIssue(t, "concurrent finalization child", "done", parent, int32Ptr(1))
	cleanupExternalPRReconcileIssueFixtures(t, parent, child)
	intentID := uuid.New()
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    next_attempt_at
) VALUES ($1,$2,$3,'finalization-concurrent','external_pr_terminal_reconcile',
          'in_progress','done',$4,'{}',now())`, intentID, testWorkspaceID, child, uuid.New()); err != nil {
		t.Fatalf("seed concurrent finalization intent: %v", err)
	}
	bindExternalPRFinalizationToCurrentGeneration(t, intentID.String(), child)
	var wg sync.WaitGroup
	results := make(chan int64, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"finalizer-concurrent-a", "finalizer-concurrent-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			got, err := testHandler.reconcileDueExternalPRFinalizations(ctx, testHandler.Queries, owner)
			results <- got
			errs <- err
		}(owner)
	}
	wg.Wait()
	close(results)
	close(errs)
	var processed int64
	for got := range results {
		processed += got
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent finalization sweep: %v", err)
		}
	}
	if processed != 1 {
		t.Fatalf("concurrent sweeps processed %d rows, want exactly one", processed)
	}
	var comments int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&comments); err != nil {
		t.Fatalf("count concurrent comments: %v", err)
	}
	if comments != 1 {
		t.Fatalf("concurrent finalizer comments=%d, want one", comments)
	}
}

func TestExternalPRFinalizerDeleteFenceSuppressesStaleParentEffect(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "finalizer delete parent", "in_progress", "", nil)
	child := createExternalPRTestIssue(t, "finalizer delete child", "done", parent, int32Ptr(1))
	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id=$1 ORDER BY created_at ASC LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load finalizer delete agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type='agent', assignee_id=$1 WHERE id=$2`, agentID, parent); err != nil {
		t.Fatalf("assign finalizer delete parent: %v", err)
	}
	intentID := uuid.New()
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    next_attempt_at
) VALUES ($1,$2,$3,'finalizer-delete-fence','external_pr_terminal_reconcile','in_progress','done',$4,'{}',now())`, intentID, testWorkspaceID, child, uuid.New()); err != nil {
		t.Fatalf("seed finalizer delete intent: %v", err)
	}
	bindExternalPRFinalizationToCurrentGeneration(t, intentID.String(), child)
	t.Cleanup(func() {
		testHandler.IssueDeleteHook = nil
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id=$1 OR issue_id=$2`, parent, child)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id=$1 OR issue_id=$2`, parent, child)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1 OR issue_id=$2`, parent, child)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, child)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, parent)
	})
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(child))
	if err != nil {
		t.Fatalf("load finalizer delete child: %v", err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.IssueDeleteHook = func(stage string) {
		if stage == "completion_lock_acquired" {
			close(locked)
			<-release
		}
	}
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := testHandler.deleteIssueAndCollectAttachmentURLs(ctx, issue)
		deleteDone <- deleteErr
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not acquire provider/Issue fence")
	}
	finalizerDone := make(chan error, 1)
	go func() {
		finalizerDone <- testHandler.finalizePullRequestCompletionIntent(ctx, pgtype.UUID{Bytes: intentID, Valid: true})
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete child: %v", err)
	}
	if err := <-finalizerDone; err != nil {
		t.Fatalf("finalizer after delete: %v", err)
	}
	var comments int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&comments); err != nil {
		t.Fatalf("count stale parent comments: %v", err)
	}
	if comments != 0 {
		t.Fatalf("finalizer created %d stale parent comments after delete", comments)
	}
}

func TestExternalPRSourceSweepDeleteFenceLeavesNoOrphanWork(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issueID := createExternalPRTestIssue(t, "source sweep delete fence", "todo", "", nil)
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("load source sweep issue: %v", err)
	}
	request := externalPRCompletionReq(testWorkspaceID, issueID, 9922)
	request.State = "closed"
	request.LinkConfidence = "authoritative"
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, request); err != nil {
		t.Fatalf("seed source sweep link: %v", err)
	}
	t.Cleanup(func() {
		testHandler.IssueDeleteHook = nil
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})
	locked := make(chan struct{})
	release := make(chan struct{})
	testHandler.IssueDeleteHook = func(stage string) {
		if stage == "completion_lock_acquired" {
			close(locked)
			<-release
		}
	}
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := testHandler.deleteIssueAndCollectAttachmentURLs(ctx, issue)
		deleteDone <- deleteErr
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("issue delete did not acquire canonical completion fence")
	}
	sweepDone := make(chan error, 1)
	go func() {
		_, sweepErr := testHandler.sweepExternalPRTerminalWork(ctx, testPool)
		sweepDone <- sweepErr
	}()
	// The sweep may have read the link before it blocks on the provider fence;
	// releasing delete must still force its second scope read to observe no link.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	if err := <-sweepDone; err != nil {
		t.Fatalf("source sweep after delete: %v", err)
	}
	var workCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM external_pr_reconcile_work WHERE issue_id=$1`, issueID).Scan(&workCount); err != nil {
		t.Fatalf("count orphan source work: %v", err)
	}
	if workCount != 0 {
		t.Fatalf("source sweep left %d orphan work rows after delete", workCount)
	}
}

func TestExternalPRFinalizationBarrierGenerationDedupAndNewChildGeneration(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := createExternalPRTestIssue(t, "barrier generation parent", "in_progress", "", nil)
	child1 := createExternalPRTestIssue(t, "barrier generation child 1", "done", parent, int32Ptr(1))
	child2 := createExternalPRTestIssue(t, "barrier generation child 2", "done", parent, int32Ptr(1))
	cleanupExternalPRReconcileIssueFixtures(t, parent, child1)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id=$1`, parent)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id=$1`, parent)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_finalization WHERE issue_id IN ($1,$2)`, child2, parent)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, child2)
	})
	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id=$1 ORDER BY created_at ASC LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load barrier agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type='agent', assignee_id=$1 WHERE id=$2`, agentID, parent); err != nil {
		t.Fatalf("assign barrier parent: %v", err)
	}
	seedIntent := func(issueID, revision string) uuid.UUID {
		intentID := uuid.New()
		if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    next_attempt_at, max_attempts
) VALUES ($1,$2,$3,$4,'external_pr_terminal_reconcile','in_progress','done',$5,'{}',now(),4)`,
			intentID, testWorkspaceID, issueID, revision, uuid.New()); err != nil {
			t.Fatalf("seed barrier intent %s: %v", revision, err)
		}
		bindExternalPRFinalizationToCurrentGeneration(t, intentID.String(), issueID)
		return intentID
	}
	seedIntent(child1, "barrier-child-1")
	seedIntent(child2, "barrier-child-2")
	var wg sync.WaitGroup
	results := make(chan int64, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"barrier-a", "barrier-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			processed, err := testHandler.reconcileDueExternalPRFinalizations(ctx, testHandler.Queries, owner)
			results <- processed
			errs <- err
		}(owner)
	}
	wg.Wait()
	close(results)
	close(errs)
	var processed int64
	for count := range results {
		processed += count
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("barrier concurrent finalization: %v", err)
		}
	}
	if processed != 2 {
		t.Fatalf("barrier intents processed=%d, want 2", processed)
	}
	var comments, tasks int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&comments); err != nil {
		t.Fatalf("count first barrier comments: %v", err)
	}
	if comments != 1 {
		t.Fatalf("first barrier comments=%d, want 1", comments)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM agent_task_queue WHERE issue_id=$1`, parent).Scan(&tasks); err != nil {
		t.Fatalf("count first barrier tasks: %v", err)
	}
	if tasks != 1 {
		t.Fatalf("first barrier tasks=%d, want 1", tasks)
	}
	// Simulate the first generation being consumed so a new generation may
	// enqueue its own task while retaining the first durable comment.
	if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id=$1`, parent); err != nil {
		t.Fatalf("clear first generation task: %v", err)
	}
	child3 := createExternalPRTestIssue(t, "barrier generation child 3", "done", parent, int32Ptr(1))
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, child3) })
	seedIntent(child3, "barrier-child-3-new-generation")
	if processed, err := testHandler.reconcileDueExternalPRFinalizations(ctx, testHandler.Queries, "barrier-new-generation"); err != nil || processed != 1 {
		t.Fatalf("new barrier generation=(%d,%v), want (1,nil)", processed, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM comment WHERE issue_id=$1 AND finalization_key LIKE 'external-pr-barrier:v1:%'`, parent).Scan(&comments); err != nil {
		t.Fatalf("count new barrier comments: %v", err)
	}
	if comments != 2 {
		t.Fatalf("new child barrier comments=%d, want 2 generations", comments)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM agent_task_queue WHERE issue_id=$1`, parent).Scan(&tasks); err != nil {
		t.Fatalf("count new barrier tasks: %v", err)
	}
	if tasks != 1 {
		t.Fatalf("new barrier tasks=%d, want one task for new generation", tasks)
	}
}

func TestExternalPRTerminalWorkUsesPersistedEffectiveRevision(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "effective revision fixture", "todo", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	first := externalPRCompletionReq(testWorkspaceID, issue, 992)
	first.IdempotencyKey = "revision-key-992"
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, first); err != nil {
		t.Fatalf("seed keyed terminal fact: %v", err)
	}
	second := first
	second.IdempotencyKey = ""
	second.State = "merged"
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, second); err != nil {
		t.Fatalf("seed keyless terminal replay: %v", err)
	}
	var linkID, effective string
	if err := testPool.QueryRow(ctx, `
SELECT id, `+externalPRLinkEffectiveRevisionExpr+`
FROM external_pull_request_link WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issue).Scan(&linkID, &effective); err != nil {
		t.Fatalf("read persisted effective revision: %v", err)
	}
	if effective == "revision-key-992" || effective == "" {
		t.Fatalf("effective revision=%q, want keyless durable link revision", effective)
	}
	var revision string
	var sourceKey *string
	if err := testPool.QueryRow(ctx, `
SELECT source_revision, source_idempotency_key
FROM external_pr_reconcile_work
WHERE link_id=$1`, linkID).Scan(&revision, &sourceKey); err != nil {
		t.Fatalf("read persisted revision work: %v", err)
	}
	if revision != effective {
		t.Fatalf("work revision=%q effective=%q, want persisted terminal facts to own revision", revision, effective)
	}
	if sourceKey == nil || *sourceKey != "revision-key-992" {
		t.Fatalf("source idempotency key=%v, want first request correlation preserved", sourceKey)
	}
	var workCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM external_pr_reconcile_work WHERE link_id=$1`, linkID).Scan(&workCount); err != nil {
		t.Fatalf("count persisted revision work: %v", err)
	}
	if workCount != 1 {
		t.Fatalf("same persisted fact created %d work rows, want one", workCount)
	}
}

func TestExternalPRTerminalRevisionBindsCompletionFactsAndPreservesReplayIdentity(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "terminal revision facts", "todo", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	closed := externalPRCompletionReq(testWorkspaceID, issue, 993)
	closed.IdempotencyKey = ""
	closed.State = "closed"
	closed.LinkConfidence = "inferred"
	intent := false
	closed.CompletionIntent = &intent
	closed.MergedSHA = ""
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, closed); err != nil {
		t.Fatalf("seed keyless closed fact: %v", err)
	}
	var linkID, closedRevision string
	if err := testPool.QueryRow(ctx, `
SELECT id, `+externalPRLinkEffectiveRevisionExpr+`
FROM external_pull_request_link WHERE workspace_id=$1 AND issue_id=$2`, testWorkspaceID, issue).Scan(&linkID, &closedRevision); err != nil {
		t.Fatalf("read closed source revision: %v", err)
	}
	merged := closed
	merged.State = "merged"
	merged.LinkConfidence = "authoritative"
	intent = true
	merged.CompletionIntent = &intent
	merged.MergedSHA = "sha-993-merged"
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, merged); err != nil {
		t.Fatalf("promote keyless fact to merged: %v", err)
	}
	var mergedRevision string
	if err := testPool.QueryRow(ctx, `
SELECT `+externalPRLinkEffectiveRevisionExpr+`
FROM external_pull_request_link WHERE id=$1`, linkID).Scan(&mergedRevision); err != nil {
		t.Fatalf("read merged source revision: %v", err)
	}
	if closedRevision == "" || mergedRevision == "" || closedRevision == mergedRevision {
		t.Fatalf("source revisions closed=%q merged=%q, want distinct non-empty identities", closedRevision, mergedRevision)
	}
	var workCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM external_pr_reconcile_work WHERE link_id=$1`, linkID).Scan(&workCount); err != nil {
		t.Fatalf("count terminal revision work: %v", err)
	}
	if workCount != 2 {
		t.Fatalf("terminal revision work count=%d, want closed and merged rows", workCount)
	}
	if _, err := testHandler.upsertExternalPullRequestLink(ctx, merged); err != nil {
		t.Fatalf("replay same merged fact: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM external_pr_reconcile_work WHERE link_id=$1`, linkID).Scan(&workCount); err != nil {
		t.Fatalf("count replayed terminal revision work: %v", err)
	}
	if workCount != 2 {
		t.Fatalf("same fact replay created %d work rows, want 2", workCount)
	}
	if _, err := testHandler.Queries.SweepExternalPRTerminalWork(ctx); err != nil {
		t.Fatalf("source sweep same facts: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM external_pr_reconcile_work WHERE link_id=$1`, linkID).Scan(&workCount); err != nil {
		t.Fatalf("count source-swept terminal work: %v", err)
	}
	if workCount != 2 {
		t.Fatalf("same fact source sweep created %d work rows, want 2", workCount)
	}
	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_work SET state='retry_wait', next_attempt_at=now()+interval '1 hour' WHERE link_id=$1 AND source_revision=$2`, linkID, mergedRevision); err != nil {
		t.Fatalf("seed retry_wait source work: %v", err)
	}
	if _, err := testHandler.Queries.SweepExternalPRTerminalWork(ctx); err != nil {
		t.Fatalf("source sweep retry nudge: %v", err)
	}
	var nextAttempt time.Time
	if err := testPool.QueryRow(ctx, `SELECT next_attempt_at FROM external_pr_reconcile_work WHERE link_id=$1 AND source_revision=$2`, linkID, mergedRevision).Scan(&nextAttempt); err != nil {
		t.Fatalf("read nudged source work: %v", err)
	}
	var dbNow time.Time
	if err := testPool.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatalf("read source sweep database clock: %v", err)
	}
	if nextAttempt.After(dbNow) {
		t.Fatalf("source sweep left retry_wait next_attempt_at=%s after db now=%s", nextAttempt, dbNow)
	}
	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_work SET state='dead' WHERE link_id=$1 AND source_revision=$2`, linkID, mergedRevision); err != nil {
		t.Fatalf("seed dead source work: %v", err)
	}
	if _, err := testHandler.Queries.SweepExternalPRTerminalWork(ctx); err != nil {
		t.Fatalf("source sweep dead terminal row: %v", err)
	}
	var finalState string
	if err := testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_work WHERE link_id=$1 AND source_revision=$2`, linkID, mergedRevision).Scan(&finalState); err != nil {
		t.Fatalf("read dead source work: %v", err)
	}
	if finalState != "dead" {
		t.Fatalf("source sweep resurrected dead row to %q", finalState)
	}
}

func TestExternalPRReconcileLeaseExpiryStaleStealAndCASLost(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "reconcile lease CAS", "todo", "", nil)
	workID := uuid.New()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	workDBID := pgtype.UUID{Bytes: workID, Valid: true}
	linkID := uuid.New()
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_work (
    id, workspace_id, issue_id, link_id, kind, provider, external_repo,
    external_number, source_revision, max_attempts, next_attempt_at
) VALUES ($1,$2,$3,$4,'external_pr_terminal','ags','ags/agent-kit',991,
          'lease-cas-fixture',4,now())`, workID, testWorkspaceID, issue, linkID); err != nil {
		t.Fatalf("seed reconcile work: %v", err)
	}
	first, err := testHandler.Queries.ClaimExternalPRReconcileWorkByID(ctx, db.ClaimExternalPRReconcileWorkByIDParams{
		ID: workDBID, LeaseOwner: pgtype.Text{String: "worker-a", Valid: true}, Secs: 90,
	})
	if err != nil {
		t.Fatalf("first reconcile claim: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_work SET state='succeeded', completed_at=now() WHERE workspace_id=$1 AND id<>$2`, testWorkspaceID, workID); err != nil {
		t.Fatalf("isolate reconcile lease fixture: %v", err)
	}
	if _, err := testHandler.Queries.ClaimExternalPRReconcileWorkByID(ctx, db.ClaimExternalPRReconcileWorkByIDParams{
		ID: workDBID, LeaseOwner: pgtype.Text{String: "worker-b", Valid: true}, Secs: 90,
	}); err == nil {
		t.Fatal("second worker claimed an active reconcile lease")
	}
	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_work SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, workID); err != nil {
		t.Fatalf("expire reconcile lease fixture: %v", err)
	}
	second, err := testHandler.Queries.ClaimExternalPRReconcileWorkByID(ctx, db.ClaimExternalPRReconcileWorkByIDParams{
		ID: workDBID, LeaseOwner: pgtype.Text{String: "worker-b", Valid: true}, Secs: 90,
	})
	if err != nil {
		t.Fatalf("stale reconcile steal: %v", err)
	}
	if second.LeaseToken == first.LeaseToken {
		t.Fatal("stale steal reused the old lease token")
	}
	rows, err := testHandler.Queries.CompleteExternalPRReconcileWork(ctx, db.CompleteExternalPRReconcileWorkParams{
		ID: workDBID, LeaseToken: first.LeaseToken, State: "succeeded",
	})
	if err != nil {
		t.Fatalf("lost CAS completion: %v", err)
	}
	if rows != 0 {
		t.Fatalf("lost CAS completion rows=%d, want 0", rows)
	}
	rows, err = testHandler.Queries.FailExternalPRReconcileWork(ctx, db.FailExternalPRReconcileWorkParams{
		ID: workDBID, LeaseToken: first.LeaseToken, DelaySeconds: 60,
		LastErrorCode: pgtype.Text{String: "stale", Valid: true}, LastRedactedError: pgtype.Text{String: "stale", Valid: true},
	})
	if err != nil {
		t.Fatalf("lost CAS failure: %v", err)
	}
	if rows != 0 {
		t.Fatalf("lost CAS failure rows=%d, want 0", rows)
	}
}

func TestExternalPRFinalizationLeaseExpiryStaleStealAndCASLost(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "finalization lease CAS", "done", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	intentID := uuid.New()
	intentDBID := pgtype.UUID{Bytes: intentID, Valid: true}
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    next_attempt_at, max_attempts
) VALUES ($1,$2,$3,'finalization-lease-cas','external_pr_terminal_reconcile',
          'todo','done',$4,'{}',now(),3)`, intentID, testWorkspaceID, issue, uuid.New()); err != nil {
		t.Fatalf("seed finalization lease fixture: %v", err)
	}
	first, err := testHandler.Queries.ClaimExternalPRFinalizationByID(ctx, db.ClaimExternalPRFinalizationByIDParams{
		ID: intentDBID, LeaseOwner: pgtype.Text{String: "finalizer-a", Valid: true}, Secs: 90,
	})
	if err != nil {
		t.Fatalf("first finalization claim: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_finalization SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, intentID); err != nil {
		t.Fatalf("expire finalization lease fixture: %v", err)
	}
	second, err := testHandler.Queries.ClaimExternalPRFinalizationByID(ctx, db.ClaimExternalPRFinalizationByIDParams{
		ID: intentDBID, LeaseOwner: pgtype.Text{String: "finalizer-b", Valid: true}, Secs: 90,
	})
	if err != nil {
		t.Fatalf("stale finalization steal: %v", err)
	}
	if second.LeaseToken == first.LeaseToken {
		t.Fatal("stale finalization steal reused the old lease token")
	}
	rows, err := testHandler.Queries.UpdateExternalPRFinalization(ctx, db.UpdateExternalPRFinalizationParams{
		ID: first.ID, State: "recorded", Attempt: first.Attempt,
		LastErrorCode:     pgtype.Text{String: "stale", Valid: true},
		LastRedactedError: pgtype.Text{String: "stale", Valid: true},
		LeaseToken:        first.LeaseToken,
	})
	if err != nil {
		t.Fatalf("stale finalization update: %v", err)
	}
	if rows != 0 {
		t.Fatalf("stale finalization update rows=%d, want 0", rows)
	}
	if err := testHandler.recordFinalizationError(ctx, intentDBID, first.LeaseToken, "stale"); !errors.Is(err, errExternalPRFinalizationLeaseLost) {
		t.Fatalf("stale finalization error=%v, want lease lost", err)
	}
}

func TestExternalPRFinalizationExpiryConvergesCrashAtAttemptLimit(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "finalization expiry crash", "done", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	intentID := uuid.New()
	intentDBID := pgtype.UUID{Bytes: intentID, Valid: true}
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    state, attempt, max_attempts, next_attempt_at
) VALUES ($1,$2,$3,'finalization-expiry-crash','external_pr_terminal_reconcile',
          'todo','done',$4,'{}','pending',0,1,now())`, intentID, testWorkspaceID, issue, uuid.New()); err != nil {
		t.Fatalf("seed finalization expiry fixture: %v", err)
	}
	claimed, err := testHandler.Queries.ClaimExternalPRFinalizationByID(ctx, db.ClaimExternalPRFinalizationByIDParams{
		ID: intentDBID, LeaseOwner: pgtype.Text{String: "expiry-crash-worker", Valid: true}, Secs: 90,
	})
	if err != nil {
		t.Fatalf("claim finalization expiry fixture: %v", err)
	}
	if claimed.Attempt != 1 {
		t.Fatalf("attempt=%d, want 1", claimed.Attempt)
	}
	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_finalization SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, intentID); err != nil {
		t.Fatalf("expire finalization crash lease: %v", err)
	}
	job := ExternalPRReconcileJob(testPool, testHandler)
	result, err := job.Handler(ctx, scheduler.HandlerInput{RunnerID: "expiry-sweep-worker"})
	if !errors.Is(err, errExternalPRFinalizationDead) {
		t.Fatalf("finalization scheduler expiry error=%v, want typed dead alert", err)
	}
	if result.Result["finalizations_dead"] != int64(1) || result.Result["finalizations_expired"] != int64(1) {
		t.Fatalf("finalization scheduler audit=%v, want dead=1 expired=1", result.Result)
	}
	rows, err := testHandler.Queries.ExpireExternalPRFinalization(ctx)
	if err != nil {
		t.Fatalf("repeat expire finalization crash row: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("repeat expired finalization rows=%d, want 0", len(rows))
	}
	var state, code, summary string
	var owner, token *string
	var completedAt *time.Time
	if err := testPool.QueryRow(ctx, `
SELECT state, last_error_code, last_redacted_error, lease_owner, lease_token::text, completed_at
FROM external_pr_reconcile_finalization WHERE id=$1`, intentID).Scan(&state, &code, &summary, &owner, &token, &completedAt); err != nil {
		t.Fatalf("read expired finalization: %v", err)
	}
	if state != "dead" || code != "lease_expired_max_attempts" || summary != "finalization lease expired after retry budget was exhausted" || owner != nil || token != nil || completedAt == nil {
		t.Fatalf("expired finalization=(%q,%q,%q,%v,%v,%v), want fixed dead terminal facts", state, code, summary, owner, token, completedAt)
	}
	if strings.Contains(summary, "secret") || strings.Contains(summary, "password") {
		t.Fatalf("expiry summary is not secret-safe: %q", summary)
	}
}

func TestExternalPRFinalizationExpiryMarksLinkedWorkDeadAndFailsSchedulerTick(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "expired linked finalization", "done", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	linkID := uuid.New()
	workID := uuid.New()
	intentID := uuid.New()
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pull_request_link (
    id, workspace_id, issue_id, provider, external_repo, external_number,
    state, link_confidence, completion_intent
) VALUES ($1,$2,$3,'ags','expired-finalization-repo',9912,'merged','authoritative',true)`, linkID, testWorkspaceID, issue); err != nil {
		t.Fatalf("seed expired finalization link: %v", err)
	}
	var revision string
	if err := testPool.QueryRow(ctx, `SELECT `+externalPRLinkEffectiveRevisionExpr+` FROM external_pull_request_link WHERE id=$1`, linkID).Scan(&revision); err != nil {
		t.Fatalf("read expired finalization revision: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_work (
    id, workspace_id, issue_id, link_id, kind, provider, external_repo,
    external_number, source_revision, state, attempt, max_attempts,
    next_attempt_at, lease_owner, lease_token, lease_expires_at
) VALUES ($1,$2,$3,$4,'external_pr_terminal','ags','expired-finalization-repo',9912,$5,'claimed',1,4,now(),'expired-worker',$6,now()-interval '1 second')`, workID, testWorkspaceID, issue, linkID, revision, uuid.New()); err != nil {
		t.Fatalf("seed expired finalization work: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, work_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    state, attempt, max_attempts, next_attempt_at, lease_owner, lease_token, lease_expires_at
) VALUES ($1,$2,$3,$4,$5,'external_pr_terminal_reconcile','todo','done',$6,'{}',
          'retry_wait',1,1,now(),'finalizer-expired',$7,now()-interval '1 second')`, intentID, testWorkspaceID, issue, workID, revision, uuid.New(), uuid.New()); err != nil {
		t.Fatalf("seed expired linked finalization intent: %v", err)
	}
	job := ExternalPRReconcileJob(testPool, testHandler)
	result, tickErr := job.Handler(ctx, scheduler.HandlerInput{RunnerID: "expired-finalization-scheduler"})
	if !errors.Is(tickErr, errExternalPRFinalizationDead) {
		t.Fatalf("scheduler tick error=%v, want typed dead finalization alert (result=%v)", tickErr, result.Result)
	}
	if result.Result["finalizations_expired"] != int64(1) || result.Result["finalizations_dead"] != int64(1) {
		t.Fatalf("scheduler finalization audit=%v, want expired=1 dead=1", result.Result)
	}
	var finalizationState, workState string
	if err := testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_finalization WHERE id=$1`, intentID).Scan(&finalizationState); err != nil {
		t.Fatalf("read expired linked finalization state: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_work WHERE id=$1`, workID).Scan(&workState); err != nil {
		t.Fatalf("read expired linked work state: %v", err)
	}
	if finalizationState != "dead" || workState != "dead" {
		t.Fatalf("expired linked states=(%q,%q), want dead/dead", finalizationState, workState)
	}
}

func TestExternalPRDeadFinalizationBlocksWorkAndFailsSchedulerTick(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "dead finalization scheduler", "done", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_work WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pull_request_link WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	linkID := uuid.New()
	workID := uuid.New()
	intentID := uuid.New()
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pull_request_link (
    id, workspace_id, issue_id, provider, external_repo, external_number,
    state, link_confidence, completion_intent
) VALUES ($1,$2,$3,'ags','dead-finalization-repo',9911,'merged','authoritative',true)`, linkID, testWorkspaceID, issue); err != nil {
		t.Fatalf("seed dead finalization link: %v", err)
	}
	var revision string
	if err := testPool.QueryRow(ctx, `SELECT `+externalPRLinkEffectiveRevisionExpr+` FROM external_pull_request_link WHERE id=$1`, linkID).Scan(&revision); err != nil {
		t.Fatalf("read dead finalization revision: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_work (
    id, workspace_id, issue_id, link_id, kind, provider, external_repo,
    external_number, source_revision, next_attempt_at
) VALUES ($1,$2,$3,$4,'external_pr_terminal','ags','dead-finalization-repo',9911,$5,'1970-01-01'::timestamptz)`, workID, testWorkspaceID, issue, linkID, revision); err != nil {
		t.Fatalf("seed dead finalization work: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, work_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    state, attempt, max_attempts, last_error_code, last_redacted_error, completed_at
) VALUES ($1,$2,$3,$4,$5,'external_pr_terminal_reconcile','todo','done',$6,'{}',
          'dead',4,4,'comment_finalize_error','finalization is permanently failed',now())`, intentID, testWorkspaceID, issue, workID, revision, uuid.New()); err != nil {
		t.Fatalf("seed dead finalization intent: %v", err)
	}
	deadErr := testHandler.finalizePullRequestCompletionIntent(ctx, pgtype.UUID{Bytes: intentID, Valid: true})
	if !errors.Is(deadErr, errExternalPRFinalizationDead) {
		t.Fatalf("dead finalization error=%v, want typed dead outcome", deadErr)
	}
	job := ExternalPRReconcileJob(testPool, testHandler)
	result, tickErr := job.Handler(ctx, scheduler.HandlerInput{RunnerID: "dead-finalization-scheduler"})
	if tickErr == nil || !strings.Contains(tickErr.Error(), "dead") {
		var debugWork, debugIntent string
		_ = testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_work WHERE id=$1`, workID).Scan(&debugWork)
		_ = testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_finalization WHERE id=$1`, intentID).Scan(&debugIntent)
		t.Fatalf("scheduler tick error=%v, want dead finalization error (work=%s intent=%s result=%v)", tickErr, debugWork, debugIntent, result.Result)
	}
	if result.Result["finalizations_dead"] == nil {
		t.Fatalf("scheduler result=%v, want finalizations_dead audit field", result.Result)
	}
	var state string
	if err := testPool.QueryRow(ctx, `SELECT state FROM external_pr_reconcile_work WHERE id=$1`, workID).Scan(&state); err != nil {
		t.Fatalf("read dead work state: %v", err)
	}
	if state != "dead" {
		t.Fatalf("dead finalization work state=%q, must not be succeeded", state)
	}
}

func TestExternalPRFinalizationRecordErrorUsesDBClockAndDeadBudget(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createExternalPRTestIssue(t, "finalization retry budget", "done", "", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM external_pr_reconcile_finalization WHERE issue_id=$1`, issue)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issue)
	})
	intentID := uuid.New()
	intentDBID := pgtype.UUID{Bytes: intentID, Valid: true}
	if _, err := testPool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    next_attempt_at, max_attempts
) VALUES ($1,$2,$3,'finalization-retry','external_pr_terminal_reconcile',
          'todo','done',$4,'{}',now(),2)`, intentID, testWorkspaceID, issue, uuid.New()); err != nil {
		t.Fatalf("seed retry budget intent: %v", err)
	}
	var dbBefore time.Time
	if err := testPool.QueryRow(ctx, `SELECT now()`).Scan(&dbBefore); err != nil {
		t.Fatalf("read database clock before retry: %v", err)
	}
	claimed, err := testHandler.Queries.ClaimExternalPRFinalizationByID(ctx, db.ClaimExternalPRFinalizationByIDParams{
		ID: intentDBID, LeaseOwner: pgtype.Text{String: "retry-budget-a", Valid: true}, Secs: 90,
	})
	if err != nil {
		t.Fatalf("claim retry budget intent: %v", err)
	}
	if err := testHandler.recordFinalizationError(ctx, intentDBID, claimed.LeaseToken, "injected"); err != nil {
		t.Fatalf("record retryable finalization error: %v", err)
	}
	var state string
	var nextAttempt time.Time
	if err := testPool.QueryRow(ctx, `SELECT state, next_attempt_at FROM external_pr_reconcile_finalization WHERE id=$1`, intentID).Scan(&state, &nextAttempt); err != nil {
		t.Fatalf("read retry budget state: %v", err)
	}
	if state != "retry_wait" {
		t.Fatalf("retry budget state=%q, want retry_wait", state)
	}
	if !nextAttempt.After(dbBefore) {
		t.Fatalf("retry finalization next_attempt_at=%s, want after database clock %s", nextAttempt, dbBefore)
	}

	if _, err := testPool.Exec(ctx, `UPDATE external_pr_reconcile_finalization SET next_attempt_at=now() WHERE id=$1`, intentID); err != nil {
		t.Fatalf("make second retry due: %v", err)
	}
	claimed, err = testHandler.Queries.ClaimExternalPRFinalizationByID(ctx, db.ClaimExternalPRFinalizationByIDParams{
		ID: intentDBID, LeaseOwner: pgtype.Text{String: "retry-budget-b", Valid: true}, Secs: 90,
	})
	if err != nil {
		t.Fatalf("claim second retry budget attempt: %v", err)
	}
	if claimed.Attempt != 2 {
		t.Fatalf("second finalization attempt=%d, want 2", claimed.Attempt)
	}
	if err := testHandler.recordFinalizationError(ctx, intentDBID, claimed.LeaseToken, "injected-again"); !errors.Is(err, errExternalPRFinalizationDead) {
		t.Fatalf("record terminal finalization error=%v, want typed dead", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT state, next_attempt_at FROM external_pr_reconcile_finalization WHERE id=$1`, intentID).Scan(&state, &nextAttempt); err != nil {
		t.Fatalf("read dead finalization state: %v", err)
	}
	if state != "dead" {
		t.Fatalf("retry budget state=%q, want dead", state)
	}
	if nextAttempt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("dead finalization moved next_attempt_at into the future: %s", nextAttempt)
	}
}

func TestExternalPRReconcileJobUsesTypedBoundedWork(t *testing.T) {
	job := ExternalPRReconcileJob(nil, &Handler{})
	if job.Name != "external_pr_reconcile" || job.AllowStaleReentry != true || job.MaxAttempts != 4 {
		t.Fatalf("unexpected reconcile job: %#v", job)
	}
	if job.StaleTimeout <= job.RunTimeout || job.HeartbeatInterval >= job.StaleTimeout {
		t.Fatalf("invalid lease timing: run=%s stale=%s heartbeat=%s", job.RunTimeout, job.StaleTimeout, job.HeartbeatInterval)
	}
	if got := job.RetryBackoff; len(got) != 3 || got[0] != time.Minute || got[1] != 5*time.Minute || got[2] != 15*time.Minute {
		t.Fatalf("retry backoff=%v", got)
	}
	if job.CatchUpMode != scheduler.CatchUpLatestOnly {
		t.Fatalf("reconcile job should use one bounded startup/source sweep")
	}
}
