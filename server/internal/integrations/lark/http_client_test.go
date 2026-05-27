package lark

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// larkFakeServer is a tiny in-memory stand-in for the Lark Open
// Platform. Tests register handlers per path; the server panics if a
// path is hit without a registration (a missed assertion is louder
// than a 404).
//
// The handler shape mirrors http.HandlerFunc so each test can encode
// its own response without inheriting boilerplate.
type larkFakeServer struct {
	t       *testing.T
	mux     *http.ServeMux
	srv     *httptest.Server
	tokenN  atomic.Int32
	sendN   atomic.Int32
	patchN  atomic.Int32
	bindN   atomic.Int32
	authObs atomic.Value // last Authorization header seen across all paths
}

func newLarkFake(t *testing.T) *larkFakeServer {
	t.Helper()
	f := &larkFakeServer{t: t, mux: http.NewServeMux()}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *larkFakeServer) URL() string { return f.srv.URL }

func (f *larkFakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a := r.Header.Get("Authorization"); a != "" {
		f.authObs.Store(a)
	}
	f.mux.ServeHTTP(w, r)
}

func (f *larkFakeServer) lastAuth() string {
	v, _ := f.authObs.Load().(string)
	return v
}

// stubToken installs a token endpoint that returns the supplied token
// with the supplied expire (seconds) and counts hits.
func (f *larkFakeServer) stubToken(token string, expireSec int64) {
	f.mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		f.tokenN.Add(1)
		if r.Method != http.MethodPost {
			f.t.Errorf("token: want POST, got %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("token: decode body: %v", err)
		}
		if body["app_id"] == "" || body["app_secret"] == "" {
			f.t.Errorf("token: missing app credentials: %v", body)
		}
		writeJSON(w, map[string]any{
			"code":                0,
			"msg":                 "ok",
			"tenant_access_token": token,
			"expire":              expireSec,
		})
	})
}

// stubTokenError installs a token endpoint returning a Lark-style
// error code (non-zero `code` with HTTP 200).
func (f *larkFakeServer) stubTokenError(code int, msg string) {
	f.mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		f.tokenN.Add(1)
		writeJSON(w, map[string]any{"code": code, "msg": msg})
	})
}

// stubSend installs the IM-send endpoint. resp is the response body
// (typically the standard {code, msg, data:{message_id}} shape).
func (f *larkFakeServer) stubSend(resp map[string]any, verify func(r *http.Request, body map[string]string)) {
	f.mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		f.sendN.Add(1)
		if r.Method != http.MethodPost {
			f.t.Errorf("send: want POST, got %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("send: decode body: %v", err)
		}
		if verify != nil {
			verify(r, body)
		}
		writeJSON(w, resp)
	})
}

// stubPatch installs the IM-patch endpoint. The Lark route is
// /open-apis/im/v1/messages/<id>; ServeMux uses prefix matching when
// we register the parent path explicitly. We register the parent
// SEND path above already, so the patch path needs the full prefix.
func (f *larkFakeServer) stubPatch(resp map[string]any, verify func(r *http.Request, id string, body map[string]string)) {
	const prefix = "/open-apis/im/v1/messages/"
	f.mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			f.t.Errorf("patch: want PATCH, got %s", r.Method)
		}
		id := strings.TrimPrefix(r.URL.Path, prefix)
		if id == "" {
			f.t.Errorf("patch: missing message id")
		}
		f.patchN.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("patch: decode body: %v", err)
		}
		if verify != nil {
			verify(r, id, body)
		}
		writeJSON(w, resp)
	})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// newTestClient returns an httpAPIClient pointed at the fake server,
// using the supplied clock so token expiry can be controlled
// deterministically.
func newTestClient(fake *larkFakeServer, now func() time.Time) *httpAPIClient {
	c := NewHTTPAPIClient(HTTPClientConfig{
		BaseURL: fake.URL(),
		Now:     now,
	}).(*httpAPIClient)
	return c
}

func testCreds() InstallationCredentials {
	return InstallationCredentials{AppID: "cli_app_xx", AppSecret: "secret_xx"}
}

func TestHTTPClient_IsConfigured(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{})
	if !c.IsConfigured() {
		t.Fatalf("real client must report IsConfigured()=true")
	}
}

