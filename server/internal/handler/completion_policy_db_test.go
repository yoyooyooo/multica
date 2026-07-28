package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var completionPolicyTestSequence atomic.Int32

func TestCompletionPolicyPublicHandlerMatrix(t *testing.T) {
	cases := []struct {
		name         string
		policy       any
		setPolicy    bool
		wantStatus   string
		wantOutcome  string
		wantReason   string
		wantComments int
	}{
		{name: "absent", wantStatus: "done", wantOutcome: "completed", wantComments: 1},
		{name: "empty", setPolicy: true, policy: "", wantStatus: "done", wantOutcome: "completed", wantComments: 1},
		{name: "leaf child only", setPolicy: true, policy: "leaf_child_only", wantStatus: "done", wantOutcome: "completed", wantComments: 1},
		{name: "record only", setPolicy: true, policy: "record_only", wantStatus: "todo", wantOutcome: "recorded", wantReason: "record_only"},
		{name: "unknown", setPolicy: true, policy: "future", wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "bool", setPolicy: true, policy: true, wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "number", setPolicy: true, policy: 1, wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "null", setPolicy: true, policy: nil, wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "object", setPolicy: true, policy: map[string]any{"mode": "leaf_child_only"}, wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "array", setPolicy: true, policy: []any{"leaf_child_only"}, wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "case variant", setPolicy: true, policy: "Leaf_Child_Only", wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "leading whitespace", setPolicy: true, policy: " leaf_child_only", wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
		{name: "trailing whitespace", setPolicy: true, policy: "leaf_child_only ", wantStatus: "todo", wantOutcome: "recorded", wantReason: "completion_policy_unsupported"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent, child, nextStage := createCompletionPolicyIssuePairViaHandlers(t, tc.name)
			if tc.setPolicy {
				setCompletionPolicyForMatrix(t, child.ID, tc.policy)
			}
			baseline := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID)
			response := completeExternalPRViaHandler(t, child.ID, nextCompletionPolicyPRNumber())
			if response.Outcome != tc.wantOutcome || response.Reason != tc.wantReason {
				t.Fatalf("completion response=%#v want outcome=%q reason=%q", response, tc.wantOutcome, tc.wantReason)
			}
			assertIssueStatus(t, child.ID, tc.wantStatus)
			if comments := countParentSystemComments(t, parent.ID); comments != tc.wantComments {
				t.Fatalf("parent system comments=%d want=%d", comments, tc.wantComments)
			}
			after := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID)
			if tc.wantStatus == "done" {
				assertOrdinaryAssignedStageEffects(t, baseline, after, true)
			} else if after != baseline {
				t.Fatalf("record/unsupported completion emitted next-stage effects: before=%#v after=%#v", baseline, after)
			}
		})
	}
}

func TestCompletionPolicyAssignedStageOneAndTwoSuccessEffects(t *testing.T) {
	parent, stageOne, stageTwo := createCompletionPolicyIssuePairViaHandlers(t, "assigned-stages")
	beforeStageOne := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	if result := completeExternalPRViaHandler(t, stageOne.ID, nextCompletionPolicyPRNumber()); result.Outcome != "completed" {
		t.Fatalf("assigned stage 1 completion=%#v", result)
	}
	afterStageOne := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	assertOrdinaryAssignedStageEffects(t, beforeStageOne, afterStageOne, true)

	beforeStageTwo := afterStageOne
	if result := completeExternalPRViaHandler(t, stageTwo.ID, nextCompletionPolicyPRNumber()); result.Outcome != "completed" {
		t.Fatalf("assigned stage 2 completion=%#v", result)
	}
	afterStageTwo := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	beforeStageTwo.nextStageStatus = "done"
	assertOrdinaryAssignedStageEffects(t, beforeStageTwo, afterStageTwo, false)
}

