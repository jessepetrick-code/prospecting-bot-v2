package llm

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/conductorone/prospecting-bot/internal/config"
)

// Client wraps the Anthropic SDK client.
type Client struct {
	api *anthropic.Client
}

// New creates a new LLM client using the API key from config.
func New(cfg *config.Config) *Client {
	api := anthropic.NewClient(option.WithAPIKey(cfg.AnthropicAPIKey))
	return &Client{api: &api}
}
