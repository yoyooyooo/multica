package slack

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This file is the Slack OAuth self-serve install backend (MUL-3666, B2):
// Multica hosts ONE Slack app, and a workspace admin installs it into their
// Slack workspace with an in-product OAuth flow. The OAuth grant yields a
// per-workspace bot token (xoxb-) which — together with team_id — is upserted
// as a channel_type='slack' installation. It is the Slack equivalent of
// lark.RegistrationService / lark.InstallationService, but uses the standard
// OAuth v2 redirect (authorize -> callback -> code exchange) instead of the
// device-code flow, mirroring the GitHub App connect handler.

// stateTTL bounds how long an in-flight OAuth authorization may sit before the
// callback's state is rejected. The state is stateless (sealed, not stored), so
// this is enforced by an embedded expiry rather than a session row.
const stateTTL = 10 * time.Minute

// defaultScopes are the bot scopes requested at install time. They cover the
// inbound events the adapter consumes (app mentions + message history across
// DMs / channels / private channels / group DMs) and outbound posting. Override
// with MULTICA_SLACK_SCOPES if the hosted app is configured differently.
var defaultScopes = []string{
	"app_mentions:read",
	"channels:history",
	"chat:write",
	"groups:history",
	"im:history",
	"mpim:history",
}

var (
	// ErrInstallNotSupported is returned by Begin/Complete when the OAuth
	// client credentials are not configured (so no install can be performed,
	// though listing/revoking existing installs still works).
	ErrInstallNotSupported = errors.New("slack: oauth install not configured (client id/secret/redirect missing)")
	// ErrInvalidState is returned when the callback's state fails to decrypt,
	// is malformed, or has expired.
	ErrInvalidState = errors.New("slack: invalid or expired oauth state")
	// ErrInstallationNotFound surfaces "no row matches in this workspace".
	ErrInstallationNotFound = errors.New("slack installation not found")
)

// OAuthConfig holds the deployment-level credentials for the hosted Slack app.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // {PublicURL}/api/slack/oauth/callback
	Scopes       []string
}