func TestCompletionPolicyPublicPromotionRunAndStageTwoCompletion(t *testing.T) {
	parent, stageOne, stageTwo := createCompletionPolicyIssuePairViaHandlers(t, "public-stage-run")

	updateIssueStatusViaHandler(t, stageOne.ID, "in_progress")
	reassignCompletionIssueForRun(t, stageOne)
	stageOneTask := claimCompletionTaskForIssue(t, stageOne.ID)
	startCompletionTaskViaHandler(t, stageOneTask)
	stageOneRunning := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	var stageOneRunningCount int
	if err := testPool.QueryRow(context.Background(), `SELECT COUNT(*)::int FROM agent_task_queue WHERE id=$1 AND status='running'`, stageOneTask).Scan(&stageOneRunningCount); err != nil {
		t.Fatal(err)
	}
	if stageOneRunningCount != 1 || stageOneRunning.nextStageStatus != "backlog" {
		t.Fatalf("stage 1 public run count=%d next-stage status=%q", stageOneRunningCount, stageOneRunning.nextStageStatus)
	}
	completeCompletionTaskViaHandler(t, stageOneTask)

	beforeStageOne := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	if result := completeExternalPRViaHandler(t, stageOne.ID, nextCompletionPolicyPRNumber()); result.Outcome != "completed" {
		t.Fatalf("stage 1 provider completion=%#v", result)
	}
	afterStageOne := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	assertOrdinaryAssignedStageEffects(t, beforeStageOne, afterStageOne, true)

	parentTask := claimCompletionTaskForIssue(t, parent.ID)
	startCompletionTaskViaHandler(t, parentTask)
	completeCompletionTaskViaHandler(t, parentTask)

	updateIssueStatusViaHandler(t, stageTwo.ID, "todo")
	reassignCompletionIssueForRun(t, stageTwo)
	promoted := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	if promoted.nextStageStatus != "todo" || promoted.nextStageQueued != afterStageOne.nextStageQueued+1 {
		t.Fatalf("stage 2 promotion effects before=%#v after=%#v", afterStageOne, promoted)
	}
	stageTwoTask := claimCompletionTaskForIssue(t, stageTwo.ID)
	claimed := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	if claimed.nextDispatched != promoted.nextDispatched+1 || claimed.nextStageQueued != promoted.nextStageQueued-1 {
		t.Fatalf("stage 2 dispatch effects before=%#v after=%#v", promoted, claimed)
	}
	startCompletionTaskViaHandler(t, stageTwoTask)
	running := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	if running.nextRunning != claimed.nextRunning+1 || running.nextDispatched != claimed.nextDispatched-1 {
		t.Fatalf("stage 2 run effects before=%#v after=%#v", claimed, running)
	}
	completeCompletionTaskViaHandler(t, stageTwoTask)

	beforeStageTwo := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	if result := completeExternalPRViaHandler(t, stageTwo.ID, nextCompletionPolicyPRNumber()); result.Outcome != "completed" {
		t.Fatalf("stage 2 provider completion=%#v", result)
	}
	afterStageTwo := completionEffectSnapshotForIssues(t, parent.ID, stageTwo.ID)
	if afterStageTwo.nextStageStatus != "done" || afterStageTwo.parentComments != beforeStageTwo.parentComments+1 {
		t.Fatalf("stage 2 terminal effects before=%#v after=%#v", beforeStageTwo, afterStageTwo)
	}
	if afterStageTwo.parentInbox != beforeStageTwo.parentInbox || afterStageTwo.nextStageInbox != beforeStageTwo.nextStageInbox {
		t.Fatalf("public stage chain duplicated inbox effects before=%#v after=%#v", beforeStageTwo, afterStageTwo)
	}
}

func TestCompletionPolicyReplayHandlerRebuildAndExplicitCloseReleaseOnce(t *testing.T) {
	t.Run("ordinary replay and handler rebuild", func(t *testing.T) {
		parent, child, _ := createCompletionPolicyIssuePairViaHandlers(t, "ordinary-replay")
		number := nextCompletionPolicyPRNumber()
		first := completeExternalPRViaHandler(t, child.ID, number)
		if first.Outcome != "completed" {
			t.Fatalf("first completion=%#v", first)
		}
		rebuilt := *testHandler
		second := completeExternalPRWithHandler(t, &rebuilt, child.ID, number)
		if second.Outcome != "already_done" {
			t.Fatalf("replayed completion=%#v", second)
		}
		if comments := countParentSystemComments(t, parent.ID); comments != 1 {
			t.Fatalf("replay/rebuild parent release comments=%d want=1", comments)
		}
	})

	t.Run("record only explicit close", func(t *testing.T) {
		parent, child, _ := createCompletionPolicyIssuePairViaHandlers(t, "explicit-close")
		setCompletionPolicyViaHandler(t, child.ID, "record_only")
		number := nextCompletionPolicyPRNumber()
		first := completeExternalPRViaHandler(t, child.ID, number)
		second := completeExternalPRViaHandler(t, child.ID, number)
		if first.Reason != "record_only" || second.Reason != "record_only" {
			t.Fatalf("record-only replay responses first=%#v second=%#v", first, second)
		}
		if comments := countParentSystemComments(t, parent.ID); comments != 0 {
			t.Fatalf("provider replay released parent %d times", comments)
		}

		updateIssueStatusViaHandler(t, child.ID, "done")
		updateIssueStatusViaHandler(t, child.ID, "done")
		if comments := countParentSystemComments(t, parent.ID); comments != 1 {
			t.Fatalf("explicit close released parent %d times, want 1", comments)
		}
	})
}

