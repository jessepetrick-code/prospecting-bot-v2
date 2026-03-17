package bot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/slack-go/slack"
)

// postReply sends a threaded reply to a Slack message.
func (b *Bot) postReply(channel, threadTS, text string) {
	_, _, err := b.client.PostMessage(
		channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		slog.Error("failed to post reply", "channel", channel, "err", err)
	}
}

// PostScheduled posts a message to the configured channel (used by scheduler).
func (b *Bot) PostScheduled(ctx context.Context, prompt string) error {
	channel := b.resolveChannel()
	if channel == "" {
		return fmt.Errorf(
			"could not resolve channel %q", b.cfg.SlackChannel,
		)
	}

	response, err := b.llm.Process(ctx, prompt, b.registry)
	if err != nil {
		return fmt.Errorf("llm processing failed: %w", err)
	}

	_, _, err = b.client.PostMessageContext(
		ctx,
		channel,
		slack.MsgOptionText(response, false),
	)
	if err != nil {
		return fmt.Errorf("failed to post to %s: %w", b.cfg.SlackChannel, err)
	}
	return nil
}

func (b *Bot) resolveChannel() string {
	if b.channelID != "" {
		return b.channelID
	}

	// If it looks like a channel ID already, use it directly
	if len(b.cfg.SlackChannel) > 0 && b.cfg.SlackChannel[0] == 'C' {
		b.channelID = b.cfg.SlackChannel
		return b.channelID
	}

	// Look up channel by name
	params := &slack.GetConversationsParameters{
		Types:           []string{"public_channel", "private_channel"},
		Limit:           200,
		ExcludeArchived: true,
	}
	channels, _, err := b.client.GetConversations(params)
	if err != nil {
		slog.Error("failed to list channels", "err", err)
		return ""
	}
	for _, ch := range channels {
		if ch.Name == b.cfg.SlackChannel {
			b.channelID = ch.ID
			return b.channelID
		}
	}
	return ""
}
