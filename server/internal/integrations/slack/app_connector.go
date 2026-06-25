package slack

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AppConnector is the inbound half of the multi-tenant B2 Slack model: ONE
// app-level Socket Mode connection for the whole deployment. Multica hosts a
// single Slack app; its app-level token (xapp-…, a deployment-level env var,
// since app-level tokens cannot be minted via OAuth) authorizes a connection
// that receives the Events API stream for EVERY installed workspace. Each
// inbound event carries its team_id, which the engine's Slack ResolverSet maps
// to the right channel_installation (the existing GetChannelInstallationByAppID
// routing, unchanged). This replaces the stage-3 per-installation Socket Mode
// model, where each installation opened its own connection with its own
// app-level token — which does not scale past Slack's ~10-connection cap and
// cannot self-install via OAuth.
//
// The connector does not need leader election. Per the design, "one (or a few)"
// connections are acceptable: each server replica opens its own connection,
// Slack delivers each event to one of them, and the engine's
// (installation, message_id) two-phase dedup guarantees exactly-once
// processing across replicas. Lifecycle mirrors a channel: Run blocks running
// the receive loop and reconnects under backoff until its context is cancelled.
type AppConnector struct {
	appToken string
	handler  channel.InboundHandler
	botUsers BotUserLookup
	logger   *slog.Logger

	minBackoff        time.Duration
	maxBackoff        time.Duration
	resetBackoffAfter time.Duration
	now               func() time.Time

	// reCache memoizes the per-bot @-mention regexp so a hot channel does not
	// recompile it on every event. Keyed by bot user id; bounded by the number
	// of installed workspaces.
	reMu    sync.Mutex
	reCache map[string]*regexp.Regexp
}

// BotUserLookup resolves the installed bot's Slack user id for an inbound
// team_id. The shared connection serves every workspace, and each workspace has
// its own bot user id, so the connector resolves it per event to strip / detect
// @-mentions of the bot. A team with no active installation resolves to "" (the
// event is then dropped at installation resolution by the Router).
type BotUserLookup interface {
	BotUserID(ctx context.Context, teamID string) (string, error)
}

// AppConnectorConfig configures the connector. AppToken, Handler and BotUsers
// are required; the rest default.
type AppConnectorConfig struct {
	// AppToken is the deployment-level app-level token (xapp-…) authorizing the
	// Socket Mode connection.
	AppToken string
	// Handler is the engine's shared inbound pipeline (channelRouter.Handle).
	Handler channel.InboundHandler
	// BotUsers resolves a team_id to its installed bot user id.
	BotUsers BotUserLookup
	Logger   *slog.Logger

	// Reconnect backoff tuning; zero values take production defaults.
	MinBackoff        time.Duration
	MaxBackoff        time.Duration
	ResetBackoffAfter time.Duration
	// Now is injected for tests; production uses time.Now.
	Now func() time.Time
}

// NewAppConnector builds the connector with production defaults applied.
func NewAppConnector(cfg AppConnectorConfig) *AppConnector {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = 2 * time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 60 * time.Second
	}
	if cfg.ResetBackoffAfter == 0 {
		cfg.ResetBackoffAfter = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &AppConnector{
		appToken:          cfg.AppToken,
		handler:           cfg.Handler,
		botUsers:          cfg.BotUsers,
		logger:            logger,
		minBackoff:        cfg.MinBackoff,
		maxBackoff:        cfg.MaxBackoff,
		resetBackoffAfter: cfg.ResetBackoffAfter,
		now:               cfg.Now,
		reCache:           make(map[string]*regexp.Regexp),
	}
}

// Run drives the single Socket Mode connection, reconnecting under exponential
// backoff until ctx is cancelled. It blocks; start it in a goroutine and cancel
// ctx to stop. There is no lease to release on stop (the connector owns no
// per-installation state), so the caller need not join it for correctness.
func (c *AppConnector) Run(ctx context.Context) {
	backoff := c.minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		startedAt := c.now()
		err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		// A connection that lived "long enough" resets the backoff, so a single
		// late drop does not start the next attempt at the cap.
		if c.now().Sub(startedAt) >= c.resetBackoffAfter {
			backoff = c.minBackoff
		}
		if err != nil {
			c.logger.Warn("slack connector: connection exited with error", "error", err)
		} else {
			c.logger.Info("slack connector: connection closed")
		}
		if sleepCtx(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, c.maxBackoff)
	}
}

