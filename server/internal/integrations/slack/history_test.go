package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeHistoryQueries struct {
	binding    db.ChannelChatSessionBinding
	bindingErr error
	inst       db.ChannelInstallation
	instErr    error
}

func (f *fakeHistoryQueries) GetChannelChatSessionBindingBySession(context.Context, db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error) {
	return f.binding, f.bindingErr
}

func (f *fakeHistoryQueries) GetChannelInstallation(context.Context, db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	return f.inst, f.instErr
}

type fakeHistoryClient struct {
	historyMsgs  []slack.Message
	repliesMsgs  []slack.Message
	users        []slack.User
	historyCalls int
	repliesCalls int
	lastHistory  *slack.GetConversationHistoryParameters
	lastReplies  *slack.GetConversationRepliesParameters
}

func (f *fakeHistoryClient) GetConversationHistoryContext(_ context.Context, p *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	f.historyCalls++
	f.lastHistory = p
	return &slack.GetConversationHistoryResponse{Messages: f.historyMsgs}, nil
}

func (f *fakeHistoryClient) GetConversationRepliesContext(_ context.Context, p *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	f.repliesCalls++
	f.lastReplies = p
	return f.repliesMsgs, false, "", nil
}

func (f *fakeHistoryClient) GetUsersInfoContext(_ context.Context, _ ...string) (*[]slack.User, error) {
	return &f.users, nil
}

func msg(user, text, ts string) slack.Message {
	return slack.Message{Msg: slack.Msg{User: user, Text: text, Timestamp: ts}}
}

func activeSlackInstall() db.ChannelInstallation {
	return db.ChannelInstallation{Status: "active", Config: slackInstallConfigJSON()}
}

func newTestHistory(q historyQueries, fc historyClient) *History {
	h := NewHistory(q, nil, nil) // nil decrypter => stored bytes treated as plaintext
	h.newClient = func(string) historyClient { return fc }
	return h
}