// TestHTTPClient_SupportsOAuthInstall_FalseWithoutOAuthCreds pins the
// must-fix from Elon's review: HTTP outbound transport being wired
// does NOT imply the OAuth install flow is ready. The exchange step
// requires the parent Lark app credentials (OAuthAppID / Secret) and
// returns ErrAPIClientNotConfigured without them; the capability gate
// must mirror that so handlers do not surface a bind UI that would
// crash at the exchange step.
func TestHTTPClient_SupportsOAuthInstall_FalseWithoutOAuthCreds(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{})
	if c.SupportsOAuthInstall() {
		t.Fatalf("SupportsOAuthInstall must be false without OAuth credentials")
	}
	// Both gates must flip in lockstep — if a future change makes one
	// say "ready" while the other says "not configured", users see an
	// install UI that crashes at the exchange. Lock the invariant here.
	_, err := c.ExchangeOAuthCode(context.Background(), "x", "https://x")
	if !errors.Is(err, ErrAPIClientNotConfigured) {
		t.Fatalf("ExchangeOAuthCode must surface ErrAPIClientNotConfigured without OAuth creds; got %v", err)
	}
}

// TestHTTPClient_SupportsOAuthInstall_TrueWithOAuthCreds is the
// inverse: once a deployment has supplied OAuthAppID + OAuthAppSecret,
// SupportsOAuthInstall reveals the install entry. The exchange path is
// real (covered by the happy-path test below); this test only pins the
// capability gate.
func TestHTTPClient_SupportsOAuthInstall_TrueWithOAuthCreds(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{OAuthAppID: "cli_app_parent", OAuthAppSecret: "secret_parent"})
	if !c.SupportsOAuthInstall() {
		t.Fatalf("SupportsOAuthInstall must be true when OAuth creds are configured")
	}
}

// TestHTTPClient_StubReportsBothCapabilitiesFalse pins the stub side
// of the capability split: the stub has no transport and no OAuth
// support, so both must be false.
func TestHTTPClient_StubReportsBothCapabilitiesFalse(t *testing.T) {
	s := NewStubAPIClient(nil)
	if s.IsConfigured() {
		t.Errorf("stub IsConfigured must be false")
	}
	if s.SupportsOAuthInstall() {
		t.Errorf("stub SupportsOAuthInstall must be false")
	}
}