func TestPullRequestCompletionPublicActivityFailurePreventsRelease(t *testing.T) {
	parent, child, nextStage := createCompletionPolicyIssuePairViaHandlers(t, "activity-failure")
	baseline := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID)
	failing := *testHandler
	failing.CompletionActivityWriter = func(context.Context, *db.Queries, db.CreateActivityParams) (db.ActivityLog, error) {
		return db.ActivityLog{}, errors.New("injected durable activity failure")
	}
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "completion-policy-test-token")
	request := externalPRCompletionReq(testWorkspaceID, child.ID, nextCompletionPolicyPRNumber())
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", request)
	req.Header.Set("Authorization", "Bearer completion-policy-test-token")
	w := httptest.NewRecorder()
	failing.CompleteIssueFromExternalPR(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("activity failure status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "injected durable activity failure") {
		t.Fatalf("activity failure leaked internal detail: %s", w.Body.String())
	}
	assertIssueStatus(t, child.ID, "todo")
	if after := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID); after != baseline {
		t.Fatalf("activity failure released parent: before=%#v after=%#v", baseline, after)
	}
}

func TestPullRequestCompletionExplicitTerminalActivityFailureRollsBackSingleAndBatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		batch bool
	}{
		{name: "single"}, {name: "batch", batch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, child, nextStage := createCompletionPolicyIssuePairViaHandlers(t, "explicit-activity-failure-"+tc.name)
			baseline := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID)
			failing := *testHandler
			failing.CompletionActivityWriter = func(context.Context, *db.Queries, db.CreateActivityParams) (db.ActivityLog, error) {
				return db.ActivityLog{}, errors.New("injected explicit activity failure")
			}
			var req *http.Request
			if tc.batch {
				req = newRequest(http.MethodPut, "/api/issues/batch", map[string]any{
					"issue_ids": []string{child.ID}, "updates": map[string]any{"status": "done"},
				})
				w := httptest.NewRecorder()
				failing.BatchUpdateIssues(w, req)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"updated":0`) {
					t.Fatalf("BatchUpdateIssues status=%d body=%s", w.Code, w.Body.String())
				}
			} else {
				req = withURLParam(newRequest(http.MethodPut, "/api/issues/"+child.ID, map[string]any{"status": "done"}), "id", child.ID)
				w := httptest.NewRecorder()
				failing.UpdateIssue(w, req)
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("UpdateIssue status=%d body=%s", w.Code, w.Body.String())
				}
			}
			assertIssueStatus(t, child.ID, "todo")
			if after := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID); after != baseline {
				t.Fatalf("explicit activity failure released parent: before=%#v after=%#v", baseline, after)
			}
		})
	}
}

func TestPullRequestCompletionSerializesWithPublicTopologyWrites(t *testing.T) {
	create := func(t *testing.T, title string, parentID *string) IssueResponse {
		t.Helper()
		body := map[string]any{"title": title, "status": "todo", "priority": "medium"}
		if parentID != nil {
			body["parent_issue_id"] = *parentID
		}
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
		testHandler.CreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateIssue status=%d body=%s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}

	for _, tc := range []struct {
		name     string
		reparent bool
	}{
		{name: "child-create"}, {name: "reparent", reparent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			anchor := create(t, "topology anchor "+tc.name, nil)
			target := create(t, "topology target "+tc.name, &anchor.ID)
			var detached IssueResponse
			if tc.reparent {
				detached = create(t, "detached child "+tc.name, nil)
			}
			locked := make(chan struct{})
			release := make(chan struct{})
			completion := *testHandler
			completion.PullRequestFactHook = func(scope, point string) {
				if scope == "completion" && point == "current_loaded_before_terminal_update" {
					close(locked)
					<-release
				}
			}
			t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "completion-policy-test-token")
			type completionCall struct {
				code int
				body externalCompleteFromPRResponse
			}
			resultCh := make(chan completionCall, 1)
			prNumber := nextCompletionPolicyPRNumber()
			go func() {
				request := externalPRCompletionReq(testWorkspaceID, target.ID, prNumber)
				req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", request)
				req.Header.Set("Authorization", "Bearer completion-policy-test-token")
				w := httptest.NewRecorder()
				completion.CompleteIssueFromExternalPR(w, req)
				var body externalCompleteFromPRResponse
				_ = json.NewDecoder(w.Body).Decode(&body)
				resultCh <- completionCall{code: w.Code, body: body}
			}()
			<-locked

			codeCh := make(chan int, 1)
			go func() {
				w := httptest.NewRecorder()
				if tc.reparent {
					req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+detached.ID, map[string]any{"parent_issue_id": target.ID}), "id", detached.ID)
					testHandler.UpdateIssue(w, req)
				} else {
					req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
						"title": "late child", "status": "todo", "priority": "medium", "parent_issue_id": target.ID,
					})
					testHandler.CreateIssue(w, req)
				}
				codeCh <- w.Code
			}()
			time.Sleep(50 * time.Millisecond)
			close(release)
			if result := <-resultCh; result.code != http.StatusOK || result.body.Outcome != "completed" {
				t.Fatalf("completion status=%d body=%#v", result.code, result.body)
			}
			if code := <-codeCh; code != http.StatusConflict {
				t.Fatalf("topology write status=%d want 409", code)
			}
			assertIssueStatus(t, target.ID, "done")
			var childCount int
			if err := testPool.QueryRow(context.Background(), `SELECT COUNT(*)::int FROM issue WHERE parent_issue_id=$1`, target.ID).Scan(&childCount); err != nil {
				t.Fatal(err)
			}
			if childCount != 0 {
				t.Fatalf("terminal parent has %d late children", childCount)
			}
		})
	}
}

func TestPullRequestCompletionTopologyFirstKeepsParentNonTerminal(t *testing.T) {
	create := func(t *testing.T, title string, parentID *string) IssueResponse {
		t.Helper()
		body := map[string]any{"title": title, "status": "todo", "priority": "medium"}
		if parentID != nil {
			body["parent_issue_id"] = *parentID
		}
		w := httptest.NewRecorder()
		testHandler.CreateIssue(w, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body))
		if w.Code != http.StatusCreated {
			t.Fatalf("create issue status=%d body=%s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	for _, reparent := range []bool{false, true} {
		name := "child-create"
		if reparent {
			name = "reparent"
		}
		t.Run(name, func(t *testing.T) {
			anchor := create(t, "topology-first anchor "+name, nil)
			target := create(t, "topology-first target "+name, &anchor.ID)
			var detached IssueResponse
			if reparent {
				detached = create(t, "topology-first detached "+name, nil)
			}

			locked := make(chan struct{})
			release := make(chan struct{})
			topology := *testHandler
			if reparent {
				topology.TopologyFactHook = func(stage string) {
					if stage == "locked_before_write" {
						close(locked)
						<-release
					}
				}
			} else {
				issueService := *testHandler.IssueService
				issueService.TopologyFactHook = func(stage string) {
					if stage == "locked_before_write" {
						close(locked)
						<-release
					}
				}
				topology.IssueService = &issueService
			}
			topologyStatus := make(chan int, 1)
			go func() {
				w := httptest.NewRecorder()
				if reparent {
					topology.UpdateIssue(w, withURLParam(newRequest(http.MethodPut, "/api/issues/"+detached.ID, map[string]any{"parent_issue_id": target.ID}), "id", detached.ID))
				} else {
					topology.CreateIssue(w, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
						"title": "topology-first child " + name, "status": "todo", "priority": "medium", "parent_issue_id": target.ID,
					}))
				}
				topologyStatus <- w.Code
			}()
			<-locked

			completionResult := make(chan externalCompleteFromPRResponse, 1)
			go func() {
				completionResult <- completeExternalPRViaHandler(t, target.ID, nextCompletionPolicyPRNumber())
			}()
			select {
			case result := <-completionResult:
				t.Fatalf("provider crossed uncommitted topology transaction: %#v", result)
			case <-time.After(75 * time.Millisecond):
			}
			close(release)
			wantTopologyStatus := http.StatusCreated
			if reparent {
				wantTopologyStatus = http.StatusOK
			}
			if code := <-topologyStatus; code != wantTopologyStatus {
				t.Fatalf("topology status=%d want=%d", code, wantTopologyStatus)
			}
			result := <-completionResult
			if result.Outcome == "completed" {
				t.Fatalf("provider completed non-leaf target after topology commit: %#v", result)
			}
			assertIssueStatus(t, target.ID, "todo")
		})
	}
}

func TestConcurrentTopologyWritersCannotCreateCrossChainCycle(t *testing.T) {
	create := func(title string, parentID *string) IssueResponse {
		body := map[string]any{"title": title, "status": "todo", "priority": "medium"}
		if parentID != nil {
			body["parent_issue_id"] = *parentID
		}
		w := httptest.NewRecorder()
		testHandler.CreateIssue(w, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body))
		if w.Code != http.StatusCreated {
			t.Fatalf("create issue status=%d body=%s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}

	a := create("cross-cycle-a", nil)
	b := create("cross-cycle-b", nil)
	c := create("cross-cycle-c", &b.ID)
	d := create("cross-cycle-d", &a.ID)

	locked := make(chan struct{})
	release := make(chan struct{})
	first := *testHandler
	first.TopologyFactHook = func(stage string) {
		if stage == "locked_before_write" {
			close(locked)
			<-release
		}
	}
	statuses := make(chan int, 2)
	go func() {
		w := httptest.NewRecorder()
		first.UpdateIssue(w, withURLParam(newRequest(http.MethodPut, "/api/issues/"+a.ID, map[string]any{"parent_issue_id": c.ID}), "id", a.ID))
		statuses <- w.Code
	}()
	<-locked
	go func() {
		w := httptest.NewRecorder()
		testHandler.UpdateIssue(w, withURLParam(newRequest(http.MethodPut, "/api/issues/"+b.ID, map[string]any{"parent_issue_id": d.ID}), "id", b.ID))
		statuses <- w.Code
	}()
	select {
	case status := <-statuses:
		t.Fatalf("second topology writer crossed workspace lock: status=%d", status)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	firstStatus, secondStatus := <-statuses, <-statuses
	if !((firstStatus == http.StatusOK && secondStatus == http.StatusBadRequest) ||
		(firstStatus == http.StatusBadRequest && secondStatus == http.StatusOK)) {
		t.Fatalf("topology statuses=(%d,%d), want one 200 and one 400", firstStatus, secondStatus)
	}

	for _, start := range []string{a.ID, b.ID, c.ID, d.ID} {
		seen := map[string]bool{}
		cursor := start
		for cursor != "" {
			if seen[cursor] {
				t.Fatalf("cycle remains from %s at %s", start, cursor)
			}
			seen[cursor] = true
			issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(cursor))
			if err != nil {
				t.Fatal(err)
			}
			cursor = uuidToString(issue.ParentIssueID)
		}
	}
}

func TestWorkspaceTopologyLockDoesNotBlockAnotherWorkspace(t *testing.T) {
	ctx := context.Background()
	txA, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback(ctx)
	if err := testHandler.Queries.WithTx(txA).LockWorkspaceIssueTopology(ctx, parseUUID(testWorkspaceID)); err != nil {
		t.Fatal(err)
	}

	txB, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer txB.Rollback(ctx)
	done := make(chan error, 1)
	go func() {
		done <- testHandler.Queries.WithTx(txB).LockWorkspaceIssueTopology(ctx, parseUUID("11111111-1111-4111-8111-111111111111"))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("topology lock in another workspace blocked")
	}
}

func TestUpdateIssueRejectsCycleBeyondTraversalLimit(t *testing.T) {
	create := func(title string, parentID *string) IssueResponse {
		body := map[string]any{"title": title, "status": "todo", "priority": "medium"}
		if parentID != nil {
			body["parent_issue_id"] = *parentID
		}
		w := httptest.NewRecorder()
		testHandler.CreateIssue(w, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body))
		if w.Code != http.StatusCreated {
			t.Fatalf("create deep issue status=%d body=%s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	root := create("deep-cycle-root", nil)
	cursor := root
	for depth := 0; depth < 102; depth++ {
		cursor = create(fmt.Sprintf("deep-cycle-%03d", depth), &cursor.ID)
	}
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, withURLParam(newRequest(http.MethodPut, "/api/issues/"+root.ID, map[string]any{"parent_issue_id": cursor.ID}), "id", root.ID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("deep cycle status=%d want 400 body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "parent topology exceeds") && !strings.Contains(w.Body.String(), "circular parent") {
		t.Fatalf("deep cycle rejection body=%s", w.Body.String())
	}
}

func TestUpdateIssueSerializedRetriesActualOldParentDrift(t *testing.T) {
	create := func(title string, parentID *string) IssueResponse {
		body := map[string]any{"title": title, "status": "todo", "priority": "medium"}
		if parentID != nil {
			body["parent_issue_id"] = *parentID
		}
		w := httptest.NewRecorder()
		testHandler.CreateIssue(w, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body))
		if w.Code != http.StatusCreated {
			t.Fatalf("create issue status=%d body=%s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	parentA := create("drift-parent-a", nil)
	parentB := create("drift-parent-b", nil)
	parentC := create("drift-parent-c", nil)
	child := create("drift-child", &parentA.ID)
	stale, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(child.ID))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, withURLParam(newRequest(http.MethodPut, "/api/issues/"+child.ID, map[string]any{"parent_issue_id": parentB.ID}), "id", child.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("move to parent B status=%d body=%s", w.Code, w.Body.String())
	}
	newParent := parseUUID(parentC.ID)
	params := db.UpdateIssueParams{ID: stale.ID, ParentIssueID: newParent}
	updated, _, _, err := testHandler.updateIssueSerialized(
		context.Background(), stale, params, map[string]json.RawMessage{"parent_issue_id": json.RawMessage(`"` + parentC.ID + `"`)}, true,
		"topology_drift_test", "member", parseUUID(testUserID),
	)
	if err != nil {
		t.Fatalf("retry actual old-parent drift: %v", err)
	}
	if updated.ParentIssueID != newParent {
		t.Fatalf("parent after drift retry=%s want %s", uuidToString(updated.ParentIssueID), parentC.ID)
	}
}

func TestPullRequestCompletionPublicHandlerPublishesIssueBeforeParentWake(t *testing.T) {
	parent, child, _ := createCompletionPolicyIssuePairViaHandlers(t, "activity-order")
	var issuePublished atomic.Bool
	commentObserved := make(chan bool, 1)
	testHandler.Bus.Subscribe(protocol.EventIssueUpdated, func(event events.Event) {
		payload, _ := event.Payload.(map[string]any)
		if payload["source"] == "external_pr_merged" {
			issuePublished.Store(true)
		}
	})
	testHandler.Bus.Subscribe(protocol.EventCommentCreated, func(event events.Event) {
		payload, _ := event.Payload.(map[string]any)
		if payload["issue_title"] == parent.Title {
			commentObserved <- issuePublished.Load()
		}
	})
	if result := completeExternalPRViaHandler(t, child.ID, nextCompletionPolicyPRNumber()); result.Outcome != "completed" {
		t.Fatalf("completion result=%#v", result)
	}
	select {
	case ordered := <-commentObserved:
		if !ordered {
			t.Fatal("parent comment/wake became observable before issue event/activity")
		}
	default:
		t.Fatal("parent completion comment event was not observed")
	}
}

func TestCompletionPolicySerializesPublicExplicitAndProviderTerminalWriters(t *testing.T) {
	parent, child, nextStage := createCompletionPolicyIssuePairViaHandlers(t, "terminal-race")
	baseline := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID)
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "completion-policy-race-token")
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var providerStatus, explicitStatus int
	var provider externalCompleteFromPRResponse
	go func() {
		defer wg.Done()
		<-start
		req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge",
			externalPRCompletionReq(testWorkspaceID, child.ID, nextCompletionPolicyPRNumber()))
		req.Header.Set("Authorization", "Bearer completion-policy-race-token")
		w := httptest.NewRecorder()
		testHandler.CompleteIssueFromExternalPR(w, req)
		providerStatus = w.Code
		_ = json.NewDecoder(w.Body).Decode(&provider)
	}()
	go func() {
		defer wg.Done()
		<-start
		req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+child.ID, map[string]any{"status": "done"}), "id", child.ID)
		w := httptest.NewRecorder()
		testHandler.UpdateIssue(w, req)
		explicitStatus = w.Code
	}()
	close(start)
	wg.Wait()
	if explicitStatus != http.StatusOK || providerStatus != http.StatusOK {
		t.Fatalf("concurrent public statuses explicit=%d provider=%d", explicitStatus, providerStatus)
	}
	if provider.Outcome != "completed" && provider.Outcome != "already_done" {
		t.Fatalf("provider concurrent response=%#v", provider)
	}
	after := completionEffectSnapshotForIssues(t, parent.ID, nextStage.ID)
	want := baseline
	want.parentComments++
	want.parentTasks++
	want.parentQueued++
	if after != want {
		t.Fatalf("concurrent terminal release before=%#v after=%#v want=%#v", baseline, after, want)
	}
}

func reassignCompletionIssueForRun(t *testing.T, issue IssueResponse) {
	t.Helper()
	if issue.AssigneeID == nil {
		t.Fatal("completion issue has no assigned agent")
	}
	for _, body := range []map[string]any{
		{"assignee_type": nil, "assignee_id": nil},
		{"assignee_type": "agent", "assignee_id": *issue.AssigneeID, "suppress_run": false},
	} {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issue.ID, body), "id", issue.ID)
		testHandler.UpdateIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("public issue reassignment status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func claimCompletionTaskForIssue(t *testing.T, issueID string) string {
	t.Helper()
	runtimeID := handlerTestRuntimeID(t)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "completion-policy-stage-chain")
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Task *AgentTaskResponse `json:"task"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Task == nil || response.Task.IssueID != issueID {
		t.Fatalf("claimed task=%#v, want issue %s", response.Task, issueID)
	}
	return response.Task.ID
}

func startCompletionTaskViaHandler(t *testing.T, taskID string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", nil, testWorkspaceID, "completion-policy-stage-chain")
	req = withURLParam(req, "taskId", taskID)
	testHandler.StartTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("StartTask status=%d body=%s", w.Code, w.Body.String())
	}
}

