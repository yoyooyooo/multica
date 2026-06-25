package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/slack-go/slack/slackevents"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// fakeBotUsers is a deterministic BotUserLookup for tests: it maps team_id to a
// bot user id, and can be told to fail.
type fakeBotUsers struct {
	byTeam map[string]string
	err    error
	calls  int
}

func (f *fakeBotUsers) BotUserID(_ context.Context, teamID string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.byTeam[teamID], nil
}

func newTestConnector(botUsers BotUserLookup, handler channel.InboundHandler) *AppConnector {
	return NewAppConnector(AppConnectorConfig{
		AppToken: "xapp-test",
		Handler:  handler,
		BotUsers: botUsers,
	})
}

func TestDispatch_RoutesByTeamAndResolvesBotUser(t *testing.T) {
	var got channel.InboundMessage
	calls := 0
	c := newTestConnector(
		&fakeBotUsers{byTeam: map[string]string{"T1": "UBOT"}},
		func(_ context.Context, m channel.InboundMessage) error { calls++; got = m; return nil },
	)
	// A channel message mentioning the bot: the connector must resolve UBOT for
	// team T1, mark it addressed, and strip the mention.
	e := eventsAPI(&slackevents.MessageEvent{
		User: "UALICE", Text: "<@UBOT> create an issue", Channel: "C1", ChannelType: "channel", TimeStamp: "1.2",
	})
	if err := c.dispatchEventsAPI(context.Background(), e); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler called %d times, want 1", calls)
	}
	if !got.AddressedToBot {
		t.Error("channel mention must be addressed to bot")
	}
	if got.Text != "create an issue" {
		t.Errorf("Text = %q, want mention stripped", got.Text)
	}
	if got.Source.ChannelType != TypeSlack || got.Source.ChatID != "C1" {
		t.Errorf("source = %+v", got.Source)
	}
}

func TestDispatch_SkipsOwnMessage(t *testing.T) {
	// The bot's own message (User == resolved bot user id) must never reach the
	// handler — relies on the per-team bot user id resolution.
	calls := 0
	c := newTestConnector(
		&fakeBotUsers{byTeam: map[string]string{"T1": "UBOT"}},
		func(_ context.Context, _ channel.InboundMessage) error { calls++; return nil },
	)
	e := eventsAPI(&slackevents.MessageEvent{User: "UBOT", Text: "echo", Channel: "D1", ChannelType: "im", TimeStamp: "1.3"})
	if err := c.dispatchEventsAPI(context.Background(), e); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if calls != 0 {
		t.Errorf("own message reached handler %d times, want 0", calls)
	}
}

func TestDispatch_PropagatesHandlerError(t *testing.T) {
	wantErr := errors.New("db down")
	calls := 0
	c := newTestConnector(
		&fakeBotUsers{byTeam: map[string]string{"T1": "UBOT"}},
		func(_ context.Context, _ channel.InboundMessage) error { calls++; return wantErr },
	)
	e := eventsAPI(&slackevents.MessageEvent{User: "UALICE", Text: "hi", Channel: "D1", ChannelType: "im", TimeStamp: "1.1"})
	if err := c.dispatchEventsAPI(context.Background(), e); !errors.Is(err, wantErr) {
		t.Errorf("dispatch error = %v, want %v (infra error must propagate to Run→reconnect)", err, wantErr)
	}
	if calls != 1 {
		t.Errorf("handler called %d times, want 1", calls)
	}
}

func TestDispatch_UnknownTeamStillDeliversDM(t *testing.T) {
	// A team with no installation resolves bot user id "" — a DM is still
	// delivered (addressed is always true for p2p) and gets dropped downstream
	// at installation resolution; the connector must not crash on the miss.
	var got channel.InboundMessage
	calls := 0
	fb := &fakeBotUsers{byTeam: map[string]string{}} // no team mapped
	c := newTestConnector(fb, func(_ context.Context, m channel.InboundMessage) error { calls++; got = m; return nil })
	e := eventsAPI(&slackevents.MessageEvent{User: "UALICE", Text: "hello", Channel: "D9", ChannelType: "im", TimeStamp: "1.4"})
	if err := c.dispatchEventsAPI(context.Background(), e); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if calls != 1 || !got.AddressedToBot {
		t.Errorf("DM from unknown team should still deliver addressed: calls=%d addressed=%v", calls, got.AddressedToBot)
	}
}

func TestDispatch_BotUserLookupErrorDegradesGracefully(t *testing.T) {
	// A lookup failure must not error the dispatch (which would tear down the
	// shared connection for every workspace); it degrades to no mention id.
	calls := 0
	fb := &fakeBotUsers{err: errors.New("pool exhausted")}
	c := newTestConnector(fb, func(_ context.Context, _ channel.InboundMessage) error { calls++; return nil })
	e := eventsAPI(&slackevents.MessageEvent{User: "UALICE", Text: "hi", Channel: "D1", ChannelType: "im", TimeStamp: "1.5"})
	if err := c.dispatchEventsAPI(context.Background(), e); err != nil {
		t.Fatalf("a bot-user lookup error must not fail dispatch: %v", err)
	}
	if calls != 1 {
		t.Errorf("DM should still deliver despite lookup error: calls=%d", calls)
	}
}

func TestDispatch_IgnoresUnknownInnerEvent(t *testing.T) {
	calls := 0
	c := newTestConnector(&fakeBotUsers{}, func(_ context.Context, _ channel.InboundMessage) error { calls++; return nil })
	// reaction_added etc. are not message/app_mention; dispatch returns nil.
	e := eventsAPI(&slackevents.ReactionAddedEvent{User: "UALICE"})
	if err := c.dispatchEventsAPI(context.Background(), e); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if calls != 0 {
		t.Errorf("unknown inner event reached handler %d times, want 0", calls)
	}
}