// TestHTTPClient_SendInteractiveCard_DefaultRendererBodyHasUpdateMulti
// is the send-side half of the must-fix wire check: when the Patcher
// uses NewDefaultRenderer to produce a card and ships it via
// SendInteractiveCard, the actual HTTP body Lark receives must carry
// config.update_multi=true so the card is patchable downstream.
// Without this, the first send succeeds but every subsequent patch
// silently no-ops on Lark's side while local DB status still flips.
func TestHTTPClient_SendInteractiveCard_DefaultRendererBodyHasUpdateMulti(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_um_send", 7200)
	var capturedContent string
	fake.stubSend(
		map[string]any{"code": 0, "data": map[string]string{"message_id": "om_send_um"}},
		func(_ *http.Request, body map[string]string) {
			capturedContent = body["content"]
		},
	)

	r := NewDefaultRenderer()
	render, err := r.Render(RenderInput{Kind: CardKindThinking, AgentName: "TestAgent"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	c := newTestClient(fake, time.Now)
	if _, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc_send_um"),
		CardJSON:       render.JSON,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	assertCardContentHasUpdateMulti(t, capturedContent)
}

// TestHTTPClient_PatchInteractiveCard_DefaultRendererBodyHasUpdateMulti
// is the patch-side half of the same wire check. Every PatchCardParams
// the Patcher produces goes through the default renderer; the body
// shipped over PATCH /open-apis/im/v1/messages/:id must still carry
// update_multi=true, otherwise Lark refuses to apply the patch to a
// card that was sent with update_multi=true (the two ends must agree).
func TestHTTPClient_PatchInteractiveCard_DefaultRendererBodyHasUpdateMulti(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_um_patch", 7200)
	var capturedContent string
	fake.stubPatch(
		map[string]any{"code": 0, "msg": "ok"},
		func(_ *http.Request, _ string, body map[string]string) {
			capturedContent = body["content"]
		},
	)

	r := NewDefaultRenderer()
	render, err := r.Render(RenderInput{Kind: CardKindRunning, AgentName: "TestAgent"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	c := newTestClient(fake, time.Now)
	if err := c.PatchInteractiveCard(context.Background(), PatchCardParams{
		InstallationID:    testCreds(),
		LarkCardMessageID: "om_patch_um",
		CardJSON:          render.JSON,
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	assertCardContentHasUpdateMulti(t, capturedContent)
}

func assertCardContentHasUpdateMulti(t *testing.T, content string) {
	t.Helper()
	if content == "" {
		t.Fatalf("captured content empty — fake server did not receive the request body")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("card content is not valid JSON: %v (raw=%s)", err, content)
	}
	cfg, ok := doc["config"].(map[string]any)
	if !ok {
		t.Fatalf("card content missing config block (raw=%s)", content)
	}
	if v, _ := cfg["update_multi"].(bool); !v {
		t.Fatalf("config.update_multi must be true so the card is patchable on Lark's side; got config=%v (raw=%s)", cfg, content)
	}
}

func TestHTTPClient_SendInteractiveCard_HappyPath(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_1", 7200)
	fake.stubSend(
		map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]string{"message_id": "om_msg_42"},
		},
		func(r *http.Request, body map[string]string) {
			if got := r.URL.Query().Get("receive_id_type"); got != "chat_id" {
				t.Errorf("receive_id_type: got %q want chat_id", got)
			}
			if body["receive_id"] != "oc_chat_1" {
				t.Errorf("receive_id: got %q", body["receive_id"])
			}
			if body["msg_type"] != "interactive" {
				t.Errorf("msg_type: got %q want interactive", body["msg_type"])
			}
			if !strings.Contains(body["content"], "\"tag\"") {
				t.Errorf("content not a card body: %q", body["content"])
			}
		},
	)

	c := newTestClient(fake, time.Now)
	msgID, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc_chat_1"),
		CardJSON:       `{"tag":"div","text":"hi"}`,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msgID != "om_msg_42" {
		t.Errorf("message id: got %q want om_msg_42", msgID)
	}
	if got := fake.lastAuth(); got != "Bearer tok_1" {
		t.Errorf("Authorization header: got %q want Bearer tok_1", got)
	}
}

func TestHTTPClient_SendInteractiveCard_TokenCached(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_cached", 7200)
	fake.stubSend(
		map[string]any{
			"code": 0,
			"data": map[string]string{"message_id": "om_msg_x"},
		},
		nil,
	)
	c := newTestClient(fake, time.Now)
	for i := 0; i < 3; i++ {
		if _, err := c.SendInteractiveCard(context.Background(), SendCardParams{
			InstallationID: testCreds(),
			ChatID:         ChatID("oc_chat_1"),
			CardJSON:       `{}`,
		}); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	if got := fake.tokenN.Load(); got != 1 {
		t.Errorf("token endpoint hits: got %d want 1 (cached after first call)", got)
	}
	if got := fake.sendN.Load(); got != 3 {
		t.Errorf("send endpoint hits: got %d want 3", got)
	}
}

func TestHTTPClient_TokenRefreshAfterExpiry(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_refresh", 120) // 120s expire → 60s usable after safety margin
	fake.stubSend(
		map[string]any{
			"code": 0,
			"data": map[string]string{"message_id": "om"},
		},
		nil,
	)

	now := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{now: now}
	c := NewHTTPAPIClient(HTTPClientConfig{BaseURL: fake.URL(), Now: clock.Now}).(*httpAPIClient)

	// First call — fetches token.
	if _, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if fake.tokenN.Load() != 1 {
		t.Fatalf("first call should have fetched a token, got tokenN=%d", fake.tokenN.Load())
	}

	// Advance past the cached token's expiry (token expire 120s,
	// safety margin 60s → cache valid for 60s of wall-clock).
	clock.Advance(90 * time.Second)

	if _, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	}); err != nil {
		t.Fatalf("post-expiry send: %v", err)
	}
	if got := fake.tokenN.Load(); got != 2 {
		t.Errorf("token endpoint hits after expiry: got %d want 2", got)
	}
}

func TestHTTPClient_SendInteractiveCard_LarkErrorCode(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_e", 7200)
	fake.stubSend(map[string]any{"code": 230001, "msg": "no permission"}, nil)
	c := newTestClient(fake, time.Now)
	_, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	})
	if err == nil {
		t.Fatal("want error on non-zero code")
	}
	if !strings.Contains(err.Error(), "code=230001") {
		t.Errorf("error should surface code: %v", err)
	}
}

func TestHTTPClient_SendInteractiveCard_TokenExpired_InvalidatesCache(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_first", 7200)
	// First send replies with expired-token. Second send (after the
	// client should have dropped its cache) reaches the token
	// endpoint again. We swap the send handler mid-test to model
	// this without race conditions: send fails first, second call
	// from the same fake gets the token-endpoint hit + a fresh send
	// reply. To keep the test small we simply assert tokenN
	// increments after the failing call when the caller retries.
	var sendCalls atomic.Int32
	fake.mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		fake.sendN.Add(1)
		n := sendCalls.Add(1)
		if n == 1 {
			writeJSON(w, map[string]any{"code": codeTokenExpired, "msg": "expired"})
			return
		}
		writeJSON(w, map[string]any{"code": 0, "data": map[string]string{"message_id": "om_ok"}})
	})

	c := newTestClient(fake, time.Now)
	_, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	})
	if err == nil {
		t.Fatal("first send must fail with token-expired")
	}
	if !strings.Contains(err.Error(), "code=99991663") {
		t.Errorf("error should mention token-expired code: %v", err)
	}

	// Caller's retry — should re-fetch the token, then succeed.
	msgID, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	})
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if msgID != "om_ok" {
		t.Errorf("retry message id: got %q", msgID)
	}
	if got := fake.tokenN.Load(); got != 2 {
		t.Errorf("token endpoint hits after invalidation: got %d want 2", got)
	}
}