func completeCompletionTaskViaHandler(t *testing.T, taskID string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", TaskCompleteRequest{Output: "completion policy stage proof"}, testWorkspaceID, "completion-policy-stage-chain")
	req = withURLParam(req, "taskId", taskID)
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("CompleteTask status=%d body=%s", w.Code, w.Body.String())
	}
}

func createCompletionPolicyIssuePairViaHandlers(t *testing.T, suffix string) (IssueResponse, IssueResponse, IssueResponse) {
	t.Helper()
	create := func(body map[string]any) IssueResponse {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
		testHandler.CreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateIssue status=%d body=%s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatalf("decode created issue: %v", err)
		}
		return issue
	}
	sequence := nextCompletionPolicyPRNumber()
	parentAgentID := createHandlerTestAgent(t, fmt.Sprintf("completion-policy-%s-%d", suffix, sequence), []byte(`{}`))
	parent := create(map[string]any{
		"title":    fmt.Sprintf("completion policy %s parent %d", suffix, sequence),
		"status":   "in_progress",
		"priority": "medium",
	})
	assignReq := withURLParam(newRequest(http.MethodPut, "/api/issues/"+parent.ID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   parentAgentID,
		"suppress_run":  true,
	}), "id", parent.ID)
	assignW := httptest.NewRecorder()
	testHandler.UpdateIssue(assignW, assignReq)
	if assignW.Code != http.StatusOK {
		t.Fatalf("assign completion parent status=%d body=%s", assignW.Code, assignW.Body.String())
	}
	if err := json.NewDecoder(assignW.Body).Decode(&parent); err != nil {
		t.Fatalf("decode assigned parent: %v", err)
	}
	child := create(map[string]any{
		"title":           fmt.Sprintf("completion policy %s child %d", suffix, sequence),
		"status":          "todo",
		"priority":        "medium",
		"parent_issue_id": parent.ID,
		"stage":           1,
	})
	nextStage := create(map[string]any{
		"title":           fmt.Sprintf("completion policy %s stage 2 %d", suffix, sequence),
		"status":          "backlog",
		"priority":        "medium",
		"parent_issue_id": parent.ID,
		"stage":           2,
	})
	child = assignCompletionIssueAgentViaHandler(t, child.ID, parentAgentID)
	nextStage = assignCompletionIssueAgentViaHandler(t, nextStage.ID, parentAgentID)
	t.Cleanup(func() {
		ctx := context.Background()
		_ = testHandler.Queries.DeleteIssue(ctx, db.DeleteIssueParams{ID: parseUUID(nextStage.ID), WorkspaceID: parseUUID(testWorkspaceID)})
		_ = testHandler.Queries.DeleteIssue(ctx, db.DeleteIssueParams{ID: parseUUID(child.ID), WorkspaceID: parseUUID(testWorkspaceID)})
		_ = testHandler.Queries.DeleteIssue(ctx, db.DeleteIssueParams{ID: parseUUID(parent.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	})
	return parent, child, nextStage
}

func assignCompletionIssueAgentViaHandler(t *testing.T, issueID, agentID string) IssueResponse {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   agentID,
		"suppress_run":  true,
	}), "id", issueID)
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("assign completion child status=%d body=%s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode assigned child: %v", err)
	}
	return issue
}

