package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/conductorone/prospecting-bot/internal/config"
	"github.com/conductorone/prospecting-bot/internal/llm"
	"github.com/conductorone/prospecting-bot/internal/tools"
)

// Bot handles Slack events and dispatches them through the LLM pipeline.
type Bot struct {
	client        *slack.Client
	socket        *socketmode.Client
	llm           *llm.Client
	registry      *tools.Registry
	cfg           *config.Config
	channelID     string
	activeThreads sync.Map // key: "channel:threadTS", value: time.Time
}

// New creates a Bot connected to Slack via Socket Mode.
func New(cfg *config.Config) *Bot {
	api := slack.New(
		cfg.SlackBotToken,
		slack.OptionAppLevelToken(cfg.SlackAppToken),
	)

	sm := socketmode.New(api)

	return &Bot{
		client:   api,
		socket:   sm,
		llm:      llm.New(cfg),
		registry: tools.New(cfg),
		cfg:      cfg,
	}
}

// Run starts the Socket Mode event loop (blocks until ctx is cancelled).
func (b *Bot) Run(ctx context.Context) error {
	go b.handleEvents(ctx)
	go b.cleanupThreads(ctx)
	return b.socket.RunContext(ctx)
}

// cleanupThreads periodically removes active thread entries older than 24 hours.
func (b *Bot) cleanupThreads(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-24 * time.Hour)
			b.activeThreads.Range(func(key, value any) bool {
				if t, ok := value.(time.Time); ok && t.Before(cutoff) {
					b.activeThreads.Delete(key)
				}
				return true
			})
		}
	}
}

func (b *Bot) handleEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-b.socket.Events:
			if !ok {
				slog.Debug("socket events channel closed")
				return
			}
			slog.Debug("socket event received", "type", evt.Type)
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					slog.Debug("failed to cast event data to EventsAPIEvent", "data_type", fmt.Sprintf("%T", evt.Data))
					continue
				}
				slog.Debug("events API event",
					"api_type", eventsAPI.Type,
					"inner_event_type", eventsAPI.InnerEvent.Type,
				)
				b.socket.Ack(*evt.Request)

				if ev, ok := eventsAPI.InnerEvent.Data.(*slackevents.AppMentionEvent); ok {
					slog.Debug("dispatching app mention", "user", ev.User, "channel", ev.Channel)
					go b.handleMention(ev)
				} else if ev, ok := eventsAPI.InnerEvent.Data.(*slackevents.MessageEvent); ok {
					go b.handleMessage(ev)
				} else {
					slog.Debug("inner event is not AppMentionEvent",
						"inner_type", eventsAPI.InnerEvent.Type,
						"data_type", fmt.Sprintf("%T", eventsAPI.InnerEvent.Data),
					)
				}
			case socketmode.EventTypeConnecting:
				slog.Debug("socket mode connecting")
			case socketmode.EventTypeConnected:
				slog.Debug("socket mode connected")
			case socketmode.EventTypeDisconnect:
				slog.Debug("socket mode disconnected")
			case socketmode.EventTypeHello:
				slog.Debug("socket mode hello")
			default:
				slog.Debug("unhandled event type", "type", evt.Type)
				if evt.Request != nil && evt.Request.EnvelopeID != "" {
					b.socket.Ack(*evt.Request)
				}
			}
		}
	}
}