func (c OAuthConfig) supported() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// installQueries is the slice of generated queries InstallService needs.
// *db.Queries satisfies it.
type installQueries interface {
	UpsertChannelInstallationByAppID(ctx context.Context, arg db.UpsertChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
	CreateChannelUserBinding(ctx context.Context, arg db.CreateChannelUserBindingParams) (db.ChannelUserBinding, error)
	ListChannelInstallationsByWorkspace(ctx context.Context, arg db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(ctx context.Context, arg db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
	SetChannelInstallationStatus(ctx context.Context, arg db.SetChannelInstallationStatusParams) error
}

// InstallService owns the OAuth install lifecycle and the at-rest encryption of
// the bot token, so no caller can write a channel_installation with a plaintext
// token. The box MUST be non-nil (we refuse plaintext storage even in dev).
type InstallService struct {
	oauth      OAuthConfig
	box        *secretbox.Box
	q          installQueries
	httpClient *http.Client
	logger     *slog.Logger
	now        func() time.Time

	// apiURL overrides the Slack API base for the code exchange (tests point
	// it at an httptest server). Empty uses the real Slack API.
	apiURL string
}

// NewInstallService binds the service to queries, an encryption box, and the
// hosted app's OAuth credentials. Listing / revoking work whenever the box is
// present; Begin / Complete additionally require the OAuth credentials
// (InstallSupported reports whether they are set).
func NewInstallService(q installQueries, box *secretbox.Box, oauth OAuthConfig, logger *slog.Logger) (*InstallService, error) {
	if box == nil {
		return nil, errors.New("slack: InstallService requires a non-nil secretbox.Box")
	}
	if q == nil {
		return nil, errors.New("slack: InstallService requires queries")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if len(oauth.Scopes) == 0 {
		oauth.Scopes = defaultScopes
	}
	return &InstallService{
		oauth:      oauth,
		box:        box,
		q:          q,
		httpClient: http.DefaultClient,
		logger:     logger,
		now:        time.Now,
	}, nil
}

// InstallSupported reports whether the OAuth begin/complete path is wired (the
// hosted app's client credentials are configured).
func (s *InstallService) InstallSupported() bool { return s.oauth.supported() }

// BeginParams identifies who is installing and which agent the bot represents.
type BeginParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
}

// Begin returns the Slack authorize URL the admin's browser is redirected to.
// The workspace/agent/initiator are sealed into the OAuth state so the callback
// can attribute the install without a server-side session.
func (s *InstallService) Begin(p BeginParams) (string, error) {
	if !s.InstallSupported() {
		return "", ErrInstallNotSupported
	}
	state, err := s.signState(installState{
		WorkspaceID: util.UUIDToString(p.WorkspaceID),
		AgentID:     util.UUIDToString(p.AgentID),
		UserID:      util.UUIDToString(p.InitiatorID),
		Exp:         s.now().Add(stateTTL).Unix(),
		Nonce:       randNonce(),
	})
	if err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("client_id", s.oauth.ClientID)
	v.Set("scope", strings.Join(s.oauth.Scopes, ","))
	v.Set("redirect_uri", s.oauth.RedirectURL)
	v.Set("state", state)
	return "https://slack.com/oauth/v2/authorize?" + v.Encode(), nil
}

// CompletedInstall is the result of a successful OAuth callback.
type CompletedInstall struct {
	WorkspaceID    pgtype.UUID
	AgentID        pgtype.UUID
	InstallationID pgtype.UUID
	TeamID         string
	TeamName       string
}

// Complete handles the OAuth callback: verify the state, exchange the code for a
// bot token via oauth.v2.access, upsert the installation (bot token encrypted at
// rest), and bind the installing user to their Slack id so their first message
// is not dropped as unbound.
func (s *InstallService) Complete(ctx context.Context, code, rawState string) (CompletedInstall, error) {
	if !s.InstallSupported() {
		return CompletedInstall{}, ErrInstallNotSupported
	}
	st, err := s.verifyState(rawState)
	if err != nil {
		return CompletedInstall{}, err
	}
	wsID, err := util.ParseUUID(st.WorkspaceID)
	if err != nil {
		return CompletedInstall{}, ErrInvalidState
	}
	agentID, err := util.ParseUUID(st.AgentID)
	if err != nil {
		return CompletedInstall{}, ErrInvalidState
	}
	userID, err := util.ParseUUID(st.UserID)
	if err != nil {
		return CompletedInstall{}, ErrInvalidState
	}

	resp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return CompletedInstall{}, err
	}
	if resp.AccessToken == "" || resp.Team.ID == "" || resp.BotUserID == "" {
		return CompletedInstall{}, errors.New("slack oauth: incomplete response (token/team/bot_user_id missing)")
	}

	sealed, err := s.box.Seal([]byte(resp.AccessToken))
	if err != nil {
		return CompletedInstall{}, fmt.Errorf("encrypt slack bot token: %w", err)
	}
	cfgJSON, err := json.Marshal(installConfig{
		AppID:             resp.Team.ID,
		TeamID:            resp.Team.ID,
		BotUserID:         resp.BotUserID,
		BotTokenEncrypted: base64.StdEncoding.EncodeToString(sealed),
	})
	if err != nil {
		return CompletedInstall{}, fmt.Errorf("encode slack installation config: %w", err)
	}
	// Team-keyed upsert: a Slack workspace (team_id) is one installation. Re-
	// connecting the same team — including to represent a different agent —
	// updates the existing row rather than colliding with the (channel_type,
	// app_id) unique index (Niko review must-fix #3).
	inst, err := s.q.UpsertChannelInstallationByAppID(ctx, db.UpsertChannelInstallationByAppIDParams{
		WorkspaceID:     wsID,
		AgentID:         agentID,
		ChannelType:     string(TypeSlack),
		Config:          cfgJSON,
		InstallerUserID: userID,
	})
	if err != nil {
		return CompletedInstall{}, fmt.Errorf("upsert slack installation: %w", err)
	}

	// Auto-bind the installer to their Slack user id (authed_user.id) so their
	// own first DM / mention is not dropped as unbound — mirroring Feishu's
	// installer auto-bind. Best-effort: the install itself already succeeded, so
	// a bind failure is logged, not fatal (the user can rebind later).
	if installerSlackID := resp.AuthedUser.ID; installerSlackID != "" {
		if _, err := s.q.CreateChannelUserBinding(ctx, db.CreateChannelUserBindingParams{
			WorkspaceID:    wsID,
			MulticaUserID:  userID,
			InstallationID: inst.ID,
			ChannelType:    string(TypeSlack),
			ChannelUserID:  installerSlackID,
			Config:         []byte(`{}`),
		}); err != nil {
			s.logger.WarnContext(ctx, "slack: installer auto-bind failed (install still active)",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	}

	return CompletedInstall{
		WorkspaceID:    wsID,
		AgentID:        agentID,
		InstallationID: inst.ID,
		TeamID:         resp.Team.ID,
		TeamName:       resp.Team.Name,
	}, nil
}

// ListByWorkspace returns every Slack installation in the workspace (active and
// revoked), for the management surface.
func (s *InstallService) ListByWorkspace(ctx context.Context, wsID pgtype.UUID) ([]db.ChannelInstallation, error) {
	return s.q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: wsID,
		ChannelType: string(TypeSlack),
	})
}

// GetInWorkspace is the workspace-scoped lookup so a forged installation id from
// another workspace returns NotFound instead of leaking existence.
func (s *InstallService) GetInWorkspace(ctx context.Context, id, wsID pgtype.UUID) (db.ChannelInstallation, error) {
	inst, err := s.q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: wsID,
		ChannelType: string(TypeSlack),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelInstallation{}, ErrInstallationNotFound
		}
		return db.ChannelInstallation{}, err
	}
	return inst, nil
}

