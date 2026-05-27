package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Real Lark/飞书 Open Platform HTTP APIClient.
//
// Scope: tenant_access_token acquisition + caching, IM v1 interactive-
// card send / patch, the dedicated binding-prompt outbound, AND the
// install-time OAuth code → installer-identity + parent-bot-identity
// exchange. The OAuth path requires the deployment to supply the
// parent Lark app credentials (OAuthAppID / OAuthAppSecret); when
// they are absent, ExchangeOAuthCode short-circuits to
// ErrAPIClientNotConfigured and SupportsOAuthInstall stays false so
// the UI never reveals a bind entry that would fail.
//
// Per-installation credentials flow in on each call via
// InstallationCredentials; the client never reads lark_installation
// directly. tenant_access_token is cached in-process keyed by app_id,
// honoring Lark's `expire` field minus a safety margin so callers
// never present a token that's about to lapse mid-flight. The OAuth
// path reuses this same cache when fetching /bot/v3/info, so a steady
// state install does not re-mint a token.

const (
	// defaultLarkBaseURL is the production 飞书 (mainland) open-platform
	// host. Operators on the Lark international tenant set
	// MULTICA_LARK_HTTP_BASE_URL to https://open.larksuite.com; tests
	// substitute an httptest.Server URL.
	defaultLarkBaseURL = "https://open.feishu.cn"

	// tokenSafetyMargin is subtracted from Lark's `expire` so we
	// refresh before a token actually lapses. 60s comfortably exceeds
	// any in-flight HTTP timeout we set below.
	tokenSafetyMargin = 60 * time.Second

	// defaultRequestTimeout is the per-call HTTP timeout. Lark's API
	// is normally well under 1s; we leave headroom for cross-region
	// latency from a self-hosted Multica deployment to feishu.cn.
	defaultRequestTimeout = 10 * time.Second

	// Lark's "invalid tenant_access_token" / "tenant_access_token
	// expired" error codes. When we see either, drop the cached token
	// so the next call refreshes from /tenant_access_token/internal.
	// 99991663 = expired, 99991664 = invalid. Documented at:
	// open.feishu.cn/document/server-docs/api-call-guide/server-error-codes.
	codeTokenExpired = 99991663
	codeTokenInvalid = 99991664
)

// HTTPClientConfig configures the production Lark HTTP APIClient.
type HTTPClientConfig struct {
	// BaseURL is the Lark open-platform root, e.g.
	// "https://open.feishu.cn" or "https://open.larksuite.com". Empty
	// defaults to defaultLarkBaseURL. Trailing "/" is stripped.
	BaseURL string

	// HTTPClient is the transport used for every outbound call. Tests
	// substitute an *http.Client whose Transport routes to an
	// httptest.Server. Empty defaults to a fresh http.Client with
	// defaultRequestTimeout.
	HTTPClient *http.Client

	// Now is overridable for deterministic token-expiry tests.
	Now func() time.Time

	// Logger receives warnings about Lark error codes and the
	// "OAuth not implemented" surface. Nil uses slog.Default().
	Logger *slog.Logger

	// OAuthAppID / OAuthAppSecret are the Multica-owned parent Lark
	// app credentials used as `client_id` / `client_secret` during the
	// authorization-code exchange AND as the per-installation
	// credentials persisted into lark_installation. PersonalAgent MVP
	// shares a single Lark app across every Multica installation: the
	// `tenant_access_token` is mint per (app, tenant_key) pair and
	// every bot speaks through Multica's parent app identity, so the
	// app_id/app_secret are the same on every lark_installation row.
	//
	// Empty disables ExchangeOAuthCode — it surfaces
	// ErrAPIClientNotConfigured and SupportsOAuthInstall returns
	// false, so the install UI stays hidden. Wiring (router.go) feeds
	// these from MULTICA_LARK_OAUTH_APP_ID / _APP_SECRET so the OAuth
	// config and the HTTP client read the same env vars.
	OAuthAppID     string
	OAuthAppSecret string
}

