package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkCoordinationBlockerCanonicalGoldens(t *testing.T) {
	actor := CoordinationActor{
		WorkspaceID: util.MustParseUUID("00000000-0000-0000-0000-000000000000"),
		ActorType:   CoordinationActorMember,
		ActorID:     util.MustParseUUID("00000000-0000-0000-0000-000000000002"),
	}
	input := AppendBlockerInput{
		ScopeID: util.MustParseUUID("00000000-0000-0000-0000-000000000006"), ExpectedRevision: 2,
		DownstreamIssueID: util.MustParseUUID("00000000-0000-0000-0000-000000000007"),
		UpstreamIssueID:   util.MustParseUUID("00000000-0000-0000-0000-000000000008"),
		SchemaVersion:     1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "append-golden",
		EvidenceRefs: []CoordinationEvidenceRef{
			{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000b")},
			{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000a")},
		},
	}
	canonical, err := AppendBlockerCanonicalJSON(actor, input)
	if err != nil {
		t.Fatalf("append canonical: %v", err)
	}
	want := `{"actor":{"id":"00000000-0000-0000-0000-000000000002","task_id":null,"type":"member"},"hash_version":1,"operation":"append_blocker","request":{"dependency_id":null,"downstream_issue_id":"00000000-0000-0000-0000-000000000007","expected_revision":"2","payload":{"evidence_refs":[{"id":"00000000-0000-0000-0000-00000000000a","kind":"issue"},{"id":"00000000-0000-0000-0000-00000000000b","kind":"issue"}],"reason_code":"waiting_on_issue"},"schema_version":1,"scope_id":"00000000-0000-0000-0000-000000000006","upstream_issue_id":"00000000-0000-0000-0000-000000000008"},"workspace_id":"00000000-0000-0000-0000-000000000000"}`
	if string(canonical) != want {
		t.Fatalf("append canonical mismatch\n got: %s\nwant: %s", canonical, want)
	}
	sum := sha256.Sum256([]byte(want))
	hash, err := AppendBlockerRequestHash(actor, input)
	if err != nil || hex.EncodeToString(hash) != hex.EncodeToString(sum[:]) {
		t.Fatalf("append hash=%x want=%x err=%v", hash, sum, err)
	}

	withDependency := input
	withDependency.DependencyID = util.MustParseUUID("00000000-0000-0000-0000-000000000009")
	withDependency.EvidenceRefs = []CoordinationEvidenceRef{
		{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000a")},
		{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000b")},
	}
	withDependencyHash, err := AppendBlockerRequestHash(actor, withDependency)
	if err != nil || hex.EncodeToString(withDependencyHash) == hex.EncodeToString(hash) {
		t.Fatalf("dependency must change append hash: %x %x err=%v", hash, withDependencyHash, err)
	}
	reordered := input
	reordered.EvidenceRefs = []CoordinationEvidenceRef{
		{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000a")},
		{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000b")},
	}
	if reorderedHash, err := AppendBlockerRequestHash(actor, reordered); err != nil || hex.EncodeToString(reorderedHash) != hex.EncodeToString(hash) {
		t.Fatalf("evidence order must normalize: %x %x err=%v", hash, reorderedHash, err)
	}
	assertAppendHashDifferent := func(name string, changedActor CoordinationActor, changedInput AppendBlockerInput) {
		t.Helper()
		changed, err := AppendBlockerRequestHash(changedActor, changedInput)
		if err != nil || bytes.Equal(changed, hash) {
			t.Fatalf("%s must change append hash: %x %x err=%v", name, hash, changed, err)
		}
	}
	changedEndpoint := input
	changedEndpoint.UpstreamIssueID = util.MustParseUUID("00000000-0000-0000-0000-00000000000c")
	assertAppendHashDifferent("endpoint", actor, changedEndpoint)
	changedRevision := input
	changedRevision.ExpectedRevision++
	assertAppendHashDifferent("expected revision", actor, changedRevision)
	changedEvidence := input
	changedEvidence.EvidenceRefs = []CoordinationEvidenceRef{{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000c")}}
	assertAppendHashDifferent("payload evidence", actor, changedEvidence)
	changedActor := actor
	changedActor.ActorID = util.MustParseUUID("00000000-0000-0000-0000-000000000003")
	assertAppendHashDifferent("actor id", changedActor, input)
	agentActor := CoordinationActor{
		WorkspaceID: actor.WorkspaceID, ActorType: CoordinationActorAgent,
		ActorID:           util.MustParseUUID("00000000-0000-0000-0000-000000000003"),
		TaskID:            util.MustParseUUID("00000000-0000-0000-0000-000000000004"),
		TaskCredentialRef: util.MustParseUUID("00000000-0000-0000-0000-00000000000d"),
	}
	assertAppendHashDifferent("actor type", agentActor, input)
	changedTaskActor := agentActor
	changedTaskActor.TaskID = util.MustParseUUID("00000000-0000-0000-0000-000000000005")
	agentHash, err := AppendBlockerRequestHash(agentActor, input)
	if err != nil {
		t.Fatalf("agent append hash: %v", err)
	}
	changedTaskHash, err := AppendBlockerRequestHash(changedTaskActor, input)
	if err != nil || bytes.Equal(agentHash, changedTaskHash) {
		t.Fatalf("task id must change append hash: %x %x err=%v", agentHash, changedTaskHash, err)
	}
	invalidSchema := input
	invalidSchema.SchemaVersion = 2
	if _, err := AppendBlockerRequestHash(actor, invalidSchema); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("schema drift must be rejected: %v", err)
	}

	resolve := ResolveBlockerInput{
		ScopeID: input.ScopeID, BlockerID: util.MustParseUUID("00000000-0000-0000-0000-000000000009"),
		ExpectedRevision: 3, SchemaVersion: 1, ResolutionCode: CoordinationBlockerResolutionNoLongerBlocking,
		EvidenceRefs:   []CoordinationEvidenceRef{{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000a")}},
		IdempotencyKey: "resolve-golden",
	}
	resolveCanonical, err := ResolveBlockerCanonicalJSON(actor, resolve)
	if err != nil {
		t.Fatalf("resolve canonical: %v", err)
	}
	wantResolve := `{"actor":{"id":"00000000-0000-0000-0000-000000000002","task_id":null,"type":"member"},"hash_version":1,"operation":"resolve_blocker","request":{"expected_revision":"3","record_id":"00000000-0000-0000-0000-000000000009","resolution":{"evidence_refs":[{"id":"00000000-0000-0000-0000-00000000000a","kind":"issue"}],"resolution_code":"no_longer_blocking"},"schema_version":1,"scope_id":"00000000-0000-0000-0000-000000000006"},"workspace_id":"00000000-0000-0000-0000-000000000000"}`
	if string(resolveCanonical) != wantResolve {
		t.Fatalf("resolve canonical mismatch\n got: %s\nwant: %s", resolveCanonical, wantResolve)
	}
	resolveHash, err := ResolveBlockerRequestHash(actor, resolve)
	if err != nil {
		t.Fatalf("resolve hash: %v", err)
	}
	assertResolveHashDifferent := func(name string, changedActor CoordinationActor, changedInput ResolveBlockerInput) {
		t.Helper()
		changed, err := ResolveBlockerRequestHash(changedActor, changedInput)
		if err != nil || bytes.Equal(changed, resolveHash) {
			t.Fatalf("%s must change resolve hash: %x %x err=%v", name, resolveHash, changed, err)
		}
	}
	changedRecord := resolve
	changedRecord.BlockerID = util.MustParseUUID("00000000-0000-0000-0000-00000000000c")
	assertResolveHashDifferent("record", actor, changedRecord)
	changedResolveRevision := resolve
	changedResolveRevision.ExpectedRevision++
	assertResolveHashDifferent("resolve revision", actor, changedResolveRevision)
	changedResolution := resolve
	changedResolution.ResolutionCode = CoordinationBlockerResolutionSuperseded
	assertResolveHashDifferent("resolution code", actor, changedResolution)
	changedResolutionEvidence := resolve
	changedResolutionEvidence.EvidenceRefs = []CoordinationEvidenceRef{{Kind: "issue", ID: util.MustParseUUID("00000000-0000-0000-0000-00000000000b")}}
	assertResolveHashDifferent("resolution evidence", actor, changedResolutionEvidence)
	assertResolveHashDifferent("resolve actor", changedActor, resolve)
	agentResolveHash, err := ResolveBlockerRequestHash(agentActor, resolve)
	if err != nil {
		t.Fatalf("agent resolve hash: %v", err)
	}
	changedTaskResolveHash, err := ResolveBlockerRequestHash(changedTaskActor, resolve)
	if err != nil || bytes.Equal(agentResolveHash, changedTaskResolveHash) {
		t.Fatalf("task id must change resolve hash: %x %x err=%v", agentResolveHash, changedTaskResolveHash, err)
	}
}

func TestWorkCoordinationBlockerValidation(t *testing.T) {
	base := AppendBlockerInput{
		ScopeID: util.MustParseUUID("00000000-0000-0000-0000-000000000001"), ExpectedRevision: 0,
		DownstreamIssueID: util.MustParseUUID("00000000-0000-0000-0000-000000000002"),
		UpstreamIssueID:   util.MustParseUUID("00000000-0000-0000-0000-000000000003"),
		SchemaVersion:     1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "valid",
	}
	cases := []AppendBlockerInput{
		func() AppendBlockerInput { value := base; value.SchemaVersion = 2; return value }(),
		func() AppendBlockerInput { value := base; value.ReasonCode = "free_text"; return value }(),
		func() AppendBlockerInput {
			value := base
			value.UpstreamIssueID = value.DownstreamIssueID
			return value
		}(),
		func() AppendBlockerInput {
			value := base
			value.EvidenceRefs = []CoordinationEvidenceRef{{Kind: "url", ID: value.UpstreamIssueID}}
			return value
		}(),
		func() AppendBlockerInput {
			value := base
			value.EvidenceRefs = []CoordinationEvidenceRef{{Kind: "issue", ID: value.UpstreamIssueID}, {Kind: "issue", ID: value.UpstreamIssueID}}
			return value
		}(),
	}
	tooMany := base
	for index := 0; index < 33; index++ {
		tooMany.EvidenceRefs = append(tooMany.EvidenceRefs, CoordinationEvidenceRef{Kind: "issue", ID: util.MustParseUUID(fmt.Sprintf("00000000-0000-0000-0000-%012x", index+10))})
	}
	cases = append(cases, tooMany)
	for index, input := range cases {
		if _, err := validateAppendBlockerInput(input); coordinationCode(err) != CoordinationInvalidPayload {
			t.Fatalf("append validation case %d code=%q err=%v", index, coordinationCode(err), err)
		}
	}
	resolve := ResolveBlockerInput{ScopeID: base.ScopeID, BlockerID: base.UpstreamIssueID, ExpectedRevision: 0, SchemaVersion: 1, ResolutionCode: "unknown", IdempotencyKey: "valid"}
	if _, err := validateResolveBlockerInput(resolve); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("resolve invalid code=%q err=%v", coordinationCode(err), err)
	}
}

func TestWorkCoordinationBlockerLifecycleAndIndependentResolve(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	fixture := createWorkCoordinationFixture(t, pool)
	ctx := context.Background()
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2101, "blocker-downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 2102, "blocker-upstream")
	evidenceA := createWorkCoordinationChildIssue(t, pool, fixture, 2103, "blocker-evidence-a")
	evidenceB := createWorkCoordinationChildIssue(t, pool, fixture, 2104, "blocker-evidence-b")

	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "blocker-v1", IdempotencyKey: "blocker-scope"})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	dependency, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "blocker-dependency",
	})
	if err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	appendInput := AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		DependencyID: dependency.Dependency.ID, SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue,
		EvidenceRefs: []CoordinationEvidenceRef{{Kind: "issue", ID: evidenceB}, {Kind: "issue", ID: evidenceA}}, IdempotencyKey: "blocker-append",
	}
	created, err := svc.AppendBlocker(ctx, actor, appendInput)
	if err != nil {
		t.Fatalf("append blocker: %v", err)
	}
	if created.Outcome != CoordinationOutcomeCreated || !created.Changed || created.ScopeRevision != 2 || created.Blocker.Status != "open" ||
		created.Receipt.Operation != CoordinationOperationAppendBlocker || created.Receipt.ResourceType != CoordinationResourceBlocker || created.Receipt.RevisionBefore != 1 || created.Receipt.RevisionAfter != 2 ||
		len(created.Blocker.CreateEvidenceRefs) != 2 || util.UUIDToString(created.Blocker.CreateEvidenceRefs[0].ID) >= util.UUIDToString(created.Blocker.CreateEvidenceRefs[1].ID) {
		t.Fatalf("unexpected append result: %+v", created)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 2)

	replay, err := svc.AppendBlocker(ctx, actor, appendInput)
	if err != nil {
		t.Fatalf("append replay: %v", err)
	}
	if replay.Outcome != CoordinationOutcomeReplay || !replay.Changed || replay.ScopeRevision != 2 || replay.Receipt.ReceiptOrdinal != created.Receipt.ReceiptOrdinal || !uuidEqual(replay.Blocker.ID, created.Blocker.ID) {
		t.Fatalf("unexpected append replay: %+v", replay)
	}
	receiptRow, err := db.New(pool).GetCoordinationReceiptByIdempotencyKey(ctx, db.GetCoordinationReceiptByIdempotencyKeyParams{WorkspaceID: fixture.workspaceID, IdempotencyKey: appendInput.IdempotencyKey})
	if err != nil {
		t.Fatalf("load append receipt: %v", err)
	}
	cancelledContext, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := replayBlockerReceipt(cancelledContext, db.New(pool), receiptRow, scope.Scope.ID); coordinationCode(err) != CoordinationInternal {
		t.Fatalf("cancelled replay lookup code=%q err=%v", coordinationCode(err), err)
	}
	missingReceipt := receiptRow
	missingReceipt.ResourceID = util.MustParseUUID("dddddddd-dddd-dddd-dddd-dddddddddddd")
	if _, _, _, err := replayBlockerReceipt(ctx, db.New(pool), missingReceipt, scope.Scope.ID); coordinationCode(err) != CoordinationNotFound {
		t.Fatalf("missing replay resource code=%q err=%v", coordinationCode(err), err)
	}
	var otherUserID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name,email) VALUES ('WCS Other Member',$1) RETURNING id`, fmt.Sprintf("wcs-other-%d@multica.ai", time.Now().UnixNano())).Scan(&otherUserID); err != nil {
		t.Fatalf("insert other member user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'member')`, fixture.workspaceID, otherUserID); err != nil {
		t.Fatalf("insert other member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, fixture.workspaceID, otherUserID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, otherUserID)
	})
	otherActor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: otherUserID}
	if _, err := svc.AppendBlocker(ctx, otherActor, appendInput); coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("actor-conflicting replay code=%q err=%v", coordinationCode(err), err)
	}
	appendConflict := appendInput
	appendConflict.EvidenceRefs = []CoordinationEvidenceRef{{Kind: "issue", ID: evidenceA}}
	if _, err := svc.AppendBlocker(ctx, actor, appendConflict); coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("append idempotency conflict code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 2)
	staleAppend := appendInput
	staleAppend.IdempotencyKey = "blocker-append-stale-revision"
	staleAppend.ExpectedRevision = 1
	if _, err := svc.AppendBlocker(ctx, actor, staleAppend); coordinationCode(err) != CoordinationRevisionConflict {
		t.Fatalf("stale append code=%q err=%v", coordinationCode(err), err)
	}
	var staleReceipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE workspace_id=$1 AND idempotency_key=$2`, fixture.workspaceID, staleAppend.IdempotencyKey).Scan(&staleReceipts); err != nil || staleReceipts != 0 {
		t.Fatalf("stale append receipts=%d err=%v", staleReceipts, err)
	}

	secondInput := appendInput
	secondInput.ExpectedRevision = 2
	secondInput.IdempotencyKey = "blocker-append-second"
	second, err := svc.AppendBlocker(ctx, actor, secondInput)
	if err != nil {
		t.Fatalf("append second evidence record: %v", err)
	}
	if !second.Changed || second.ScopeRevision != 3 || uuidEqual(second.Blocker.ID, created.Blocker.ID) {
		t.Fatalf("fresh-key duplicate payload must create a distinct record: %+v", second)
	}

	page, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "open", "", 100)
	if err != nil || len(page.Blockers) != 2 || page.ScopeRevision != 3 {
		t.Fatalf("list open blockers=%+v err=%v", page, err)
	}
	resolveInput := ResolveBlockerInput{
		ScopeID: scope.Scope.ID, BlockerID: created.Blocker.ID, ExpectedRevision: 3, SchemaVersion: 1,
		ResolutionCode: CoordinationBlockerResolutionNoLongerBlocking,
		EvidenceRefs:   []CoordinationEvidenceRef{{Kind: "issue", ID: evidenceA}}, IdempotencyKey: "blocker-resolve",
	}
	resolved, err := svc.ResolveBlocker(ctx, actor, resolveInput)
	if err != nil {
		t.Fatalf("resolve blocker: %v", err)
	}
	if resolved.Outcome != CoordinationOutcomeResolved || !resolved.Changed || resolved.ScopeRevision != 4 || resolved.Blocker.Status != "resolved" ||
		resolved.Blocker.ResolutionCode != CoordinationBlockerResolutionNoLongerBlocking || len(resolved.Blocker.ResolutionEvidenceRefs) != 1 || resolved.Receipt.RevisionBefore != 3 || resolved.Receipt.RevisionAfter != 4 {
		t.Fatalf("unexpected resolve result: %+v", resolved)
	}
	activeDependencies, err := svc.ListDependencies(ctx, actor, scope.Scope.ID, "", 100)
	if err != nil || len(activeDependencies.Dependencies) != 1 {
		t.Fatalf("blocker resolve must not resolve dependency: %+v err=%v", activeDependencies, err)
	}

	resolveReplay, err := svc.ResolveBlocker(ctx, actor, resolveInput)
	if err != nil || resolveReplay.Outcome != CoordinationOutcomeReplay || !resolveReplay.Changed || resolveReplay.ScopeRevision != 4 {
		t.Fatalf("resolve replay=%+v err=%v", resolveReplay, err)
	}
	resolveConflict := resolveInput
	resolveConflict.ResolutionCode = CoordinationBlockerResolutionSuperseded
	if _, err := svc.ResolveBlocker(ctx, actor, resolveConflict); coordinationCode(err) != CoordinationIdempotencyConflict {
		t.Fatalf("resolve idempotency conflict code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 4)
	noopInput := resolveInput
	noopInput.ExpectedRevision = 4
	noopInput.IdempotencyKey = "blocker-resolve-noop"
	noop, err := svc.ResolveBlocker(ctx, actor, noopInput)
	if err != nil {
		t.Fatalf("resolve new-key noop: %v", err)
	}
	if noop.Outcome != CoordinationOutcomeNoop || noop.Changed || noop.ScopeRevision != 4 || noop.Receipt.ReceiptOrdinal <= resolved.Receipt.ReceiptOrdinal || noop.Receipt.RevisionBefore != 4 || noop.Receipt.RevisionAfter != 4 {
		t.Fatalf("unexpected resolve noop: %+v", noop)
	}

	dependencyResolved, err := svc.ResolveDependency(ctx, actor, ResolveDependencyInput{
		ScopeID: scope.Scope.ID, DependencyID: dependency.Dependency.ID, ExpectedRevision: 4, IdempotencyKey: "dependency-resolve-after-blocker",
	})
	if err != nil || dependencyResolved.ScopeRevision != 5 {
		t.Fatalf("resolve dependency independently=%+v err=%v", dependencyResolved, err)
	}
	lateResolveReplay, err := svc.ResolveBlocker(ctx, actor, resolveInput)
	if err != nil || lateResolveReplay.Outcome != CoordinationOutcomeReplay || lateResolveReplay.ScopeRevision != 4 || !lateResolveReplay.Changed {
		t.Fatalf("resolve replay must return saved revision after current advance: %+v err=%v", lateResolveReplay, err)
	}
	if _, err := svc.AppendBlocker(ctx, actor, appendInput); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("append replay after dependency resolve code=%q err=%v", coordinationCode(err), err)
	}
	resolvedPage, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "resolved", "", 100)
	if err != nil || len(resolvedPage.Blockers) != 1 || !uuidEqual(resolvedPage.Blockers[0].ID, created.Blocker.ID) {
		t.Fatalf("resolved blockers=%+v err=%v", resolvedPage, err)
	}
	openPage, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "open", "", 100)
	if err != nil || len(openPage.Blockers) != 1 || !uuidEqual(openPage.Blockers[0].ID, second.Blocker.ID) {
		t.Fatalf("open blockers=%+v err=%v", openPage, err)
	}
	resolvedLinkInput := appendInput
	resolvedLinkInput.ExpectedRevision = 5
	resolvedLinkInput.IdempotencyKey = "blocker-append-resolved-dependency"
	if _, err := svc.AppendBlocker(ctx, actor, resolvedLinkInput); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("resolved dependency link code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 5)
	mismatchedDependency, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 5, DownstreamIssueID: evidenceA, UpstreamIssueID: evidenceB, IdempotencyKey: "blocker-mismatched-dependency",
	})
	if err != nil || mismatchedDependency.ScopeRevision != 6 {
		t.Fatalf("add mismatched dependency=%+v err=%v", mismatchedDependency, err)
	}
	mismatchInput := appendInput
	mismatchInput.ExpectedRevision = 6
	mismatchInput.DependencyID = mismatchedDependency.Dependency.ID
	mismatchInput.IdempotencyKey = "blocker-append-mismatched-link"
	if _, err := svc.AppendBlocker(ctx, actor, mismatchInput); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("mismatched dependency link code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 6)
}

func TestWorkCoordinationBlockerSoftReferenceValidation(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	foreign := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	foreignActor := CoordinationActor{WorkspaceID: foreign.workspaceID, ActorType: CoordinationActorMember, ActorID: foreign.userID}
	downstream := createWorkCoordinationChildIssue(t, pool, fixture, 2151, "blocker-ref-downstream")
	upstream := createWorkCoordinationChildIssue(t, pool, fixture, 2152, "blocker-ref-upstream")
	otherDownstream := createWorkCoordinationChildIssue(t, pool, fixture, 2153, "blocker-ref-other-downstream")
	otherUpstream := createWorkCoordinationChildIssue(t, pool, fixture, 2154, "blocker-ref-other-upstream")
	foreignEndpoint := createWorkCoordinationChildIssue(t, pool, foreign, 2155, "blocker-ref-foreign-endpoint")
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "blocker-refs-a", IdempotencyKey: "blocker-refs-scope-a"})
	if err != nil {
		t.Fatalf("ensure scope a: %v", err)
	}
	scopeB, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "blocker-refs-b", IdempotencyKey: "blocker-refs-scope-b"})
	if err != nil {
		t.Fatalf("ensure scope b: %v", err)
	}
	foreignScope, err := svc.EnsureScope(ctx, foreignActor, EnsureScopeInput{RootIssueID: foreign.issueID, WorkflowProfileKey: "blocker-refs-foreign", IdempotencyKey: "blocker-refs-scope-foreign"})
	if err != nil {
		t.Fatalf("ensure foreign scope: %v", err)
	}
	foreignDependency, err := svc.AddDependency(ctx, foreignActor, AddDependencyInput{
		ScopeID: foreignScope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: foreign.issueID, UpstreamIssueID: foreignEndpoint, IdempotencyKey: "blocker-refs-foreign-dependency",
	})
	if err != nil {
		t.Fatalf("add foreign dependency: %v", err)
	}
	base := AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue,
	}
	missingDependency := base
	missingDependency.DependencyID = util.MustParseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")
	missingDependency.IdempotencyKey = "blocker-refs-missing-dependency"
	if _, err := svc.AppendBlocker(ctx, actor, missingDependency); coordinationCode(err) != CoordinationNotFound {
		t.Fatalf("missing dependency code=%q err=%v", coordinationCode(err), err)
	}
	foreignDependencyInput := base
	foreignDependencyInput.DependencyID = foreignDependency.Dependency.ID
	foreignDependencyInput.IdempotencyKey = "blocker-refs-foreign-dependency-use"
	if _, err := svc.AppendBlocker(ctx, actor, foreignDependencyInput); coordinationCode(err) != CoordinationNotFound {
		t.Fatalf("foreign dependency code=%q err=%v", coordinationCode(err), err)
	}
	var legacyDependencyID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue_dependency (issue_id,depends_on_issue_id,type) VALUES ($1,$2,'blocked_by') RETURNING id`, downstream, upstream).Scan(&legacyDependencyID); err != nil {
		t.Fatalf("insert legacy dependency: %v", err)
	}
	legacyInput := base
	legacyInput.DependencyID = legacyDependencyID
	legacyInput.IdempotencyKey = "blocker-refs-legacy-dependency"
	if _, err := svc.AppendBlocker(ctx, actor, legacyInput); coordinationCode(err) != CoordinationNotFound {
		t.Fatalf("legacy dependency code=%q err=%v", coordinationCode(err), err)
	}
	missingEvidence := base
	missingEvidence.EvidenceRefs = []CoordinationEvidenceRef{{Kind: "issue", ID: util.MustParseUUID("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")}}
	missingEvidence.IdempotencyKey = "blocker-refs-missing-evidence"
	if _, err := svc.AppendBlocker(ctx, actor, missingEvidence); coordinationCode(err) != CoordinationNotFound {
		t.Fatalf("missing evidence code=%q err=%v", coordinationCode(err), err)
	}
	foreignEvidence := base
	foreignEvidence.EvidenceRefs = []CoordinationEvidenceRef{{Kind: "issue", ID: foreignEndpoint}}
	foreignEvidence.IdempotencyKey = "blocker-refs-foreign-evidence"
	if _, err := svc.AppendBlocker(ctx, actor, foreignEvidence); coordinationCode(err) != CoordinationCrossWorkspace {
		t.Fatalf("foreign evidence code=%q err=%v", coordinationCode(err), err)
	}
	dependencyA, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: downstream, UpstreamIssueID: upstream, IdempotencyKey: "blocker-refs-dependency-a",
	})
	if err != nil {
		t.Fatalf("add dependency a: %v", err)
	}
	dependencyB, err := svc.AddDependency(ctx, actor, AddDependencyInput{
		ScopeID: scopeB.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: otherDownstream, UpstreamIssueID: otherUpstream, IdempotencyKey: "blocker-refs-dependency-b",
	})
	if err != nil {
		t.Fatalf("add dependency b: %v", err)
	}
	ownerMismatch := base
	ownerMismatch.ExpectedRevision = 1
	ownerMismatch.DownstreamIssueID = otherDownstream
	ownerMismatch.UpstreamIssueID = otherUpstream
	ownerMismatch.DependencyID = dependencyB.Dependency.ID
	ownerMismatch.IdempotencyKey = "blocker-refs-owner-mismatch"
	if _, err := svc.AppendBlocker(ctx, actor, ownerMismatch); coordinationCode(err) != CoordinationDependencyScopeConflict {
		t.Fatalf("dependency owner mismatch code=%q err=%v", coordinationCode(err), err)
	}
	endpointMismatch := base
	endpointMismatch.ExpectedRevision = 1
	endpointMismatch.DownstreamIssueID = otherDownstream
	endpointMismatch.UpstreamIssueID = otherUpstream
	endpointMismatch.DependencyID = dependencyA.Dependency.ID
	endpointMismatch.IdempotencyKey = "blocker-refs-endpoint-mismatch"
	if _, err := svc.AppendBlocker(ctx, actor, endpointMismatch); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("dependency endpoint mismatch code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 1)
	var records int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_record WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&records); err != nil || records != 0 {
		t.Fatalf("invalid soft refs wrote records=%d err=%v", records, err)
	}
}

