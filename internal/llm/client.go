package llm

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"

	"github.com/conductorone/prospecting-bot/internal/config"
)

// Client wraps the Anthropic SDK client.
type Client struct {
	api *anthropic.Client
}

// New creates a new LLM client backed by AWS Bedrock.
// Auth is sourced from the AWS_BEARER_TOKEN_BEDROCK environment variable.
func New(cfg *config.Config) *Client {
	api := anthropic.NewClient(
		bedrock.WithLoadDefaultConfig(context.Background()),
	)
	return &Client{api: &api}
}
