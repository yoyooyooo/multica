package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/contextguard"
	"github.com/multica-ai/multica/server/internal/service/inactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestResolveMaxInactivity_PassesContext verifies the inactivity
// helper threads the resolved value through correctly. The TaskService
// is constructed with no defaults, so the package-level constant wins.
func TestResolveMaxInactivity_PassesContext(t *testing.T) {
	svc := &TaskService{
		Queries:              nil, // never invoked — invalid workspaceID short-circuits
		InactivityDefaultSecs: 0,  // 0 falls through to the package default
	}
	got := svc.resolvedMaxInactivitySecs(context.Background(), 0, db.Agent{}, pgtype.UUID{})
	if got != inactivity.DefaultDefaultMaxInactivitySecs {
		t.Fatalf("expected package default %d, got %d", inactivity.DefaultDefaultMaxInactivitySecs, got)
	}
}

// TestResolveMaxInactivity_HonoursTaskOverride confirms that a positive
// taskOverride skips the agent/workspace/default chain entirely.
func TestResolveMaxInactivity_HonoursTaskOverride(t *testing.T) {
	svc := &TaskService{}
	got := svc.resolvedMaxInactivitySecs(context.Background(), 300, db.Agent{}, pgtype.UUID{})
	if got != 300 {
		t.Fatalf("task override 300 should win, got %d", got)
	}
}

// TestResolveMaxInactivity_DefaultFromService confirms the explicit
// service-level default beats the package constant.
func TestResolveMaxInactivity_DefaultFromService(t *testing.T) {
	svc := &TaskService{InactivityDefaultSecs: 1500}
	got := svc.resolvedMaxInactivitySecs(context.Background(), 0, db.Agent{}, pgtype.UUID{})
	if got != 1500 {
		t.Fatalf("service default 1500 should win over package constant, got %d", got)
	}
}

// TestInactivityDefaults_ReadsServiceFields exercises the laziness of
// inactivityDefaults so a server-wide bump via env / flag takes
// effect on the next enqueue without requiring a restart.
func TestInactivityDefaults_ReadsServiceFields(t *testing.T) {
	svc := &TaskService{InactivityDefaultSecs: 0}
	got := svc.inactivityDefaults()
	if got.DefaultMaxInactivitySecs != 0 {
		t.Fatalf("expected zero default, got %d", got.DefaultMaxInactivitySecs)
	}
	svc.InactivityDefaultSecs = 2000
	got = svc.inactivityDefaults()
	if got.DefaultMaxInactivitySecs != 2000 {
		t.Fatalf("expected updated default 2000, got %d", got.DefaultMaxInactivitySecs)
	}
}

// TestContextGuardService_DefaultsAreLive is the symmetry of the
// above for the guard's policy chain — an admin update flips the
// service field and the next enqueue consults the new value.
func TestContextGuardService_DefaultsAreLive(t *testing.T) {
	svc := &TaskService{ContextGuardDefaultPolicy: string(contextguard.PolicyWarn)}
	got := svc.contextGuardService()
	if got.Defaults.Policy != contextguard.PolicyWarn {
		t.Fatalf("expected warn policy, got %q", got.Defaults.Policy)
	}
}

// TestErrContextMissing_IsExported pins the MUL-4059 contract: the
// sentinel error returned by reject/block_and_notify policy actions
// must be visible to callers (handler layer maps it to 422).
func TestErrContextMissing_IsExported(t *testing.T) {
	if contextguard.ErrContextMissing == nil {
		t.Fatal("ErrContextMissing must be non-nil")
	}
	if !errors.Is(contextguard.ErrContextMissing, contextguard.ErrContextMissing) {
		t.Fatal("ErrContextMissing must compare equal to itself")
	}
}

// TestRetryableReasons_InactivityTimeoutIncluded pins the auto-retry
// contract: an inactivity timeout triggers a fresh attempt so the
// agent gets one more chance to make progress.
func TestRetryableReasons_InactivityTimeoutIncluded(t *testing.T) {
	if !retryableReasons["inactivity_timeout"] {
		t.Fatal("inactivity_timeout must be in the retryable reasons map")
	}
	if !retryableReasons["timeout"] {
		t.Fatal("timeout must remain in the retryable reasons map (pre-MUL-4059 invariant)")
	}
}

// TestTaskErrorType_InactivityTimeoutIsTimeout pins the analytics
// classification: inactivity_timeout groups under the "timeout" type
// so dashboards continue to show one timeout bucket.
func TestTaskErrorType_InactivityTimeoutIsTimeout(t *testing.T) {
	if got := taskErrorType("inactivity_timeout"); got != "timeout" {
		t.Fatalf("expected timeout classification, got %q", got)
	}
	if got := taskErrorType("timeout"); got != "timeout" {
		t.Fatalf("expected timeout classification for plain timeout, got %q", got)
	}
	if got := taskErrorType("inactivity_timeout"); got != taskErrorType("timeout") {
		t.Fatalf("inactivity_timeout must classify the same as timeout")
	}
	if got := taskErrorType("agent_error"); got != "agent_error" {
		t.Fatalf("unrelated reason must not be misclassified, got %q", got)
	}
}