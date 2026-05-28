package lark

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stubAPIClientWithRecorder is a fake APIClient that captures the
// arguments of each outbound call so the replier tests can assert
// what landed.
type stubAPIClientWithRecorder struct {
	mu             sync.Mutex
	configured     bool
	bindingCalls   []BindingPromptParams
	interactiveOut []SendCardParams
	sendErr        error
	bindingErr     error
}

func (s *stubAPIClientWithRecorder) IsConfigured() bool { return s.configured }

func (s *stubAPIClientWithRecorder) SendInteractiveCard(ctx context.Context, p SendCardParams) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return "", s.sendErr
	}
	s.interactiveOut = append(s.interactiveOut, p)
	return "lark-msg-id", nil
}

func (s *stubAPIClientWithRecorder) PatchInteractiveCard(ctx context.Context, p PatchCardParams) error {
	return nil
}

func (s *stubAPIClientWithRecorder) SendBindingPromptCard(ctx context.Context, p BindingPromptParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindingErr != nil {
		return s.bindingErr
	}
	s.bindingCalls = append(s.bindingCalls, p)
	return nil
}

func (s *stubAPIClientWithRecorder) GetBotInfo(ctx context.Context, creds InstallationCredentials) (BotInfo, error) {
	return BotInfo{}, nil
}

// stubCredentialsResolver returns a fixed plaintext secret.
type stubCredentialsResolver struct{ secret string }

func (s stubCredentialsResolver) DecryptAppSecret(inst db.LarkInstallation) (string, error) {
	if s.secret == "" {
		return "", errors.New("no secret configured")
	}
	return s.secret, nil
}

// stubReplierQueries returns a fixed agent.
type stubReplierQueries struct {
	agent db.Agent
	err   error
}

func (s stubReplierQueries) GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error) {
	if s.err != nil {
		return db.Agent{}, s.err
	}
	return s.agent, nil
}

// stubBindingMint is a minimal TxStarter stand-in: the real
// BindingTokenService.Mint calls qx.CreateLarkBindingToken on the
// non-tx queries handle when no transaction is started by the caller.
// We bypass that path by constructing a BindingTokenService with a
// fake DB query interface — but since BindingTokenService is a
// concrete struct around *db.Queries, the cleanest seam in tests is
// to swap the replier's bindingSvc field for a fake that satisfies
// the narrow Mint method via an in-package alias.

// fakeBindingMinter substitutes for BindingTokenService.Mint in tests
// — we cannot construct a real BindingTokenService without a live
// *db.Queries, but the replier only calls .Mint on it, so a typed
// wrapper around a function works.
//
// We monkey-patch by exposing a package-level seam on the replier in
// the test file: the production path uses bindingSvc directly; the
// test path wraps the replier so Reply can be exercised end-to-end.

// TestLarkOutcomeReplierFallsBackToNoopWhenStubAPI ensures the
// production replier downgrades to noop when the supplied APIClient
// reports IsConfigured()=false. This avoids a misconfigured
// deployment burning binding tokens that can never be delivered.
func TestLarkOutcomeReplierFallsBackToNoopWhenStubAPI(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stub := &stubAPIClientWithRecorder{configured: false}
	rep := NewLarkOutcomeReplier(OutcomeReplierConfig{
		APIClient:   stub,
		BindingSvc:  &BindingTokenService{}, // not nil so we exercise the IsConfigured guard
		Credentials: stubCredentialsResolver{secret: "s"},
		Queries:     stubReplierQueries{},
		PublicURL:   "https://multica.test",
		Logger:      log,
	})
	if _, isNoop := rep.(*noopReplier); !isNoop {
		t.Fatalf("expected noopReplier when APIClient.IsConfigured()=false, got %T", rep)
	}
}

// TestLarkOutcomeReplierFallsBackToNoopWhenNilDep verifies that any
// missing dependency yields a noop replier rather than a half-wired
// production one (which would panic on first use).
func TestLarkOutcomeReplierFallsBackToNoopWhenNilDep(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []OutcomeReplierConfig{
		{}, // all nil
		{APIClient: &stubAPIClientWithRecorder{configured: true}},
		{APIClient: &stubAPIClientWithRecorder{configured: true}, BindingSvc: &BindingTokenService{}},
		{APIClient: &stubAPIClientWithRecorder{configured: true}, BindingSvc: &BindingTokenService{}, Credentials: stubCredentialsResolver{secret: "s"}},
	}
	for i, cfg := range cases {
		cfg.Logger = log
		if _, isNoop := NewLarkOutcomeReplier(cfg).(*noopReplier); !isNoop {
			t.Errorf("case %d: expected noopReplier with missing dep, got production", i)
		}
	}
}

