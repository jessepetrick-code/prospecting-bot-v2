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
func New(cfg *config.Config) (*Bot, error) {
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
	}, nil
}

// Run starts the Socket Mode event loop (blocks until disconnected).
func (b *Bot) Run() error {
	go b.handleEvents()
	return b.socket.Run()
}

func (b *Bot) handleEvents() {
	for evt := range b.socket.Events {
		switch evt.Type {
		case socketmode.EventTypeEventsAPI:
			eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			b.socket.Ack(*evt.Request)

			if ev, ok := eventsAPI.InnerEvent.Data.(*slackevents.AppMentionEvent); ok {
				go b.handleMention(ev)
			}
		default:
			// Acknowledge other events to prevent retries
			if evt.Request != nil {
				b.socket.Ack(*evt.Request)
			}
		}
	}
}

func (b *Bot) handleMention(ev *slackevents.AppMentionEvent) {
	text := stripMention(ev.Text)
	if text == "" {
		return
	}

	slog.Info("mention received",
		"user", ev.User,
		"channel", ev.Channel,
		"text", text,
	)

	ctx, cancel := context.WithTimeout(
		context.Background(), 3*time.Minute,
	)
	defer cancel()

	response, err := b.llm.Process(ctx, text, b.registry)
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
