// Package slack is the Slack integration for the channel-agnostic engine. It
// began (MUL-3516) as a per-installation channel.Channel adapter and was
// reshaped (MUL-3666) into the multi-tenant B2 model: Multica hosts ONE Slack
// app, workspace admins self-install via OAuth, and a single deployment-level
// Socket Mode connection (AppConnector) receives events for every installed
// workspace and routes each inbound event to its installation by team_id. Each
// channel_installation carries only that workspace's bot token (xoxb-, for
// outbound) plus routing metadata — not its own connection. The inbound
// translation (Events API payload -> channel.InboundMessage) lives in
// inbound.go; the outbound reply path (chat.postMessage with Markdown->mrkdwn
// conversion + threading) lives in channel.go. The design references the proven
// Slack adapter in Nous Research's Hermes Agent.
package slack

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// installConfig is the JSON shape stored in channel_installation.config for a
// Slack installation. The cross-platform columns stay flat; everything
// Slack-specific lives in this opaque blob (the documented config boundary).
//
// app_id holds the Slack team_id — the per-installation routing key — so the
// generic GetChannelInstallationByAppID query (which reads config->>'app_id')
// and the (channel_type, config->>'app_id') unique index route Slack inbound
// events with NO new query and NO schema change. team_id is also kept as its
// own field for readability; the two carry the same value.
//
// The bot token (xoxb-…, obtained per workspace via OAuth) authorizes Web API
// calls (chat.postMessage) and is stored as base64-encoded secretbox ciphertext
// (never plaintext), mirroring Feishu's app_secret_encrypted. There is NO
// per-installation app-level token: under the B2 model the Socket Mode
// connection uses ONE deployment-level app token (xapp-, from env), since
// app-level tokens cannot be obtained through OAuth.
type installConfig struct {
	AppID             string `json:"app_id"`
	TeamID            string `json:"team_id,omitempty"`
	BotUserID         string `json:"bot_user_id,omitempty"`
	BotTokenEncrypted string `json:"bot_token_encrypted"`
}

// credentials is the decoded, decrypted form the outbound sender runs on. The
// installation IDENTITY (workspace / agent / installer) is deliberately absent:
// it is resolved per message by the Router's InstallationResolver, exactly as
// the Feishu adapter does.
type credentials struct {
	TeamID    string
	BotUserID string
	BotToken  string
}

// Decrypter turns stored ciphertext into plaintext. The wiring injects a
// secretbox-backed implementation; tests inject an identity decrypter (or nil,
// which treats the stored bytes as plaintext).
type Decrypter func(ciphertext []byte) (plaintext []byte, err error)

// decodeCredentials parses the per-installation config blob and decrypts the
// stored tokens. It is the single place the Slack config JSON is interpreted.
func decodeCredentials(raw json.RawMessage, decrypt Decrypter) (credentials, error) {
	if len(raw) == 0 {
		return credentials{}, errors.New("slack: empty installation config")
	}
	var cfg installConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return credentials{}, fmt.Errorf("decode slack installation config: %w", err)
	}
	botToken, err := decryptToken(cfg.BotTokenEncrypted, decrypt)
	if err != nil {
		return credentials{}, fmt.Errorf("decrypt bot token: %w", err)
	}
	teamID := cfg.TeamID
	if teamID == "" {
		teamID = cfg.AppID
	}
	return credentials{
		TeamID:    teamID,
		BotUserID: cfg.BotUserID,
		BotToken:  botToken,
	}, nil
}

// botUserIDFromConfig reads just the bot_user_id from a stored installation
// config blob, without touching the encrypted tokens. The AppConnector uses it
// to resolve the @-mention identity for an inbound event's team.
func botUserIDFromConfig(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var cfg installConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("decode slack installation config: %w", err)
	}
	return cfg.BotUserID, nil
}

// decryptToken base64-decodes the stored ciphertext (tolerating the MIME
// newline wrapping PostgreSQL's encode(...,'base64') emits) and runs it through
// the injected Decrypter. An empty stored value decodes to an empty token; a
// nil Decrypter treats the decoded bytes as plaintext (test convenience).
func decryptToken(enc string, decrypt Decrypter) (string, error) {
	if enc == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(stripWhitespace(enc))
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if decrypt == nil {
		return string(ciphertext), nil
	}
	plaintext, err := decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// stripWhitespace removes ASCII whitespace so a MIME-wrapped base64 string
// (newlines every 64 chars) and an unwrapped one decode identically.
func stripWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