// Revoke flips status to 'revoked'. The row is preserved for audit; a re-install
// flips it back to 'active'. The connector simply stops resolving the team
// (GetChannelInstallationByAppID filters to active), and outbound drops too.
func (s *InstallService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.q.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     id,
		Status: "revoked",
	})
}

// ---- OAuth code exchange ----

func (s *InstallService) exchangeCode(ctx context.Context, code string) (*slack.OAuthV2Response, error) {
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	var opts []slack.OAuthOption
	if s.apiURL != "" {
		opts = append(opts, slack.OAuthOptionAPIURL(s.apiURL))
	}
	resp, err := slack.GetOAuthV2ResponseContext(ctx, client, s.oauth.ClientID, s.oauth.ClientSecret, code, s.oauth.RedirectURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("slack oauth exchange: %w", err)
	}
	return resp, nil
}

// ---- sealed OAuth state ----

// installState is the OAuth state, sealed with the deployment secretbox so it is
// both tamper-proof and confidential without a server-side session store.
type installState struct {
	WorkspaceID string `json:"w"`
	AgentID     string `json:"a"`
	UserID      string `json:"u"`
	Exp         int64  `json:"e"`
	Nonce       string `json:"n"`
}

func (s *InstallService) signState(st installState) (string, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	sealed, err := s.box.Seal(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *InstallService) verifyState(token string) (installState, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return installState{}, ErrInvalidState
	}
	raw, err := s.box.Open(sealed)
	if err != nil {
		return installState{}, ErrInvalidState
	}
	var st installState
	if err := json.Unmarshal(raw, &st); err != nil {
		return installState{}, ErrInvalidState
	}
	if s.now().Unix() > st.Exp {
		return installState{}, ErrInvalidState
	}
	return st, nil
}

func randNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic and rare; a weak nonce still
		// leaves the state sealed + expiry-bounded, so degrade rather than fail.
		return "n"
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