func TestHTTPClient_PatchInteractiveCard_HappyPath(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_p", 7200)
	fake.stubPatch(
		map[string]any{"code": 0, "msg": "ok"},
		func(r *http.Request, id string, body map[string]string) {
			if id != "om_msg_42" {
				t.Errorf("patch id: got %q want om_msg_42", id)
			}
			if !strings.Contains(body["content"], "updated") {
				t.Errorf("patch content: %q", body["content"])
			}
		},
	)
	c := newTestClient(fake, time.Now)
	if err := c.PatchInteractiveCard(context.Background(), PatchCardParams{
		InstallationID:    testCreds(),
		LarkCardMessageID: "om_msg_42",
		CardJSON:          `{"text":"updated"}`,
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}
}

func TestHTTPClient_PatchInteractiveCard_LarkErrorCode(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_p", 7200)
	fake.stubPatch(map[string]any{"code": 230002, "msg": "card not found"}, nil)
	c := newTestClient(fake, time.Now)
	err := c.PatchInteractiveCard(context.Background(), PatchCardParams{
		InstallationID:    testCreds(),
		LarkCardMessageID: "om_msg_x",
		CardJSON:          `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "code=230002") {
		t.Errorf("want code=230002 in error, got %v", err)
	}
}

func TestHTTPClient_SendBindingPromptCard_HappyPath(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_b", 7200)

	var capturedBody map[string]string
	fake.mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		fake.bindN.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		if got := r.URL.Query().Get("receive_id_type"); got != "open_id" {
			t.Errorf("receive_id_type: got %q want open_id", got)
		}
		writeJSON(w, map[string]any{"code": 0, "data": map[string]string{"message_id": "om_bind"}})
	})

	c := newTestClient(fake, time.Now)
	if err := c.SendBindingPromptCard(context.Background(), BindingPromptParams{
		InstallationID: testCreds(),
		OpenID:         OpenID("ou_user_1"),
		BindURL:        "https://multica.test/lark/bind?token=abc",
	}); err != nil {
		t.Fatalf("bind prompt: %v", err)
	}
	if capturedBody["receive_id"] != "ou_user_1" {
		t.Errorf("receive_id: got %q", capturedBody["receive_id"])
	}
	if !strings.Contains(capturedBody["content"], "multica.test/lark/bind") {
		t.Errorf("binding card should embed BindURL: %q", capturedBody["content"])
	}
	if !strings.Contains(capturedBody["content"], "去绑定") {
		t.Errorf("binding card should carry the localized CTA: %q", capturedBody["content"])
	}
}

func TestHTTPClient_TokenEndpointError(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubTokenError(10003, "invalid app_id or app_secret")
	c := newTestClient(fake, time.Now)
	_, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "code=10003") {
		t.Errorf("want code=10003 surfaced, got %v", err)
	}
}

func TestHTTPClient_MissingAppCredentials(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{}).(*httpAPIClient)
	_, err := c.tenantAccessToken(context.Background(), InstallationCredentials{AppSecret: "x"})
	if err == nil || !strings.Contains(err.Error(), "app_id") {
		t.Errorf("want missing app_id error, got %v", err)
	}
	_, err = c.tenantAccessToken(context.Background(), InstallationCredentials{AppID: "x"})
	if err == nil || !strings.Contains(err.Error(), "app_secret") {
		t.Errorf("want missing app_secret error, got %v", err)
	}
}

func TestHTTPClient_MissingChatID_PreAuth(t *testing.T) {
	// chat_id validation must short-circuit BEFORE any auth round-trip
	// — otherwise a misuse leaks load to the token endpoint.
	fake := newLarkFake(t)
	c := newTestClient(fake, time.Now)
	_, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		CardJSON:       `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "chat_id") {
		t.Errorf("want missing chat_id error, got %v", err)
	}
	if got := fake.tokenN.Load(); got != 0 {
		t.Errorf("token endpoint must not be hit on bad input: got %d", got)
	}
}

func TestHTTPClient_MissingCardJSON(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{}).(*httpAPIClient)
	if _, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
	}); err == nil || !strings.Contains(err.Error(), "card json") {
		t.Errorf("send: want missing card json, got %v", err)
	}
	if err := c.PatchInteractiveCard(context.Background(), PatchCardParams{
		InstallationID:    testCreds(),
		LarkCardMessageID: "om",
	}); err == nil || !strings.Contains(err.Error(), "card json") {
		t.Errorf("patch: want missing card json, got %v", err)
	}
}