// TestHistoryFetchTopLevelUsesConversationHistory verifies a DM / top-level
// session (no thread_ts in the binding config) reads conversations.history,
// normalizes oldest-first, maps roles, and labels speakers.
func TestHistoryFetchTopLevelUsesConversationHistory(t *testing.T) {
	q := &fakeHistoryQueries{
		binding: db.ChannelChatSessionBinding{
			InstallationID: uid(2),
			ChannelChatID:  "C1",
			Config:         []byte(`{"channel_id":"C1"}`),
		},
		inst: activeSlackInstall(),
	}
	fc := &fakeHistoryClient{
		// Slack returns newest-first; the bot (UBOT) replied last.
		historyMsgs: []slack.Message{
			msg("UBOT", "on it", "102.000000"),
			msg("U1", "@bot look into this", "101.000000"),
			msg("U2", "alert: 5xx spiking", "100.000000"),
		},
		users: []slack.User{
			{ID: "U1", RealName: "Alice"},
			// U2 intentionally unresolved -> positional fallback.
		},
	}
	h := newTestHistory(q, fc)

	page, err := h.Fetch(context.Background(), uid(9), channel.HistoryOptions{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fc.historyCalls != 1 || fc.repliesCalls != 0 {
		t.Fatalf("expected conversations.history, got history=%d replies=%d", fc.historyCalls, fc.repliesCalls)
	}
	if fc.lastHistory.ChannelID != "C1" {
		t.Errorf("channel id = %q, want C1", fc.lastHistory.ChannelID)
	}
	if page.ChannelType != "slack" {
		t.Errorf("channel type = %q, want slack", page.ChannelType)
	}
	if len(page.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(page.Messages))
	}
	// Oldest-first.
	if page.Messages[0].TS != "100.000000" || page.Messages[2].TS != "102.000000" {
		t.Errorf("not oldest-first: %q .. %q", page.Messages[0].TS, page.Messages[2].TS)
	}
	// U2 unresolved -> positional; U1 resolved; bot -> assistant/Bot.
	if got := page.Messages[0]; got.Author != "User 1" || got.Role != channel.HistoryRoleUser {
		t.Errorf("msg0 author/role = %q/%q, want User 1/user", got.Author, got.Role)
	}
	if got := page.Messages[1]; got.Author != "Alice" || got.Role != channel.HistoryRoleUser {
		t.Errorf("msg1 author/role = %q/%q, want Alice/user", got.Author, got.Role)
	}
	if got := page.Messages[2]; got.Author != "Bot" || got.Role != channel.HistoryRoleAssistant {
		t.Errorf("msg2 author/role = %q/%q, want Bot/assistant", got.Author, got.Role)
	}
}

// TestHistoryFetchThreadUsesConversationReplies verifies a session rooted in a
// real thread (thread_ts present) reads conversations.replies anchored on it.
func TestHistoryFetchThreadUsesConversationReplies(t *testing.T) {
	q := &fakeHistoryQueries{
		binding: db.ChannelChatSessionBinding{
			InstallationID: uid(2),
			ChannelChatID:  "C1:50.000000",
			Config:         []byte(`{"channel_id":"C1","thread_ts":"50.000000"}`),
		},
		inst: activeSlackInstall(),
	}
	fc := &fakeHistoryClient{
		repliesMsgs: []slack.Message{
			msg("U1", "second", "52.000000"),
			msg("U1", "root", "50.000000"),
		},
	}
	h := newTestHistory(q, fc)

	page, err := h.Fetch(context.Background(), uid(9), channel.HistoryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fc.repliesCalls != 1 || fc.historyCalls != 0 {
		t.Fatalf("expected conversations.replies, got history=%d replies=%d", fc.historyCalls, fc.repliesCalls)
	}
	if fc.lastReplies.Timestamp != "50.000000" || fc.lastReplies.ChannelID != "C1" {
		t.Errorf("replies anchored at %q/%q, want C1/50.000000", fc.lastReplies.ChannelID, fc.lastReplies.Timestamp)
	}
	if len(page.Messages) != 2 || page.Messages[0].TS != "50.000000" {
		t.Fatalf("expected 2 msgs oldest-first, got %+v", page.Messages)
	}
}

// TestHistoryFetchNoBinding maps a missing Slack binding to ErrNoSlackSession so
// the endpoint can answer "not a channel conversation" rather than fail.
func TestHistoryFetchNoBinding(t *testing.T) {
	q := &fakeHistoryQueries{bindingErr: pgx.ErrNoRows}
	h := newTestHistory(q, &fakeHistoryClient{})
	if _, err := h.Fetch(context.Background(), uid(9), channel.HistoryOptions{}); !errors.Is(err, ErrNoSlackSession) {
		t.Fatalf("err = %v, want ErrNoSlackSession", err)
	}
}

// TestHistoryFetchInactiveInstall treats a revoked installation as empty.
func TestHistoryFetchInactiveInstall(t *testing.T) {
	q := &fakeHistoryQueries{
		binding: db.ChannelChatSessionBinding{InstallationID: uid(2), ChannelChatID: "C1", Config: []byte(`{"channel_id":"C1"}`)},
		inst:    db.ChannelInstallation{Status: "revoked", Config: slackInstallConfigJSON()},
	}
	h := newTestHistory(q, &fakeHistoryClient{})
	if _, err := h.Fetch(context.Background(), uid(9), channel.HistoryOptions{}); !errors.Is(err, ErrNoSlackSession) {
		t.Fatalf("err = %v, want ErrNoSlackSession", err)
	}
}

// TestHistoryLimitClamp confirms an over-large limit is clamped to the per-page
// cap before hitting the Slack API.
func TestHistoryLimitClamp(t *testing.T) {
	q := &fakeHistoryQueries{
		binding: db.ChannelChatSessionBinding{InstallationID: uid(2), ChannelChatID: "C1", Config: []byte(`{"channel_id":"C1"}`)},
		inst:    activeSlackInstall(),
	}
	fc := &fakeHistoryClient{}
	h := newTestHistory(q, fc)
	if _, err := h.Fetch(context.Background(), uid(9), channel.HistoryOptions{Limit: 5000}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fc.lastHistory.Limit != maxHistoryLimit {
		t.Errorf("limit = %d, want clamp to %d", fc.lastHistory.Limit, maxHistoryLimit)
	}
}

// TestSlackSessionRoutingThreadTS pins the thread-vs-channel discriminator the
// history reader keys off: only a genuine thread reply records thread_ts.
func TestSlackSessionRoutingThreadTS(t *testing.T) {
	cases := []struct {
		name     string
		chatType channel.ChatType
		threadID string
		msgID    string
		wantTS   string
	}{
		{"thread reply", channel.ChatTypeGroup, "50.0", "60.0", "50.0"},
		{"top-level mention", channel.ChatTypeGroup, "", "60.0", ""},
		{"dm", channel.ChatTypeP2P, "", "60.0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, _ := slackSessionRouting(channel.InboundMessage{
				MessageID: tc.msgID,
				Source:    channel.Source{ChatID: "C1", ChatType: tc.chatType, ThreadID: tc.threadID},
			})
			got, _ := historyTargetFromConfig(cfg)
			if got != tc.wantTS {
				t.Errorf("thread_ts = %q, want %q", got, tc.wantTS)
			}
		})
	}
}

// historyTargetFromConfig is a tiny test shim that mirrors how the reader reads
// thread_ts out of a binding config blob.
func historyTargetFromConfig(cfg []byte) (threadTS, channelID string) {
	_, ts := historyTarget(db.ChannelChatSessionBinding{Config: cfg})
	return ts, ""
}