func (b *Bot) handleMention(ev *slackevents.AppMentionEvent) {
	slog.Debug("handling mention", "raw_text", ev.Text, "user", ev.User, "channel", ev.Channel, "ts", ev.TimeStamp)
	text := stripMention(ev.Text)
	if text == "" {
		slog.Debug("mention text empty after stripping, ignoring")
		return
	}

	slog.Info("mention received",
		"user", ev.User,
		"channel", ev.Channel,
		"text", text,
	)

	// Acknowledge receipt immediately so the user knows the bot is working.
	if err := b.client.AddReaction("eyes", slack.ItemRef{
		Channel:   ev.Channel,
		Timestamp: ev.TimeStamp,
	}); err != nil {
		slog.Warn("failed to add reaction", "err", err)
	}

	// Prepend the Slack user's real name so the LLM can scope to their territory automatically.
	if userInfo, err := b.client.GetUserInfo(ev.User); err == nil {
		name := userInfo.Profile.RealName
		if name == "" {
			name = userInfo.Profile.DisplayName
		}
		if name != "" {
			text = fmt.Sprintf("[SDR: %s] %s", name, text)
		}
	} else {
		slog.Warn("failed to look up Slack user profile", "user", ev.User, "err", err)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(), 3*time.Minute,
	)
	defer cancel()

	response, err := b.llm.Process(ctx, text, b.registry)

	// Remove the in-progress reaction regardless of outcome.
	if rerr := b.client.RemoveReaction("eyes", slack.ItemRef{
		Channel:   ev.Channel,
		Timestamp: ev.TimeStamp,
	}); rerr != nil {
		slog.Warn("failed to remove reaction", "err", rerr)
	}

	if err != nil {
		slog.Error("llm processing failed", "err", err)
		b.postReply(ev.Channel, ev.TimeStamp,
			fmt.Sprintf("Sorry, I hit an error: %v", err))
		return
	}

	b.postReply(ev.Channel, ev.TimeStamp, response)

	// Record this thread as active so follow-up messages don't need @mention.
	threadTS := ev.TimeStamp
	if ev.ThreadTimeStamp != "" {
		threadTS = ev.ThreadTimeStamp
	}
	b.activeThreads.Store(ev.Channel+":"+threadTS, time.Now())
}

// handleMessage processes messages in threads where the bot has already replied.
// Messages that @mention the bot are ignored here — they're handled by handleMention.
func (b *Bot) handleMessage(ev *slackevents.MessageEvent) {
	// Ignore bot messages (including our own replies).
	if ev.BotID != "" {
		return
	}
	// Only process thread replies (must have thread_ts set).
	if ev.ThreadTimeStamp == "" {
		return
	}
	// If the message contains an @mention, app_mention will handle it.
	if strings.Contains(ev.Text, "<@") {
		return
	}

	threadKey := ev.Channel + ":" + ev.ThreadTimeStamp
	if _, active := b.activeThreads.Load(threadKey); !active {
		return
	}

	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return
	}

	slog.Info("thread follow-up received",
		"user", ev.User,
		"channel", ev.Channel,
		"thread_ts", ev.ThreadTimeStamp,
		"text", text,
	)

	// Prepend the Slack user's real name so the LLM can scope to their territory automatically.
	if userInfo, err := b.client.GetUserInfo(ev.User); err == nil {
		name := userInfo.Profile.RealName
		if name == "" {
			name = userInfo.Profile.DisplayName
		}
		if name != "" {
			text = fmt.Sprintf("[SDR: %s] %s", name, text)
		}
	} else {
		slog.Warn("failed to look up Slack user profile", "user", ev.User, "err", err)
	}

	// Acknowledge receipt with eyes reaction.
	if err := b.client.AddReaction("eyes", slack.ItemRef{
		Channel:   ev.Channel,
		Timestamp: ev.TimeStamp,
	}); err != nil {
		slog.Warn("failed to add reaction", "err", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	response, err := b.llm.Process(ctx, text, b.registry)

	if rerr := b.client.RemoveReaction("eyes", slack.ItemRef{
		Channel:   ev.Channel,
		Timestamp: ev.TimeStamp,
	}); rerr != nil {
		slog.Warn("failed to remove reaction", "err", rerr)
	}

	if err != nil {
		slog.Error("llm processing failed", "err", err)
		b.postReply(ev.Channel, ev.ThreadTimeStamp, fmt.Sprintf("Sorry, I hit an error: %v", err))
		return
	}

	b.postReply(ev.Channel, ev.ThreadTimeStamp, response)

	// Refresh the thread's TTL.
	b.activeThreads.Store(threadKey, time.Now())
}

func stripMention(text string) string {
	// Slack mentions look like <@U12345> at the start
	if idx := strings.Index(text, "> "); idx != -1 {
		return strings.TrimSpace(text[idx+2:])
	}
	if idx := strings.Index(text, ">"); idx != -1 {
		return strings.TrimSpace(text[idx+1:])
	}
	return strings.TrimSpace(text)
}