func TestHTTPClient_PatchMissingID(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{}).(*httpAPIClient)
	err := c.PatchInteractiveCard(context.Background(), PatchCardParams{
		InstallationID: testCreds(),
		CardJSON:       `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "card message id") {
		t.Errorf("want missing message id error, got %v", err)
	}
}

func TestHTTPClient_BindingPromptValidation(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{}).(*httpAPIClient)
	if err := c.SendBindingPromptCard(context.Background(), BindingPromptParams{
		InstallationID: testCreds(),
		BindURL:        "https://x",
	}); err == nil || !strings.Contains(err.Error(), "open_id") {
		t.Errorf("want missing open_id, got %v", err)
	}
	if err := c.SendBindingPromptCard(context.Background(), BindingPromptParams{
		InstallationID: testCreds(),
		OpenID:         "ou",
	}); err == nil || !strings.Contains(err.Error(), "bind url") {
		t.Errorf("want missing bind url, got %v", err)
	}
}

// TestHTTPClient_ExchangeOAuthCode_NotConfigured pins the gate: without
// OAuth credentials, exchange must surface ErrAPIClientNotConfigured
// (NOT a generic error) so the callback handler can map it to the
// dedicated `oauth_exchange_unimplemented` reason instead of treating
// it as a transient outage.
func TestHTTPClient_ExchangeOAuthCode_NotConfigured(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{})
	_, err := c.ExchangeOAuthCode(context.Background(), "code_x", "https://x")
	if !errors.Is(err, ErrAPIClientNotConfigured) {
		t.Errorf("OAuth exchange without parent app creds must surface ErrAPIClientNotConfigured: %v", err)
	}
}

// TestHTTPClient_ExchangeOAuthCode_HappyPath verifies the v2 OAuth
// exchange + bot-info handshake produces an OAuthExchangeResult whose
// fields are non-empty and ready for InstallationParams: app_id /
// app_secret are the parent app credentials, BotOpenID is the parent
// bot's open_id from /bot/v3/info, InstallerOpenID is the open_id
// from the authorization-code exchange. All four are required by
// validateExchangeResult; an empty value here is a hard install
// failure.
func TestHTTPClient_ExchangeOAuthCode_HappyPath(t *testing.T) {
	fake := newLarkFake(t)
	// Token endpoint — used for the bot-info call.
	fake.stubToken("tok_oauth_install", 7200)

	fake.mux.HandleFunc("/open-apis/authen/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("oauth exchange: want POST, got %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("oauth exchange: decode body: %v", err)
		}
		if body["grant_type"] != "authorization_code" {
			t.Errorf("grant_type: got %q", body["grant_type"])
		}
		if body["client_id"] != "cli_app_parent" {
			t.Errorf("client_id: got %q", body["client_id"])
		}
		if body["client_secret"] != "secret_parent" {
			t.Errorf("client_secret: got %q", body["client_secret"])
		}
		if body["code"] != "auth_code_xyz" {
			t.Errorf("code: got %q", body["code"])
		}
		if body["redirect_uri"] != "https://multica.test/api/lark/install/callback" {
			t.Errorf("redirect_uri: got %q", body["redirect_uri"])
		}
		writeJSON(w, map[string]any{
			"access_token": "u-abc",
			"token_type":   "Bearer",
			"expires_in":   7200,
			"open_id":      "ou_installer",
			"union_id":     "on_installer_union",
		})
	})

	fake.mux.HandleFunc("/open-apis/bot/v3/info", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("bot info: want GET, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok_oauth_install" {
			t.Errorf("bot info: Authorization=%q want Bearer tok_oauth_install", got)
		}
		writeJSON(w, map[string]any{
			"code": 0,
			"msg":  "ok",
			"bot": map[string]any{
				"open_id":         "ou_bot_parent",
				"app_name":        "Multica",
				"activate_status": 0,
			},
		})
	})

	c := NewHTTPAPIClient(HTTPClientConfig{
		BaseURL:        fake.URL(),
		OAuthAppID:     "cli_app_parent",
		OAuthAppSecret: "secret_parent",
	})

	res, err := c.ExchangeOAuthCode(context.Background(), "auth_code_xyz", "https://multica.test/api/lark/install/callback")
	if err != nil {
		t.Fatalf("ExchangeOAuthCode: %v", err)
	}
	if res.AppID != "cli_app_parent" {
		t.Errorf("AppID: got %q want cli_app_parent", res.AppID)
	}
	if res.AppSecret != "secret_parent" {
		t.Errorf("AppSecret: got %q want secret_parent", res.AppSecret)
	}
	if res.BotOpenID != "ou_bot_parent" {
		t.Errorf("BotOpenID: got %q want ou_bot_parent", res.BotOpenID)
	}
	if string(res.InstallerOpenID) != "ou_installer" {
		t.Errorf("InstallerOpenID: got %q want ou_installer", res.InstallerOpenID)
	}
	if res.InstallerUnionID != "on_installer_union" {
		t.Errorf("InstallerUnionID: got %q want on_installer_union", res.InstallerUnionID)
	}
}

// TestHTTPClient_ExchangeOAuthCode_OAuthError surfaces a Lark v2 OAuth
// error response as a non-nil error from ExchangeOAuthCode so the
// callback handler can render the right copy. The v2 endpoint follows
// RFC 6749: errors come back as 200 + {error, error_description}.
func TestHTTPClient_ExchangeOAuthCode_OAuthError(t *testing.T) {
	fake := newLarkFake(t)
	fake.mux.HandleFunc("/open-apis/authen/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"error":             "invalid_grant",
			"error_description": "code already used",
		})
	})
	c := NewHTTPAPIClient(HTTPClientConfig{
		BaseURL:        fake.URL(),
		OAuthAppID:     "cli_app_parent",
		OAuthAppSecret: "secret_parent",
	})
	_, err := c.ExchangeOAuthCode(context.Background(), "code_x", "https://x")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("want invalid_grant surfaced, got %v", err)
	}
}

// TestHTTPClient_ExchangeOAuthCode_BotInfoError surfaces a bot-info
// failure as a non-nil error and does NOT leak the partial open_id
// from step 1 into a half-filled OAuthExchangeResult.
func TestHTTPClient_ExchangeOAuthCode_BotInfoError(t *testing.T) {
	fake := newLarkFake(t)
	fake.stubToken("tok_e2", 7200)
	fake.mux.HandleFunc("/open-apis/authen/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"access_token": "u-x",
			"open_id":      "ou_x",
			"expires_in":   7200,
		})
	})
	fake.mux.HandleFunc("/open-apis/bot/v3/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": 99991663, "msg": "expired token"})
	})
	c := NewHTTPAPIClient(HTTPClientConfig{
		BaseURL:        fake.URL(),
		OAuthAppID:     "cli_app_parent",
		OAuthAppSecret: "secret_parent",
	})
	res, err := c.ExchangeOAuthCode(context.Background(), "code_x", "https://x")
	if err == nil || !strings.Contains(err.Error(), "99991663") {
		t.Errorf("want code=99991663 surfaced, got err=%v", err)
	}
	if res.AppID != "" || res.BotOpenID != "" || res.InstallerOpenID != "" {
		t.Errorf("error path must not leak partial result: %+v", res)
	}
}

// TestHTTPClient_ExchangeOAuthCode_PreflightValidation pins the
// pre-auth short-circuits so a misuse cannot waste a token round-trip.
func TestHTTPClient_ExchangeOAuthCode_PreflightValidation(t *testing.T) {
	c := NewHTTPAPIClient(HTTPClientConfig{
		OAuthAppID:     "cli_app_parent",
		OAuthAppSecret: "secret_parent",
	})
	if _, err := c.ExchangeOAuthCode(context.Background(), "", "https://x"); err == nil || !strings.Contains(err.Error(), "code") {
		t.Errorf("want missing code error, got %v", err)
	}
	if _, err := c.ExchangeOAuthCode(context.Background(), "x", ""); err == nil || !strings.Contains(err.Error(), "redirect_uri") {
		t.Errorf("want missing redirect_uri error, got %v", err)
	}
}

func TestHTTPClient_BadHTTPStatus(t *testing.T) {
	fake := newLarkFake(t)
	// Token returns success.
	fake.stubToken("tok", 7200)
	// Send replies with 500 + body — exercise the non-2xx branch.
	fake.mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		fake.sendN.Add(1)
		w.WriteHeader(500)
		_, _ = io.WriteString(w, "boom")
	})
	c := newTestClient(fake, time.Now)
	_, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Errorf("want http 500 surfaced, got %v", err)
	}
}

func TestHTTPClient_TokenExpire_ClampedToSafety(t *testing.T) {
	// Lark returns expire=10s — well under the safety margin. The
	// client must NOT cache a token that is already past its safe
	// window; instead it clamps to 2× safety margin so the cached
	// entry is at least usable for one safety margin of wall-clock.
	fake := newLarkFake(t)
	fake.stubToken("tok_short", 10)
	fake.stubSend(map[string]any{"code": 0, "data": map[string]string{"message_id": "om"}}, nil)

	now := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{now: now}
	c := NewHTTPAPIClient(HTTPClientConfig{BaseURL: fake.URL(), Now: clock.Now}).(*httpAPIClient)

	if _, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	clock.Advance(30 * time.Second) // still within clamped window
	if _, err := c.SendInteractiveCard(context.Background(), SendCardParams{
		InstallationID: testCreds(),
		ChatID:         ChatID("oc"),
		CardJSON:       `{}`,
	}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	if got := fake.tokenN.Load(); got != 1 {
		t.Errorf("token endpoint hits within clamped window: got %d want 1", got)
	}
}

func TestBindingPromptTemplate_Shape(t *testing.T) {
	raw, err := bindingPromptTemplate("https://multica.test/bind?token=abc")
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("template json: %v", err)
	}
	// Shape check — top-level keys exist and elements is non-empty.
	if _, ok := doc["config"]; !ok {
		t.Errorf("missing config")
	}
	if _, ok := doc["header"]; !ok {
		t.Errorf("missing header")
	}
	elements, ok := doc["elements"].([]any)
	if !ok || len(elements) < 2 {
		t.Fatalf("elements: want >=2, got %v", doc["elements"])
	}
	// Last element should be the action button carrying the URL.
	last, _ := elements[len(elements)-1].(map[string]any)
	if last["tag"] != "action" {
		t.Errorf("last element should be action: %v", last)
	}
	actions, _ := last["actions"].([]any)
	if len(actions) == 0 {
		t.Fatalf("no actions in card")
	}
	btn, _ := actions[0].(map[string]any)
	if btn["url"] != "https://multica.test/bind?token=abc" {
		t.Errorf("button url: got %v", btn["url"])
	}
}

// fakeClock is a minimal monotonic clock for tests that need to drive
// the cache TTL deterministically.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