// connectOnce opens one Socket Mode connection and runs its receive loop until
// the connection drops or ctx is cancelled. It mirrors the stage-3
// per-installation Connect, but the connection is app-level (no bot token) and
// every event is routed by team_id rather than belonging to one installation.
func (c *AppConnector) connectOnce(ctx context.Context) error {
	if c.handler == nil {
		return errors.New("slack: inbound handler not configured")
	}
	if c.appToken == "" {
		return errors.New("slack: app-level token not configured")
	}
	// The Socket Mode connection authenticates with the app-level token alone;
	// no bot token is needed (outbound replies carry their own per-installation
	// bot token).
	api := slack.New("", slack.OptionAppLevelToken(c.appToken))
	sm := socketmode.New(api)

	// Each connection runs under its OWN cancellable context, not the parent
	// ctx directly. Every exit path (handler error, event-stream close, ctx
	// cancellation) cancels runCtx and waits for the run goroutine to observe it
	// and exit — so a transient handler error tears the live connection down
	// before Run reconnects. Without this, the old Socket Mode goroutine would
	// keep running on the long-lived ctx, leaking the connection/goroutine and
	// consuming events into an unread channel while a second connection opens.
	runCtx, runCancel := context.WithCancel(ctx)
	runErr := make(chan error, 1) // buffered: the goroutine sends once and exits even if nobody reads
	done := make(chan struct{})
	go func() {
		runErr <- sm.RunContext(runCtx)
		close(done)
	}()
	defer func() {
		runCancel()
		<-done
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-runErr:
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return err
			}
			return errors.New("slack: socket mode connection closed")
		case evt, ok := <-sm.Events:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("slack: socket mode event stream closed")
			}
			if err := c.handleSocketEvent(ctx, sm, evt); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func (c *AppConnector) handleSocketEvent(ctx context.Context, sm *socketmode.Client, evt socketmode.Event) error {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return nil
		}
		// ACK first: Slack expires un-ACKed envelopes in ~3s, far below the
		// handler's DB work. The ACK is independent of the handler outcome — a
		// handler error is surfaced to Run (reconnect/backoff), not retried
		// through the un-ACK path.
		if evt.Request != nil {
			if err := sm.Ack(*evt.Request); err != nil {
				c.logger.WarnContext(ctx, "slack: ack failed", "error", err)
			}
		}
		return c.dispatchEventsAPI(ctx, eventsAPI)
	case socketmode.EventTypeConnecting, socketmode.EventTypeConnected, socketmode.EventTypeHello:
		c.logger.DebugContext(ctx, "slack: socket mode", "event", evt.Type)
	case socketmode.EventTypeIncomingError, socketmode.EventTypeErrorBadMessage:
		c.logger.WarnContext(ctx, "slack: socket mode error", "event", evt.Type)
	default:
		// Interactive / slash-command / other envelopes are intentionally
		// ignored (ACK so Slack does not retry). In particular, /issue is NOT a
		// registered Slack slash command — the hosted app requests no `commands`
		// scope, so Slack never routes a slash-command envelope here. Issue
		// creation runs through the normal message path instead: `@bot /issue
		// <title>` in a channel (the mention is stripped, leaving "/issue …") or
		// `/issue <title>` in a DM with the bot, which the engine's
		// ParseIssueCommand picks up. Adding native slash-command support
		// (scope + registration + response_url handling) is a possible later
		// enhancement, not required for the message-driven /issue flow.
		if evt.Request != nil {
			_ = sm.Ack(*evt.Request)
		}
	}
	return nil
}

// dispatchEventsAPI translates one Events API envelope to a normalized inbound
// message and hands it to the engine. team_id drives both the routing (carried
// in Raw) and the @-mention identity (the installed bot's user id). A non-nil
// handler error is an infrastructure failure; it is propagated so Run reconnects
// (InboundHandler contract). A legitimate product drop returns nil.
func (c *AppConnector) dispatchEventsAPI(ctx context.Context, e slackevents.EventsAPIEvent) error {
	botUserID := c.lookupBotUser(ctx, e.TeamID)
	mentionRe := c.mentionRe(botUserID)

	var (
		msg channel.InboundMessage
		ok  bool
	)
	switch inner := e.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		msg, ok = inboundFromAppMention(e, inner, botUserID, mentionRe)
	case *slackevents.MessageEvent:
		msg, ok = inboundFromMessage(e, inner, botUserID, mentionRe)
	default:
		return nil
	}
	if !ok {
		return nil
	}
	return c.handler(ctx, msg)
}

// lookupBotUser resolves the bot user id for a team, returning "" on any miss or
// error so dispatch proceeds (an un-routable event is dropped at installation
// resolution; mention detection is a safe no-op without the id).
func (c *AppConnector) lookupBotUser(ctx context.Context, teamID string) string {
	if c.botUsers == nil || teamID == "" {
		return ""
	}
	id, err := c.botUsers.BotUserID(ctx, teamID)
	if err != nil {
		c.logger.WarnContext(ctx, "slack: bot user lookup failed", "team_id", teamID, "error", err)
		return ""
	}
	return id
}

func (c *AppConnector) mentionRe(botUserID string) *regexp.Regexp {
	if botUserID == "" {
		return nil
	}
	c.reMu.Lock()
	defer c.reMu.Unlock()
	if re, ok := c.reCache[botUserID]; ok {
		return re
	}
	re := compileMentionRe(botUserID)
	c.reCache[botUserID] = re
	return re
}

// ---- bot user lookup (DB-backed) ----

// botUserQueries is the slice of generated queries the installation-backed bot
// user lookup needs. *db.Queries satisfies it.
type botUserQueries interface {
	GetChannelInstallationByAppID(ctx context.Context, arg db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
}

type installationBotUserLookup struct{ q botUserQueries }

// NewInstallationBotUserLookup resolves a team's bot user id from its
// channel_installation config via the existing app_id routing query — no new
// query, no schema change.
func NewInstallationBotUserLookup(q botUserQueries) BotUserLookup {
	return &installationBotUserLookup{q: q}
}

func (l *installationBotUserLookup) BotUserID(ctx context.Context, teamID string) (string, error) {
	inst, err := l.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypeSlack),
		AppID:       teamID, // Slack team_id is stored in the routing-key slot
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil // team not installed (or revoked between events)
		}
		return "", err
	}
	return botUserIDFromConfig(inst.Config)
}

// ---- small ctx-aware helpers (local to avoid depending on engine internals) ----

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns true iff ctx was
// cancelled before the sleep completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() != nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