func setCompletionPolicyForMatrix(t *testing.T, issueID string, value any) {
	t.Helper()
	setCompletionPolicyViaHandler(t, issueID, value)
}

func setCompletionPolicyViaHandler(t *testing.T, issueID string, value any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	req := withURLParams(newRequest(http.MethodPut, "/api/issues/"+issueID+"/metadata/external_pr_completion_policy", json.RawMessage(raw)),
		"id", issueID, "key", "external_pr_completion_policy")
	w := httptest.NewRecorder()
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SetIssueMetadataKey status=%d body=%s", w.Code, w.Body.String())
	}
}

func completeExternalPRViaHandler(t *testing.T, issueID string, number int32) externalCompleteFromPRResponse {
	t.Helper()
	return completeExternalPRWithHandler(t, testHandler, issueID, number)
}

func completeExternalPRWithHandler(t *testing.T, h *Handler, issueID string, number int32) externalCompleteFromPRResponse {
	t.Helper()
	t.Setenv("MULTICA_EXTERNAL_PR_SERVICE_TOKEN", "completion-policy-test-token")
	request := externalPRCompletionReq(testWorkspaceID, issueID, number)
	req := newRequest(http.MethodPost, "/api/integrations/external-pr/complete-from-merge", request)
	req.Header.Set("Authorization", "Bearer completion-policy-test-token")
	w := httptest.NewRecorder()
	h.CompleteIssueFromExternalPR(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("CompleteIssueFromExternalPR status=%d body=%s", w.Code, w.Body.String())
	}
	var response externalCompleteFromPRResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode completion response: %v", err)
	}
	return response
}