func TestWorkCoordinationBlockerPaginationAgentAuthorityAndReplayRevocation(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	fixture := createWorkCoordinationFixture(t, pool)
	ctx := context.Background()
	svc := NewCoordinationService(db.New(pool), pool)
	member := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	child := createWorkCoordinationChildIssue(t, pool, fixture, 2201, "blocker-agent-child")
	other := createWorkCoordinationChildIssue(t, pool, fixture, 2202, "blocker-agent-other")
	agentFixture := createWorkCoordinationAgentFixture(t, pool, fixture)
	agent := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorAgent, ActorID: agentFixture.agentID, TaskID: agentFixture.taskID, TaskCredentialRef: agentFixture.tokenOneID}
	scope, err := svc.EnsureScope(ctx, member, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "blocker-agent", IdempotencyKey: "blocker-agent-scope"})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	input := AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: fixture.issueID, UpstreamIssueID: child,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, EvidenceRefs: []CoordinationEvidenceRef{}, IdempotencyKey: "blocker-agent-append",
	}
	first, err := svc.AppendBlocker(ctx, agent, input)
	if err != nil {
		t.Fatalf("agent append: %v", err)
	}
	if _, err := svc.AppendBlocker(ctx, agent, AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: child, UpstreamIssueID: other,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "blocker-agent-unrelated",
	}); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("agent unrelated endpoint code=%q err=%v", coordinationCode(err), err)
	}
	second, err := svc.AppendBlocker(ctx, member, AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: fixture.issueID, UpstreamIssueID: other,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "blocker-member-second",
	})
	if err != nil {
		t.Fatalf("member append second: %v", err)
	}
	unrelated, err := svc.AppendBlocker(ctx, member, AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 2, DownstreamIssueID: child, UpstreamIssueID: other,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "blocker-member-unrelated",
	})
	if err != nil {
		t.Fatalf("member append unrelated: %v", err)
	}
	memberPage, err := svc.ListBlockers(ctx, member, scope.Scope.ID, "open", "", 100)
	if err != nil || len(memberPage.Blockers) != 3 {
		t.Fatalf("member page=%+v err=%v", memberPage, err)
	}
	agentPage, err := svc.ListBlockers(ctx, agent, scope.Scope.ID, "open", "", 100)
	if err != nil || len(agentPage.Blockers) != 2 {
		t.Fatalf("agent page=%+v err=%v", agentPage, err)
	}
	for _, blocker := range agentPage.Blockers {
		if !uuidEqual(blocker.DownstreamIssueID, fixture.issueID) && !uuidEqual(blocker.UpstreamIssueID, fixture.issueID) {
			t.Fatalf("agent saw unrelated blocker: %+v (unrelated=%s)", blocker, util.UUIDToString(unrelated.Blocker.ID))
		}
	}
	firstPage, err := svc.ListBlockers(ctx, member, scope.Scope.ID, "open", "", 1)
	if err != nil || len(firstPage.Blockers) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", firstPage, err)
	}
	if _, err := svc.ResolveBlocker(ctx, member, ResolveBlockerInput{
		ScopeID: scope.Scope.ID, BlockerID: second.Blocker.ID, ExpectedRevision: 3, SchemaVersion: 1,
		ResolutionCode: CoordinationBlockerResolutionSuperseded, IdempotencyKey: "blocker-member-resolve",
	}); err != nil {
		t.Fatalf("resolve between pages: %v", err)
	}
	if _, err := svc.ListBlockers(ctx, member, scope.Scope.ID, "open", firstPage.NextCursor, 1); coordinationCode(err) != CoordinationRevisionConflict {
		t.Fatalf("stale cursor code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := svc.ListBlockers(ctx, member, scope.Scope.ID, "resolved", firstPage.NextCursor, 1); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("status-bound cursor code=%q err=%v", coordinationCode(err), err)
	}
	agentResolved, err := svc.ResolveBlocker(ctx, agent, ResolveBlockerInput{
		ScopeID: scope.Scope.ID, BlockerID: first.Blocker.ID, ExpectedRevision: 4, SchemaVersion: 1,
		ResolutionCode: CoordinationBlockerResolutionNoLongerBlocking, IdempotencyKey: "blocker-agent-resolve",
	})
	if err != nil || !agentResolved.Changed || agentResolved.ScopeRevision != 5 {
		t.Fatalf("agent resolve=%+v err=%v", agentResolved, err)
	}
	expiredAgent := agent
	expiredAgent.TaskCredentialRef = agentFixture.tokenTwoID
	if _, err := pool.Exec(ctx, `UPDATE task_token SET expires_at=now()-interval '1 minute' WHERE id=$1`, agentFixture.tokenTwoID); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	if _, err := svc.ListBlockers(ctx, expiredAgent, scope.Scope.ID, "all", "", 100); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("list with expired token code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := svc.AppendBlocker(ctx, expiredAgent, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("replay with expired token code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET issue_id=NULL WHERE id=$1`, agentFixture.taskID); err != nil {
		t.Fatalf("clear task issue: %v", err)
	}
	if _, err := svc.ListBlockers(ctx, agent, scope.Scope.ID, "all", "", 100); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("run-only task list code=%q err=%v", coordinationCode(err), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET issue_id=$1 WHERE id=$2`, fixture.issueID, agentFixture.taskID); err != nil {
		t.Fatalf("restore task issue: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM task_token WHERE id=$1`, agentFixture.tokenOneID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := svc.AppendBlocker(ctx, agent, input); coordinationCode(err) != CoordinationForbidden {
		t.Fatalf("replay after revoke code=%q err=%v first=%s", coordinationCode(err), err, util.UUIDToString(first.Blocker.ID))
	}
}

func TestWorkCoordinationBlockerStablePaginationAtLimitAndTimestampTie(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	ctx := context.Background()
	fixture := createWorkCoordinationFixture(t, pool)
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	child := createWorkCoordinationChildIssue(t, pool, fixture, 2251, "blocker-pagination-child")
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "blocker-pagination", IdempotencyKey: "blocker-pagination-scope"})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO coordination_record (
    id,workspace_id,coordination_scope_id,kind,schema_version,status,root_issue_id,
    downstream_issue_id,upstream_issue_id,reason_code,created_by_type,created_by_id,created_at
)
SELECT gen_random_uuid(),$1,$2,'blocker',1,'open',$3,$3,$4,'waiting_on_issue','member',$5,
       '2026-01-01T00:00:00Z'::timestamptz