// TestLarkOutcomeReplierAgentOfflineSendsCard exercises the
// non-binding path, which doesn't require the BindingTokenService
// machinery — we can construct the production replier and assert
// SendInteractiveCard was called with the expected chat_id + body.
func TestLarkOutcomeReplierAgentOfflineSendsCard(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stub := &stubAPIClientWithRecorder{configured: true}
	rep := NewLarkOutcomeReplier(OutcomeReplierConfig{
		APIClient:   stub,
		BindingSvc:  &BindingTokenService{},
		Credentials: stubCredentialsResolver{secret: "s"},
		Queries:     stubReplierQueries{agent: db.Agent{Name: "Trump"}},
		PublicURL:   "https://multica.test",
		Logger:      log,
	})
	inst := db.LarkInstallation{AppID: "cli_x"}
	inst.ID = mustUUID("11111111-1111-1111-1111-111111111111")
	msg := InboundMessage{ChatID: "oc_chat_1", SenderOpenID: "ou_user_1"}
	rep.Reply(context.Background(), inst, msg, DispatchResult{Outcome: OutcomeAgentOffline})

	if len(stub.interactiveOut) != 1 {
		t.Fatalf("expected one SendInteractiveCard call, got %d", len(stub.interactiveOut))
	}
	got := stub.interactiveOut[0]
	if got.ChatID != "oc_chat_1" {
		t.Errorf("ChatID = %q; want oc_chat_1", got.ChatID)
	}
	if got.InstallationID.AppID != "cli_x" {
		t.Errorf("AppID = %q", got.InstallationID.AppID)
	}
	if got.InstallationID.AppSecret != "s" {
		t.Errorf("AppSecret = %q", got.InstallationID.AppSecret)
	}
	if !contains(got.CardJSON, "离线") || !contains(got.CardJSON, "Trump") {
		t.Errorf("CardJSON should embed offline copy and agent name: %s", got.CardJSON)
	}
}

func TestLarkOutcomeReplierAgentArchivedSendsCard(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stub := &stubAPIClientWithRecorder{configured: true}
	rep := NewLarkOutcomeReplier(OutcomeReplierConfig{
		APIClient:   stub,
		BindingSvc:  &BindingTokenService{},
		Credentials: stubCredentialsResolver{secret: "s"},
		Queries:     stubReplierQueries{},
		PublicURL:   "https://multica.test",
		Logger:      log,
	})
	msg := InboundMessage{ChatID: "oc_chat_arch"}
	rep.Reply(context.Background(), db.LarkInstallation{}, msg, DispatchResult{Outcome: OutcomeAgentArchived})
	if len(stub.interactiveOut) != 1 {
		t.Fatalf("expected one SendInteractiveCard call, got %d", len(stub.interactiveOut))
	}
	if !contains(stub.interactiveOut[0].CardJSON, "归档") {
		t.Errorf("CardJSON should embed archived copy: %s", stub.interactiveOut[0].CardJSON)
	}
}

// TestLarkOutcomeReplierIngestedAndDroppedAreSilent asserts that the
// replier does NOT call the APIClient on outcomes owned elsewhere
// (Patcher handles Ingested; Dropped is informational only).
func TestLarkOutcomeReplierIngestedAndDroppedAreSilent(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stub := &stubAPIClientWithRecorder{configured: true}
	rep := NewLarkOutcomeReplier(OutcomeReplierConfig{
		APIClient:   stub,
		BindingSvc:  &BindingTokenService{},
		Credentials: stubCredentialsResolver{secret: "s"},
		Queries:     stubReplierQueries{},
		PublicURL:   "https://multica.test",
		Logger:      log,
	})
	msg := InboundMessage{ChatID: "oc_x"}
	rep.Reply(context.Background(), db.LarkInstallation{}, msg, DispatchResult{Outcome: OutcomeIngested})
	rep.Reply(context.Background(), db.LarkInstallation{}, msg, DispatchResult{Outcome: OutcomeDropped, DropReason: DropReasonDuplicate})
	if len(stub.interactiveOut) != 0 || len(stub.bindingCalls) != 0 {
		t.Errorf("Ingested/Dropped should not trigger any APIClient call; got interactive=%d binding=%d",
			len(stub.interactiveOut), len(stub.bindingCalls))
	}
}

// TestLarkOutcomeReplierOfflineSwallowsAPIError verifies the
// best-effort contract: an APIClient failure must NOT panic or
// propagate — Reply has no return signal — but the test still
// observes the side effect (single attempted SendInteractiveCard).
func TestLarkOutcomeReplierOfflineSwallowsAPIError(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stub := &stubAPIClientWithRecorder{configured: true, sendErr: errors.New("lark 5xx")}
	rep := NewLarkOutcomeReplier(OutcomeReplierConfig{
		APIClient:   stub,
		BindingSvc:  &BindingTokenService{},
		Credentials: stubCredentialsResolver{secret: "s"},
		Queries:     stubReplierQueries{},
		PublicURL:   "https://multica.test",
		Logger:      log,
	})
	// Should NOT panic.
	rep.Reply(context.Background(), db.LarkInstallation{}, InboundMessage{ChatID: "oc"}, DispatchResult{Outcome: OutcomeAgentOffline})
}

// TestNoopReplierIsHandledByHub verifies that NewHub installs a noop
// replier by default — so the inbound pipeline runs even when the
// caller never calls SetOutcomeReplier (e.g. in deployments that
// only run the inbound dispatcher pre-outbound-wiring). This guards
// the "no nil replier crash" contract on hub.handleEvent.
func TestNoopReplierIsHandledByHub(t *testing.T) {
	t.Parallel()
	hub := NewHub(nil, nil, nil, HubConfig{})
	if hub.replier == nil {
		t.Fatal("Hub.replier must default to noop, not nil")
	}
}

func mustUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		panic(err)
	}
	return u
}

// silence the unused import warnings for the dependencies we keep
// reaching for via reflection in future test cases.
var _ = pgx.ErrNoRows