func (c HTTPClientConfig) withDefaults() HTTPClientConfig {
	if c.BaseURL == "" {
		c.BaseURL = defaultLarkBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// NewHTTPAPIClient constructs the real APIClient that speaks to Lark's
// open platform over HTTPS. Per-installation credentials flow in via
// each call's InstallationCredentials parameter; tokens are cached
// keyed by app_id so a single Multica server reuses Lark's
// tenant_access_token across calls to the same app.
func NewHTTPAPIClient(cfg HTTPClientConfig) APIClient {
	cfg = cfg.withDefaults()
	return &httpAPIClient{cfg: cfg, tokens: make(map[string]*cachedToken)}
}

type httpAPIClient struct {
	cfg HTTPClientConfig

	mu     sync.Mutex
	tokens map[string]*cachedToken
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

// IsConfigured reports true: once this client exists at all, the
// outbound transport path (send / patch / binding prompt) is wired.
// The stub returns false because every call there errors with
// ErrAPIClientNotConfigured; the real client is the inverse contract.
//
// IsConfigured deliberately does NOT speak to OAuth install
// readiness — see SupportsOAuthInstall below for that gate.
func (c *httpAPIClient) IsConfigured() bool { return true }

// SupportsOAuthInstall reports whether the scan-to-bind install
// flow can actually complete end-to-end. Two things must be true:
//
//  1. ExchangeOAuthCode is implemented (it is, below), AND
//  2. the deployment has supplied the parent Lark app credentials
//     (OAuthAppID + OAuthAppSecret) the exchange + bot-info calls
//     authenticate with.
//
// When (2) is missing, ExchangeOAuthCode short-circuits to
// ErrAPIClientNotConfigured and the handler-level
// `install_supported` flag stays false so the UI never surfaces a
// bind entry the user cannot actually walk through. Handlers must
// consult THIS gate, NOT IsConfigured — outbound transport being
// wired says nothing about whether OAuth credentials are configured.
func (c *httpAPIClient) SupportsOAuthInstall() bool {
	return c.cfg.OAuthAppID != "" && c.cfg.OAuthAppSecret != ""
}

// tenantAccessToken returns a usable tenant_access_token for the
// given installation, reusing a cached token while it is alive (minus
// safety margin) and otherwise fetching a fresh one from Lark.
//
// Concurrent callers serialize on the per-client mutex during the
// uncached path; the cached path takes the mutex only for the lookup
// and releases before doing any I/O. Steady-state contention is
// therefore one map-read under the lock, not a per-call HTTP round
// trip.
func (c *httpAPIClient) tenantAccessToken(ctx context.Context, creds InstallationCredentials) (string, error) {
	if creds.AppID == "" {
		return "", errors.New("lark http client: missing app_id")
	}
	if creds.AppSecret == "" {
		return "", errors.New("lark http client: missing app_secret")
	}

	now := c.cfg.Now()
	c.mu.Lock()
	if t, ok := c.tokens[creds.AppID]; ok && t.expiresAt.After(now) {
		val := t.value
		c.mu.Unlock()
		return val, nil
	}
	c.mu.Unlock()

	// Self-built (internal) app endpoint. Marketplace / multi-tenant
	// apps would use /tenant_access_token/v3 with a different body
	// shape; PersonalAgent in this MVP is per-workspace self-built so
	// we stay on /internal.
	body := map[string]string{
		"app_id":     creds.AppID,
		"app_secret": creds.AppSecret,
	}
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "", body, &resp); err != nil {
		return "", fmt.Errorf("lark http client: tenant_access_token: %w", err)
	}
	if resp.Code != 0 || resp.TenantAccessToken == "" {
		return "", fmt.Errorf("lark http client: tenant_access_token: code=%d msg=%q", resp.Code, resp.Msg)
	}

	expire := time.Duration(resp.Expire) * time.Second
	// Clamp to >= 2× safety margin so a misbehaving upstream that
	// returns a sub-minute expire never makes us cache a token that
	// is already past its safe window.
	if expire < tokenSafetyMargin*2 {
		expire = tokenSafetyMargin * 2
	}
	expiresAt := c.cfg.Now().Add(expire - tokenSafetyMargin)

	c.mu.Lock()
	c.tokens[creds.AppID] = &cachedToken{value: resp.TenantAccessToken, expiresAt: expiresAt}
	c.mu.Unlock()

	return resp.TenantAccessToken, nil
}

// invalidateToken drops the cached token for an app_id. Called when
// Lark surfaces an expired / invalid token error code so the next
// call refreshes instead of looping on a stale entry.
func (c *httpAPIClient) invalidateToken(appID string) {
	c.mu.Lock()
	delete(c.tokens, appID)
	c.mu.Unlock()
}

// SendInteractiveCard posts a fresh interactive card into a chat and
// returns Lark's message_id so the Patcher can target subsequent
// patches at the same card.
func (c *httpAPIClient) SendInteractiveCard(ctx context.Context, p SendCardParams) (string, error) {
	if p.ChatID == "" {
		return "", errors.New("lark http client: missing chat_id")
	}
	if p.CardJSON == "" {
		return "", errors.New("lark http client: missing card json")
	}
	token, err := c.tenantAccessToken(ctx, p.InstallationID)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("receive_id_type", "chat_id")
	body := map[string]string{
		"receive_id": string(p.ChatID),
		"msg_type":   "interactive",
		"content":    p.CardJSON,
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	path := "/open-apis/im/v1/messages?" + q.Encode()
	if err := c.doJSON(ctx, http.MethodPost, path, token, body, &resp); err != nil {
		return "", fmt.Errorf("lark http client: send interactive card: %w", err)
	}
	if resp.Code != 0 || resp.Data.MessageID == "" {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return "", fmt.Errorf("lark http client: send interactive card: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return resp.Data.MessageID, nil
}

// PatchInteractiveCard updates an existing card's body. Lark's
// message-patch endpoint replaces the whole card payload; callers
// (i.e. the Patcher) render the full updated card each time.
func (c *httpAPIClient) PatchInteractiveCard(ctx context.Context, p PatchCardParams) error {
	if p.LarkCardMessageID == "" {
		return errors.New("lark http client: missing card message id")
	}
	if p.CardJSON == "" {
		return errors.New("lark http client: missing card json")
	}
	token, err := c.tenantAccessToken(ctx, p.InstallationID)
	if err != nil {
		return err
	}
	body := map[string]string{"content": p.CardJSON}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	path := "/open-apis/im/v1/messages/" + url.PathEscape(p.LarkCardMessageID)
	if err := c.doJSON(ctx, http.MethodPatch, path, token, body, &resp); err != nil {
		return fmt.Errorf("lark http client: patch interactive card: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return fmt.Errorf("lark http client: patch interactive card: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return nil
}

// SendBindingPromptCard renders the member-binding card and posts it
// directly to the unbound user's open_id (not the chat). Keeping the
// card template inside this client — rather than the dispatcher —
// means the dispatcher never has to know about Lark's card schema.
func (c *httpAPIClient) SendBindingPromptCard(ctx context.Context, p BindingPromptParams) error {
	if p.OpenID == "" {
		return errors.New("lark http client: missing open_id")
	}
	if p.BindURL == "" {
		return errors.New("lark http client: missing bind url")
	}
	cardJSON, err := bindingPromptTemplate(p.BindURL)
	if err != nil {
		return fmt.Errorf("lark http client: render binding prompt: %w", err)
	}
	token, err := c.tenantAccessToken(ctx, p.InstallationID)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("receive_id_type", "open_id")
	body := map[string]string{
		"receive_id": string(p.OpenID),
		"msg_type":   "interactive",
		"content":    cardJSON,
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	path := "/open-apis/im/v1/messages?" + q.Encode()
	if err := c.doJSON(ctx, http.MethodPost, path, token, body, &resp); err != nil {
		return fmt.Errorf("lark http client: send binding prompt: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return fmt.Errorf("lark http client: send binding prompt: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return nil
}

// ExchangeOAuthCode runs the install-time handshake:
//
//  1. POST /authen/v2/oauth/token — exchange the authorization code
//     for the installer's identity (open_id) using the Multica
//     parent app's client_id / client_secret. This is the OAuth 2.0
//     standard endpoint, NOT the legacy /authen/v1/access_token
//     route — Lark's v2 returns the user's open_id directly without
//     a separate /user_info round-trip.
//
//  2. POST /auth/v3/tenant_access_token/internal — mint a
//     tenant_access_token under the parent app so we can call the
//     bot-info endpoint below. The token cache keyed by app_id is
//     reused here, so a steady-state install path costs zero extra
//     token round-trips.
//
//  3. GET /bot/v3/info — fetch the parent app's bot identity
//     (bot_open_id). PersonalAgent MVP keeps one bot identity per
//     Multica deployment: every lark_installation row carries the
//     same bot_open_id because every bot speaks through the parent
//     Lark app. Per-tenant bot identities are a Phase 2 concern
//     (would require Lark Marketplace-app credentials, not
//     internal-app).
//
// The result feeds InstallationParams unchanged: app_id /
// app_secret are the parent app credentials, bot_open_id is the
// parent bot's id, InstallerOpenID is the human who just scanned.
// The downstream OAuthService.HandleCallback validates the result
// (no empty fields) and auto-binds the installer in the same step.
func (c *httpAPIClient) ExchangeOAuthCode(ctx context.Context, code, redirectURI string) (OAuthExchangeResult, error) {
	if c.cfg.OAuthAppID == "" || c.cfg.OAuthAppSecret == "" {
		// Deployment did not configure the parent Lark app credentials;
		// the OAuth callback handler maps this to
		// oauth_exchange_unimplemented so the UI surfaces a precise
		// reason instead of a generic internal_error.
		return OAuthExchangeResult{}, ErrAPIClientNotConfigured
	}
	if code == "" {
		return OAuthExchangeResult{}, errors.New("lark http client: missing authorization code")
	}
	if redirectURI == "" {
		return OAuthExchangeResult{}, errors.New("lark http client: missing redirect_uri")
	}

	// Step 1 — Lark OAuth v2 authorization-code exchange. The v2
	// endpoint follows the OAuth 2.0 RFC 6749 shape (top-level fields,
	// no {code, msg, data} wrapper). Errors come back as
	// {error: "...", error_description: "..."} with HTTP 200; non-2xx
	// is a transport / config failure.
	exchBody := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     c.cfg.OAuthAppID,
		"client_secret": c.cfg.OAuthAppSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
	}
	var exchResp struct {
		// OAuth 2.0 success fields.
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int64  `json:"expires_in"`
		RefreshToken     string `json:"refresh_token"`
		RefreshExpiresIn int64  `json:"refresh_expires_in"`
		Scope            string `json:"scope"`
		OpenID           string `json:"open_id"`
		UnionID          string `json:"union_id"`
		// OAuth 2.0 error fields (Lark v2 returns 200 + this shape).
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/open-apis/authen/v2/oauth/token", "", exchBody, &exchResp); err != nil {
		return OAuthExchangeResult{}, fmt.Errorf("lark http client: oauth token exchange: %w", err)
	}
	if exchResp.Error != "" {
		return OAuthExchangeResult{}, fmt.Errorf("lark http client: oauth token exchange: error=%q description=%q",
			exchResp.Error, exchResp.ErrorDescription)
	}
	if exchResp.OpenID == "" {
		return OAuthExchangeResult{}, errors.New("lark http client: oauth token exchange: response missing open_id")
	}

	// Steps 2+3 — fetch the parent bot identity. We need a
	// tenant_access_token so we can authenticate /bot/v3/info; reuse
	// the cache so a re-install does not re-mint a token that the
	// outbound patcher is already holding.
	token, err := c.tenantAccessToken(ctx, InstallationCredentials{
		AppID:     c.cfg.OAuthAppID,
		AppSecret: c.cfg.OAuthAppSecret,
	})
	if err != nil {
		return OAuthExchangeResult{}, fmt.Errorf("lark http client: tenant_access_token for bot info: %w", err)
	}
	var botResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID         string `json:"open_id"`
			AppName        string `json:"app_name"`
			ActivateStatus int    `json:"activate_status"`
		} `json:"bot"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/open-apis/bot/v3/info", token, nil, &botResp); err != nil {
		return OAuthExchangeResult{}, fmt.Errorf("lark http client: bot info: %w", err)
	}
	if botResp.Code != 0 {
		if isTokenError(botResp.Code) {
			c.invalidateToken(c.cfg.OAuthAppID)
		}
		return OAuthExchangeResult{}, fmt.Errorf("lark http client: bot info: code=%d msg=%q", botResp.Code, botResp.Msg)
	}
	if botResp.Bot.OpenID == "" {
		return OAuthExchangeResult{}, errors.New("lark http client: bot info: response missing bot.open_id")
	}

	return OAuthExchangeResult{
		AppID:            c.cfg.OAuthAppID,
		AppSecret:        c.cfg.OAuthAppSecret,
		BotOpenID:        botResp.Bot.OpenID,
		InstallerOpenID:  OpenID(exchResp.OpenID),
		InstallerUnionID: exchResp.UnionID,
	}, nil
}

// doJSON encapsulates the verb + URL + auth-header + JSON
// encode/decode dance so each public method stays a thin shape-only
// adapter. token == "" skips the Authorization header (only the
// tenant_access_token endpoint takes that path).
func (c *httpAPIClient) doJSON(ctx context.Context, method, path, token string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(rawBody), 512))
	}
	if out != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, out); err != nil {
			return fmt.Errorf("decode body: %w (raw=%s)", err, truncate(string(rawBody), 256))
		}
	}
	return nil
}

func isTokenError(code int) bool {
	return code == codeTokenExpired || code == codeTokenInvalid
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// bindingPromptTemplate renders the "you need to bind" interactive
// card. Single primary CTA pointing at the redemption URL; the rest
// of the body is plain-text Chinese copy matching the in-app voice.
//
// Kept here (not in defaultRenderer) so the binding card template can
// evolve independently of the streaming-status cards the Patcher
// renders — they have different lifecycles (binding card is one-shot,
// status cards are patched in place).
func bindingPromptTemplate(bindURL string) (string, error) {
	doc := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "blue",
			"title":    map[string]any{"tag": "plain_text", "content": "Multica"},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": "你还没有绑定 Multica 账户。点击下方按钮完成绑定后即可使用此 Agent。",
				},
			},
			map[string]any{
				"tag": "action",
				"actions": []any{
					map[string]any{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "去绑定"},
						"type": "primary",
						"url":  bindURL,
					},
				},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