FROM generate_series(1,105)`, fixture.workspaceID, scope.Scope.ID, fixture.issueID, child, fixture.userID); err != nil {
		t.Fatalf("seed tied blockers: %v", err)
	}
	first, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "open", "", 100)
	if err != nil || len(first.Blockers) != 100 || first.NextCursor == "" || first.ScopeRevision != 0 {
		t.Fatalf("first page len=%d cursor=%q revision=%d err=%v", len(first.Blockers), first.NextCursor, first.ScopeRevision, err)
	}
	second, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "open", first.NextCursor, 100)
	if err != nil || len(second.Blockers) != 5 || second.NextCursor != "" || second.ScopeRevision != 0 {
		t.Fatalf("second page len=%d cursor=%q revision=%d err=%v", len(second.Blockers), second.NextCursor, second.ScopeRevision, err)
	}
	seen := make(map[string]struct{}, 105)
	var previous string
	for index, blocker := range append(append([]Blocker(nil), first.Blockers...), second.Blockers...) {
		id := util.UUIDToString(blocker.ID)
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate blocker across tied pages: %s", id)
		}
		seen[id] = struct{}{}
		if index > 0 && previous <= id {
			t.Fatalf("tied page order is not id DESC: previous=%s current=%s", previous, id)
		}
		previous = id
	}
	if len(seen) != 105 {
		t.Fatalf("seen blockers=%d", len(seen))
	}
	if _, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "open", "", 101); coordinationCode(err) != CoordinationInvalidPayload {
		t.Fatalf("limit 101 code=%q err=%v", coordinationCode(err), err)
	}
	defaultPage, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "open", "", 0)
	if err != nil || len(defaultPage.Blockers) != 100 {
		t.Fatalf("default page len=%d err=%v", len(defaultPage.Blockers), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE coordination_scope SET revision=revision+1 WHERE id=$1`, scope.Scope.ID); err != nil {
		t.Fatalf("advance scope revision: %v", err)
	}
	if _, err := svc.ListBlockers(ctx, actor, scope.Scope.ID, "open", first.NextCursor, 100); coordinationCode(err) != CoordinationRevisionConflict {
		t.Fatalf("mutated tied page cursor code=%q err=%v", coordinationCode(err), err)
	}
}

