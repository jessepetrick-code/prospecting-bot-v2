package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/conductorone/prospecting-bot/internal/tools"
)

const (
	// Bedrock cross-region inference profile for Claude Opus 4.6.
	model     = "us.anthropic.claude-opus-4-6-v1"
	maxTokens = int64(4096)
	maxIter   = 20 // safety cap on agentic loop iterations
)

// Process runs the agentic tool-use loop for a user message and returns the final text response.
func (c *Client) Process(ctx context.Context, userMsg string, reg *tools.Registry) (string, error) {
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(userMsg)),
	}

	anthropicTools := reg.AnthropicTools()

	// System prompt as a TextBlockParam slice
	system := []anthropic.TextBlockParam{
		{Text: SystemPrompt},
	}

	var finalText strings.Builder

	for i := 0; i < maxIter; i++ {
		resp, err := c.api.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     model,
			MaxTokens: maxTokens,
			System:    system,
			Messages:  messages,
			Tools:     anthropicTools,
		})
		if err != nil {
			return "", fmt.Errorf("claude api error: %w", err)
		}

		// Append assistant turn to conversation history
		messages = append(messages, resp.ToParam())

		// Process content blocks
		var toolResults []anthropic.ContentBlockParamUnion
		finalText.Reset() // only keep text from the most recent turn

		for _, block := range resp.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				finalText.WriteString(v.Text)

			case anthropic.ToolUseBlock:
				slog.Info("tool call", "tool", v.Name, "id", block.ID)

				rawInput := json.RawMessage(v.JSON.Input.Raw())
				result, toolErr := reg.Execute(ctx, v.Name, rawInput)
				if toolErr != nil {
					slog.Warn("tool error", "tool", v.Name, "err", toolErr)
					toolResults = append(toolResults,
						anthropic.NewToolResultBlock(block.ID, fmt.Sprintf("Error: %v", toolErr), true))
				} else {
					toolResults = append(toolResults,
						anthropic.NewToolResultBlock(block.ID, result, false))
				}
			}
		}

		// If no tool calls, we're done
		if resp.StopReason != anthropic.StopReasonToolUse {
			break
		}

		// Feed tool results back
		messages = append(messages, anthropic.NewUserMessage(toolResults...))
	}

	return finalText.String(), nil
}
