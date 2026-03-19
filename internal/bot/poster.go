package bot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/slack-go/slack"
)

// postReply sends a threaded reply to a Slack message.
func (b *Bot) postReply(channel, threadTS, text string) {
	slog.Debug("posting reply", "channel", channel, "thread_ts", threadTS, "text_len", len(text))
	_, ts, err := b.client.PostMessage(
		channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		slog.Error("failed to post reply", "channel", channel, "err", err)
		return
	}
	slog.Debug("reply posted", "channel", channel, "ts", ts)
}

// PostScheduled posts a message to the configured channel (used by scheduler).
func (b *Bot) PostScheduled(ctx context.Context, prompt string) error {
	slog.Debug("resolving channel for scheduled post", "configured_channel", b.cfg.SlackChannel)
	channel := b.resolveChannel()
	if channel == "" {
		return fmt.Errorf(
			"could not resolve channel %q", b.cfg.SlackChannel,
		)
	}
	slog.Debug("posting scheduled message", "channel", channel, "prompt_len", len(prompt))

	response, err := b.llm.Process(ctx, prompt, b.registry)
	if err != nil {
		return fmt.Errorf("llm processing failed: %w", err)
	}

	slog.Debug("sending scheduled message to Slack", "channel", channel, "response_len", len(response))
	_, ts, err := b.client.PostMessageContext(
		ctx,
		channel,
		slack.MsgOptionText(response, false),
	)
	if err != nil {
		return fmt.Errorf("failed to post to %s: %w", b.cfg.SlackChannel, err)
	}
	slog.Debug("scheduled message posted", "channel", channel, "ts", ts)
	return nil
}

func (b *Bot) resolveChannel() string {
	if b.channelID != "" {
		slog.Debug("using cached channel ID", "channel_id", b.channelID)
		return b.channelID
	}

	// If it looks like a channel ID already, use it directly
	if len(b.cfg.SlackChannel) > 0 && b.cfg.SlackChannel[0] == 'C' {
		slog.Debug("channel looks like an ID, using directly", "channel_id", b.cfg.SlackChannel)
		b.channelID = b.cfg.SlackChannel
		return b.channelID
	}

	// Look up channel by name
	slog.Debug("looking up channel by name", "channel_name", b.cfg.SlackChannel)
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
	slog.Debug("fetched channel list", "count", len(channels))
	for _, ch := range channels {
		if ch.Name == b.cfg.SlackChannel {
			slog.Debug("resolved channel name to ID", "name", b.cfg.SlackChannel, "id", ch.ID)
			b.channelID = ch.ID
			return b.channelID
		}
	}
	slog.Error("channel not found in list", "channel_name", b.cfg.SlackChannel, "fetched_count", len(channels))
	return ""
}
