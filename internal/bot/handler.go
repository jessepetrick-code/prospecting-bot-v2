package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	client    *slack.Client
	socket    *socketmode.Client
	llm       *llm.Client
	registry  *tools.Registry
	cfg       *config.Config
	channelID string
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
	return b.socket.RunContext(ctx)
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
			default:
				slog.Debug("unhandled event type, acking", "type", evt.Type)
				// Acknowledge other events to prevent retries.
				if evt.Request != nil {
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
