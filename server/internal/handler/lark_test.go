package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/lark"
)

// stubConfiguredAPIClient is the test seam that lets us simulate a
// real Lark client being wired without dragging the transport in.
// The behaviorally interesting methods here are IsConfigured and
// SupportsOAuthInstall — flipped independently so tests can pin the
// half-wired transitional state where outbound is functional but
// OAuth exchange is not yet implemented. Every other call returns
// ErrAPIClientNotConfigured (the test paths that hit it would
// already be a misconfiguration).
type stubConfiguredAPIClient struct {
	// supportsOAuthInstall lets tests cover both the "outbound only"
	// and "fully wired" capability states. Default zero-value is
	// false, matching the real httpAPIClient's current behavior
	// (ExchangeOAuthCode not yet implemented).
	supportsOAuthInstall bool
}

func (stubConfiguredAPIClient) IsConfigured() bool { return true }
func (s stubConfiguredAPIClient) SupportsOAuthInstall() bool {
	return s.supportsOAuthInstall
}
func (stubConfiguredAPIClient) SendInteractiveCard(_ context.Context, _ lark.SendCardParams) (string, error) {
	return "", lark.ErrAPIClientNotConfigured
}
func (stubConfiguredAPIClient) PatchInteractiveCard(_ context.Context, _ lark.PatchCardParams) error {
	return lark.ErrAPIClientNotConfigured
}
func (stubConfiguredAPIClient) SendBindingPromptCard(_ context.Context, _ lark.BindingPromptParams) error {
	return lark.ErrAPIClientNotConfigured
}
func (stubConfiguredAPIClient) ExchangeOAuthCode(_ context.Context, _ string, _ string) (lark.OAuthExchangeResult, error) {
	return lark.OAuthExchangeResult{}, lark.ErrAPIClientNotConfigured
}

// Lark-handler unit tests focus on the no-config short-circuits —
// verifying that a self-host deployment without MULTICA_LARK_SECRET_KEY
// does NOT serve create / revoke / redeem, and that list degrades
// gracefully to an empty response so the Integrations tab still
// renders. Happy-path flows (create + list + revoke; token mint +
// redeem) need a real DB and land alongside the WS hub integration
// tests in a follow-up commit.