func updateIssueStatusViaHandler(t *testing.T, issueID, status string) {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{"status": status}), "id", issueID)
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue status=%d body=%s", w.Code, w.Body.String())
	}
}

func countParentSystemComments(t *testing.T, issueID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT COUNT(*)::int FROM comment
WHERE issue_id=$1 AND author_type='system' AND type='system'`, issueID).Scan(&count); err != nil {
		t.Fatalf("count parent system comments: %v", err)
	}
	return count
}

type completionEffectSnapshot struct {
	parentComments   int
	parentTasks      int
	parentQueued     int
	parentDispatched int
	parentRunning    int
	nextStageTasks   int
	nextStageQueued  int
	nextDispatched   int
	nextRunning      int
	parentInbox      int
	nextStageInbox   int
	nextStageStatus  string
}

func completionEffectSnapshotForIssues(t *testing.T, parentID, nextStageID string) completionEffectSnapshot {
	t.Helper()
	var snapshot completionEffectSnapshot
	if err := testPool.QueryRow(context.Background(), `
SELECT
  (SELECT COUNT(*)::int FROM comment WHERE issue_id=$1 AND author_type='system' AND type='system'),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$1),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$1 AND status='queued'),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$1 AND status='dispatched'),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$1 AND status='running'),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$2),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$2 AND status='queued'),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$2 AND status='dispatched'),
  (SELECT COUNT(*)::int FROM agent_task_queue WHERE issue_id=$2 AND status='running'),
  (SELECT COUNT(*)::int FROM inbox_item WHERE issue_id=$1),
  (SELECT COUNT(*)::int FROM inbox_item WHERE issue_id=$2),
  (SELECT status FROM issue WHERE id=$2)
