package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, secretbox.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	return box
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	u, err := util.ParseUUID(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

type fakeInstallQueries struct {
	upsertParams db.UpsertChannelInstallationByAppIDParams
	bindParams   db.CreateChannelUserBindingParams
	bindCalled   bool
	rowID        pgtype.UUID
}

func (f *fakeInstallQueries) UpsertChannelInstallationByAppID(_ context.Context, arg db.UpsertChannelInstallationByAppIDParams) (db.ChannelInstallation, error) {
	f.upsertParams = arg
	return db.ChannelInstallation{
		ID:              f.rowID,
		WorkspaceID:     arg.WorkspaceID,
		AgentID:         arg.AgentID,
		ChannelType:     arg.ChannelType,
		Config:          arg.Config,
		InstallerUserID: arg.InstallerUserID,
		Status:          "active",
	}, nil
}

func (f *fakeInstallQueries) CreateChannelUserBinding(_ context.Context, arg db.CreateChannelUserBindingParams) (db.ChannelUserBinding, error) {
	f.bindCalled = true
	f.bindParams = arg
	return db.ChannelUserBinding{}, nil
}

func (f *fakeInstallQueries) ListChannelInstallationsByWorkspace(_ context.Context, _ db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error) {
	return nil, nil
}

func (f *fakeInstallQueries) GetChannelInstallationInWorkspace(_ context.Context, _ db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error) {
	return db.ChannelInstallation{}, nil
}

func (f *fakeInstallQueries) SetChannelInstallationStatus(_ context.Context, _ db.SetChannelInstallationStatusParams) error {
	return nil
}

func newTestInstallService(t *testing.T, q installQueries, oauth OAuthConfig) *InstallService {
	t.Helper()
	svc, err := NewInstallService(q, testBox(t), oauth, nil)
	if err != nil {
		t.Fatalf("NewInstallService: %v", err)
	}
	return svc
}

func fullOAuthConfig() OAuthConfig {
	return OAuthConfig{
		ClientID:     "client-123",
		ClientSecret: "secret-xyz",
		RedirectURL:  "https://multica.example/api/slack/oauth/callback",
	}
}

func TestInstallSupported(t *testing.T) {
	if newTestInstallService(t, &fakeInstallQueries{}, OAuthConfig{}).InstallSupported() {
		t.Error("no client creds => not supported")
	}
	if !newTestInstallService(t, &fakeInstallQueries{}, fullOAuthConfig()).InstallSupported() {
		t.Error("full creds => supported")
	}
}

func TestBegin_BuildsAuthorizeURL(t *testing.T) {
	svc := newTestInstallService(t, &fakeInstallQueries{}, fullOAuthConfig())
	got, err := svc.Begin(BeginParams{
		WorkspaceID: mustUUID(t, "11111111-1111-1111-1111-111111111111"),
		AgentID:     mustUUID(t, "22222222-2222-2222-2222-222222222222"),
		InitiatorID: mustUUID(t, "33333333-3333-3333-3333-333333333333"),
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !strings.HasPrefix(got, "https://slack.com/oauth/v2/authorize?") {
		t.Fatalf("authorize URL = %q", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if u.Query().Get("client_id") != "client-123" {
		t.Errorf("client_id = %q", u.Query().Get("client_id"))
	}
	if u.Query().Get("redirect_uri") != fullOAuthConfig().RedirectURL {
		t.Errorf("redirect_uri = %q", u.Query().Get("redirect_uri"))
	}
	if !strings.Contains(u.Query().Get("scope"), "chat:write") {
		t.Errorf("scope = %q, want default scopes", u.Query().Get("scope"))
	}
	// The state must round-trip back to the originating identity.
	st, err := svc.verifyState(u.Query().Get("state"))
	if err != nil {
		t.Fatalf("verifyState: %v", err)
	}
	if st.WorkspaceID != "11111111-1111-1111-1111-111111111111" || st.AgentID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("state = %+v", st)
	}
}

func TestBegin_NotSupported(t *testing.T) {
	svc := newTestInstallService(t, &fakeInstallQueries{}, OAuthConfig{})
	if _, err := svc.Begin(BeginParams{}); err != ErrInstallNotSupported {
		t.Errorf("Begin without creds = %v, want ErrInstallNotSupported", err)
	}
}

func TestState_ExpiredAndTampered(t *testing.T) {
	svc := newTestInstallService(t, &fakeInstallQueries{}, fullOAuthConfig())
	// Expired.
	svc.now = func() time.Time { return time.Unix(1_000_000, 0) }
	token, err := svc.signState(installState{WorkspaceID: "w", Exp: 999_999}) // already past
	if err != nil {
		t.Fatalf("signState: %v", err)
	}
	if _, err := svc.verifyState(token); err != ErrInvalidState {
		t.Errorf("expired state = %v, want ErrInvalidState", err)
	}
	// Tampered (flip a char) must fail authentication.
	good, _ := svc.signState(installState{WorkspaceID: "w", Exp: 2_000_000})
	bad := good[:len(good)-2] + "AA"
	if _, err := svc.verifyState(bad); err != ErrInvalidState {
		t.Errorf("tampered state = %v, want ErrInvalidState", err)
	}
}

func TestComplete_ExchangesUpsertsAndBindsInstaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		// Sanity: the exchange must forward the code + our client creds.
		if r.PostForm.Get("code") != "the-code" || r.PostForm.Get("client_id") != "client-123" {
			t.Errorf("exchange form = %v", r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"ok": true,
			"access_token": "xoxb-bot-token",
			"token_type": "bot",
			"scope": "chat:write,app_mentions:read",
			"bot_user_id": "UBOT",
			"app_id": "A123",
			"team": {"id": "T123", "name": "Acme Inc"},
			"authed_user": {"id": "UADMIN"}
		}`))
	}))
	defer srv.Close()

	q := &fakeInstallQueries{rowID: mustUUID(t, "44444444-4444-4444-4444-444444444444")}
	svc := newTestInstallService(t, q, fullOAuthConfig())
	svc.apiURL = srv.URL + "/"

	state, err := svc.signState(installState{
		WorkspaceID: "11111111-1111-1111-1111-111111111111",
		AgentID:     "22222222-2222-2222-2222-222222222222",
		UserID:      "33333333-3333-3333-3333-333333333333",
		Exp:         svc.now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("signState: %v", err)
	}

	res, err := svc.Complete(context.Background(), "the-code", state)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.TeamID != "T123" || res.TeamName != "Acme Inc" {
		t.Errorf("result = %+v", res)
	}
	if res.InstallationID != q.rowID {
		t.Errorf("installation id = %v, want %v", res.InstallationID, q.rowID)
	}

	// The upserted config must carry the routing key + bot user + an ENCRYPTED
	// (base64) bot token — never plaintext.
	if q.upsertParams.ChannelType != string(TypeSlack) {
		t.Errorf("channel_type = %q", q.upsertParams.ChannelType)
	}
	var cfg installConfig
	if err := json.Unmarshal(q.upsertParams.Config, &cfg); err != nil {
		t.Fatalf("decode upserted config: %v", err)
	}
	if cfg.AppID != "T123" || cfg.TeamID != "T123" || cfg.BotUserID != "UBOT" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.BotTokenEncrypted == "" || strings.Contains(cfg.BotTokenEncrypted, "xoxb-bot-token") {
		t.Errorf("bot token must be stored encrypted, got %q", cfg.BotTokenEncrypted)
	}
	// And it must decrypt back to the original token via the package's own path.
	creds, err := decodeCredentials(q.upsertParams.Config, svc.box.Open)
	if err != nil {
		t.Fatalf("decodeCredentials: %v", err)
	}
	if creds.BotToken != "xoxb-bot-token" {
		t.Errorf("decrypted bot token = %q", creds.BotToken)
	}

	// The installer (authed_user.id) is auto-bound so their first message is
	// not dropped as unbound.
	if !q.bindCalled {
		t.Fatal("installer should be auto-bound")
	}
	if q.bindParams.ChannelUserID != "UADMIN" || q.bindParams.ChannelType != string(TypeSlack) {
		t.Errorf("installer binding = %+v", q.bindParams)
	}
}

func TestComplete_InvalidState(t *testing.T) {
	svc := newTestInstallService(t, &fakeInstallQueries{}, fullOAuthConfig())
	if _, err := svc.Complete(context.Background(), "code", "not-a-valid-state"); err != ErrInvalidState {
		t.Errorf("Complete with bad state = %v, want ErrInvalidState", err)
	}
}

func TestComplete_OAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": false, "error": "invalid_code"}`))
	}))
	defer srv.Close()

	svc := newTestInstallService(t, &fakeInstallQueries{}, fullOAuthConfig())
	svc.apiURL = srv.URL + "/"
	state, _ := svc.signState(installState{
		WorkspaceID: "11111111-1111-1111-1111-111111111111",
		AgentID:     "22222222-2222-2222-2222-222222222222",
		UserID:      "33333333-3333-3333-3333-333333333333",
		Exp:         svc.now().Add(time.Minute).Unix(),
	})
	if _, err := svc.Complete(context.Background(), "bad-code", state); err == nil {
		t.Error("expected error from oauth.v2.access ok:false")
	}
}