func TestCreateLarkInstallation_NotConfigured(t *testing.T) {
	h := &Handler{} // LarkInstallations intentionally nil
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/x/lark/installations", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.CreateLarkInstallation(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when lark not configured, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRevokeLarkInstallation_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete, "/api/workspaces/x/lark/installations/y", nil)
	w := httptest.NewRecorder()
	h.RevokeLarkInstallation(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestRedeemLarkBindingToken_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/lark/binding/redeem", strings.NewReader(`{"token":"x"}`))
	w := httptest.NewRecorder()
	h.RedeemLarkBindingToken(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestStartLarkInstall_NotConfiguredReturnsServiceUnavailable(t *testing.T) {
	// When the at-rest key is unset the LarkInstallations service is
	// nil and the install-start handler must short-circuit to 503 with
	// a clear message — degrading to "configured: false" silently would
	// hide a real misconfiguration from the operator.
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/install/start?agent_id=y", nil)
	w := httptest.NewRecorder()
	h.StartLarkInstall(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when LarkInstallations is nil, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestLarkInstallCallback_NotConfiguredRedirects(t *testing.T) {
	// The callback always finishes with a redirect to the frontend
	// settings page (success or error) so we never have to render an
	// HTML error page server-side. With LarkInstallations / LarkOAuth
	// nil the redirect's query string carries lark_error=not_configured
	// so the frontend can show the right copy without polling.
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/lark/install/callback?code=abc&state=xyz", nil)
	w := httptest.NewRecorder()
	h.LarkInstallCallback(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" || !strings.Contains(loc, "lark_error=not_configured") {
		t.Fatalf("redirect missing lark_error=not_configured marker; loc=%q", loc)
	}
	if !strings.Contains(loc, "/settings?tab=lark") {
		t.Fatalf("redirect must land on lark settings tab; loc=%q", loc)
	}
}

func TestListLarkInstallations_NotConfiguredReturnsEmpty(t *testing.T) {
	// Listing is intentionally a "soft" endpoint: when lark is not
	// configured we return an empty list + configured:false rather
	// than a 503, so the Integrations tab renders normally with a
	// "not connected" empty state instead of an error banner.
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/installations", nil)
	w := httptest.NewRecorder()
	h.ListLarkInstallations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Installations    []any `json:"installations"`
		Configured       bool  `json:"configured"`
		InstallSupported bool  `json:"install_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Fatalf("configured should be false when LarkInstallations is nil")
	}
	if resp.InstallSupported {
		t.Fatalf("install_supported should be false when LarkInstallations is nil")
	}
	if len(resp.Installations) != 0 {
		t.Fatalf("expected empty installations list, got %d", len(resp.Installations))
	}
}

// TestStartLarkInstall_StubClientReportsNotConfigured pins the
// front-half of the "no broken install flow" guarantee: even when
// the at-rest key + OAuth env are set, the install-start endpoint
// reports configured:false if the underlying APIClient is the stub.
// Without this short-circuit the user would scan, authorize, and
// get bounced back with `lark_error=internal_error` because the
// OAuth exchange would surface ErrAPIClientNotConfigured.
//
// The matching front-end half is in lark-tab.tsx: the agent-detail
// "Bind to Lark" button hides itself when install_supported==false.
func TestStartLarkInstall_StubClientReportsNotConfigured(t *testing.T) {
	stubLogger := slog.New(slog.NewTextHandler(httptest.NewRecorder(), nil))
	// The stub returns IsConfigured()==false; the handler must
	// short-circuit BEFORE invoking OAuth.StartInstall, so we
	// can leave LarkOAuth nil here — if the handler tries to use
	// it, we will see a panic instead of the expected JSON body.
	h := &Handler{
		LarkAPIClient: lark.NewStubAPIClient(stubLogger),
	}
	// LarkInstallations must be set to pass the 503 short-circuit at
	// the top of the handler; assign a non-nil sentinel.
	// Use the fact that we only need it != nil: a zero-value pointer
	// crashes when its methods are called, but the IsConfigured()
	// gate fires first so they never are.
	h.LarkInstallations = &lark.InstallationService{}

	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/install/start?agent_id=y", nil)
	w := httptest.NewRecorder()
	h.StartLarkInstall(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp StartLarkInstallResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Fatalf("configured should be false when APIClient is the stub; got %+v", resp)
	}
	if resp.URL != "" {
		t.Fatalf("URL should be empty when not configured; got %q", resp.URL)
	}
}

// TestListLarkInstallations_StubClientReportsInstallNotSupported pins
// the listing side of the same guarantee.
func TestListLarkInstallations_StubClientReportsInstallNotSupported(t *testing.T) {
	stubLogger := slog.New(slog.NewTextHandler(httptest.NewRecorder(), nil))
	// LarkInstallations is nil to keep this a pure no-config test —
	// when it's nil the handler returns the not-configured shape and
	// install_supported must be false alongside configured.
	h := &Handler{
		LarkAPIClient: lark.NewStubAPIClient(stubLogger),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/installations", nil)
	w := httptest.NewRecorder()
	h.ListLarkInstallations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Configured       bool `json:"configured"`
		InstallSupported bool `json:"install_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.InstallSupported {
		t.Fatalf("install_supported must be false while only stub APIClient is wired")
	}
}

// TestStartLarkInstall_TransportOnlyClientReportsNotConfigured pins
// the must-fix from the Elon review: outbound HTTP transport being
// wired (IsConfigured()==true) is NOT enough to flip the install
// flow open. As long as the wired APIClient still reports
// SupportsOAuthInstall()==false (ExchangeOAuthCode not yet
// implemented), StartLarkInstall MUST short-circuit to
// configured:false. Otherwise the user would scan the QR, authorize,
// and get bounced back with a generic internal_error after the
// callback's exchange step fails.
func TestStartLarkInstall_TransportOnlyClientReportsNotConfigured(t *testing.T) {
	h := &Handler{
		// Transport wired (IsConfigured=true) but OAuth not implemented
		// yet (SupportsOAuthInstall=false) — exactly the half-built
		// state the real httpAPIClient is in until ExchangeOAuthCode
		// lands.
		LarkAPIClient: stubConfiguredAPIClient{supportsOAuthInstall: false},
	}
	h.LarkInstallations = &lark.InstallationService{}

	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/install/start?agent_id=y", nil)
	w := httptest.NewRecorder()
	h.StartLarkInstall(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp StartLarkInstallResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Fatalf("configured must be false while SupportsOAuthInstall()==false; got %+v", resp)
	}
	if resp.URL != "" {
		t.Fatalf("URL must be empty when not configured; got %q", resp.URL)
	}
}

// TestListLarkInstallations_NotConfigured_HardCodedInstallSupportedFalse
// pins the invariant for the early-return branch: when
// LarkInstallations is nil (the deployment has no at-rest encryption
// key wired), the response MUST return both configured:false AND
// install_supported:false regardless of what APIClient is in place.
// A transport-only APIClient (IsConfigured=true,
// SupportsOAuthInstall=false) on a not-configured deployment must not
// accidentally flip install_supported to true via the APIClient path
// — that path is not consulted in the early-return branch.
//
// The non-nil branch's consultation of SupportsOAuthInstall is
// exercised by the route-level handler tests that wire a real
// InstallationService against the test DB.
func TestListLarkInstallations_NotConfigured_HardCodedInstallSupportedFalse(t *testing.T) {
	h := &Handler{
		LarkInstallations: nil, // triggers the not-configured early return.
		LarkAPIClient:     stubConfiguredAPIClient{supportsOAuthInstall: false},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/installations", nil)
	w := httptest.NewRecorder()
	h.ListLarkInstallations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Configured       bool `json:"configured"`
		InstallSupported bool `json:"install_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Fatalf("configured must be false when LarkInstallations is nil")
	}
	if resp.InstallSupported {
		t.Fatalf("install_supported must be false in the early-return branch even with a transport-only APIClient")
	}
}

// TestLarkOAuthErrorReason_APIClientNotConfigured pins the explicit
// mapping from the must-fix: if the OAuth callback ever does reach
// ExchangeOAuthCode (e.g. someone replayed a state URL while the
// SupportsOAuthInstall gate was open earlier) and the call surfaces
// ErrAPIClientNotConfigured, the redirect query string must carry a
// distinct, user-meaningful reason — NOT the generic internal_error
// that masks a known-unimplemented stage as a transient outage.
func TestLarkOAuthErrorReason_APIClientNotConfigured(t *testing.T) {
	// Bare sentinel.
	if got := larkOAuthErrorReason(lark.ErrAPIClientNotConfigured); got != "oauth_exchange_unimplemented" {
		t.Errorf("bare sentinel: got %q want oauth_exchange_unimplemented", got)
	}
	// Wrapped via fmt.Errorf("...: %w", ...) — this is exactly the
	// shape HandleCallback wraps in (`fmt.Errorf("exchange oauth code:
	// %w", err)`). The mapping uses errors.Is so wrapping must still
	// land on the right reason; otherwise a regression here silently
	// downgrades the error to internal_error.
	wrapped := fmt.Errorf("exchange oauth code: %w", lark.ErrAPIClientNotConfigured)
	if got := larkOAuthErrorReason(wrapped); got != "oauth_exchange_unimplemented" {
		t.Errorf("wrapped sentinel: got %q want oauth_exchange_unimplemented", got)
	}
}