`, parentID, nextStageID).Scan(
		&snapshot.parentComments,
		&snapshot.parentTasks,
		&snapshot.parentQueued,
		&snapshot.parentDispatched,
		&snapshot.parentRunning,
		&snapshot.nextStageTasks,
		&snapshot.nextStageQueued,
		&snapshot.nextDispatched,
		&snapshot.nextRunning,
		&snapshot.parentInbox,
		&snapshot.nextStageInbox,
		&snapshot.nextStageStatus,
	); err != nil {
		t.Fatalf("capture completion effects: %v", err)
	}
	return snapshot
}

func assertOrdinaryAssignedStageEffects(t *testing.T, before, after completionEffectSnapshot, expectNewParentTask bool) {
	t.Helper()
	if after.parentComments != before.parentComments+1 {
		t.Fatalf("parent comments=%d want=%d", after.parentComments, before.parentComments+1)
	}
	wantTaskDelta := 0
	if expectNewParentTask {
		wantTaskDelta = 1
	}
	if after.parentTasks != before.parentTasks+wantTaskDelta || after.parentQueued != before.parentQueued+wantTaskDelta {
		t.Fatalf("parent task/queued after=%d/%d before=%d/%d, want delta=%d", after.parentTasks, after.parentQueued, before.parentTasks, before.parentQueued, wantTaskDelta)
	}
	if after.parentDispatched != before.parentDispatched || after.parentRunning != before.parentRunning {
		t.Fatalf("parent dispatch/run changed before=%d/%d after=%d/%d before daemon claim", before.parentDispatched, before.parentRunning, after.parentDispatched, after.parentRunning)
	}
	if after.parentInbox != before.parentInbox {
		t.Fatalf("parent inbox changed from %d to %d", before.parentInbox, after.parentInbox)
	}
	if after.nextStageTasks != before.nextStageTasks || after.nextStageQueued != before.nextStageQueued || after.nextDispatched != before.nextDispatched || after.nextRunning != before.nextRunning || after.nextStageInbox != before.nextStageInbox || after.nextStageStatus != before.nextStageStatus {
		t.Fatalf("next-stage effects changed before=%#v after=%#v", before, after)
	}
}

func nextCompletionPolicyPRNumber() int32 {
	return 30000 + completionPolicyTestSequence.Add(1)
}