func TestWorkCoordinationBlockerCapacityBoundary(t *testing.T) {
	pool := openWorkCoordinationPool(t)
	fixture := createWorkCoordinationFixture(t, pool)
	ctx := context.Background()
	svc := NewCoordinationService(db.New(pool), pool)
	actor := CoordinationActor{WorkspaceID: fixture.workspaceID, ActorType: CoordinationActorMember, ActorID: fixture.userID}
	child := createWorkCoordinationChildIssue(t, pool, fixture, 2301, "blocker-capacity-child")
	scope, err := svc.EnsureScope(ctx, actor, EnsureScopeInput{RootIssueID: fixture.issueID, WorkflowProfileKey: "blocker-capacity", IdempotencyKey: "blocker-capacity-scope"})
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO coordination_record (
    id,workspace_id,coordination_scope_id,kind,schema_version,status,root_issue_id,
    downstream_issue_id,upstream_issue_id,reason_code,created_by_type,created_by_id
)
SELECT gen_random_uuid(),$1,$2,'blocker',1,'open',$3,$3,$4,'waiting_on_issue','member',$5
FROM generate_series(1,999)`, fixture.workspaceID, scope.Scope.ID, fixture.issueID, child, fixture.userID); err != nil {
		t.Fatalf("seed blocker capacity: %v", err)
	}
	filled, err := svc.AppendBlocker(ctx, actor, AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 0, DownstreamIssueID: fixture.issueID, UpstreamIssueID: child,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "blocker-capacity-fill-thousand",
	})
	if err != nil || filled.ScopeRevision != 1 {
		t.Fatalf("write 1000th open blocker=%+v err=%v", filled, err)
	}
	var beforeOverflowReceipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&beforeOverflowReceipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	_, err = svc.AppendBlocker(ctx, actor, AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 1, DownstreamIssueID: fixture.issueID, UpstreamIssueID: child,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "blocker-capacity-overflow",
	})
	if coordinationCode(err) != CoordinationCapacityExceeded {
		t.Fatalf("capacity code=%q err=%v", coordinationCode(err), err)
	}
	assertCoordinationScopeRevision(t, pool, scope.Scope.ID, 1)
	var records, receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_record WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&records); err != nil || records != 1000 {
		t.Fatalf("records=%d err=%v", records, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordination_receipt WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&receipts); err != nil || receipts != beforeOverflowReceipts {
		t.Fatalf("receipts=%d want=%d err=%v", receipts, beforeOverflowReceipts, err)
	}
	var seededID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM coordination_record WHERE coordination_scope_id=$1 AND status='open' ORDER BY id LIMIT 1`, scope.Scope.ID).Scan(&seededID); err != nil {
		t.Fatalf("select seeded blocker: %v", err)
	}
	resolved, err := svc.ResolveBlocker(ctx, actor, ResolveBlockerInput{
		ScopeID: scope.Scope.ID, BlockerID: seededID, ExpectedRevision: 1, SchemaVersion: 1,
		ResolutionCode: CoordinationBlockerResolutionSuperseded, IdempotencyKey: "blocker-capacity-resolve-one",
	})
	if err != nil || resolved.ScopeRevision != 2 {
		t.Fatalf("resolve one at capacity=%+v err=%v", resolved, err)
	}
	created, err := svc.AppendBlocker(ctx, actor, AppendBlockerInput{
		ScopeID: scope.Scope.ID, ExpectedRevision: 2, DownstreamIssueID: fixture.issueID, UpstreamIssueID: child,
		SchemaVersion: 1, ReasonCode: CoordinationBlockerReasonWaitingOnIssue, IdempotencyKey: "blocker-capacity-reuse-slot",
	})
	if err != nil || created.ScopeRevision != 3 {
		t.Fatalf("reuse resolved capacity slot=%+v err=%v", created, err)
	}
	var openRecords, totalRecords int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='open'),count(*) FROM coordination_record WHERE coordination_scope_id=$1`, scope.Scope.ID).Scan(&openRecords, &totalRecords); err != nil || openRecords != 1000 || totalRecords != 1001 {
		t.Fatalf("capacity reuse open=%d total=%d err=%v", openRecords, totalRecords, err)
	}
}
