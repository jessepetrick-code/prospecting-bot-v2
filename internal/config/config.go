package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all environment-based configuration for the bot.
type Config struct {
	// Slack
	SlackBotToken string
	SlackAppToken string
	SlackChannel  string

	// AWS Bedrock
	AWSBedrockBearerToken string
	AWSRegion             string

	// Salesforce
	SFInstanceURL string
	SFAccessToken string
	SFClientID    string
	SFUsername    string
	SFPrivateKey  string

	// Common Room
	CommonRoomAPIKey     string
	CommonRoomCommunityID string

	// Lusha
	LushaAPIKey string

	// Apollo.io
	ApolloAPIKey string

	// Google Drive (OAuth2)
	GoogleClientID       string
	GoogleClientSecret   string
	GoogleRefreshToken   string
	GoogleDriveFolderID  string

	// Gong
	GongAccessKey       string
	GongAccessKeySecret string

	// Notion
	NotionToken string

	// Brave Search
	BraveSearchAPIKey string

	// Scheduler
	ScheduleCron string
}

// Load reads configuration from environment variables (and optional .env file).
func Load() (*Config, error) {
	// Best-effort .env load; ignore error if file not present (production uses real env vars)
	_ = godotenv.Load()

	cfg := &Config{
		SlackBotToken:       os.Getenv("SLACK_BOT_TOKEN"),
		SlackAppToken:       os.Getenv("SLACK_APP_TOKEN"),
		SlackChannel:        os.Getenv("SLACK_CHANNEL"),
		AWSBedrockBearerToken: os.Getenv("AWS_BEARER_TOKEN_BEDROCK"),
		AWSRegion:             os.Getenv("AWS_REGION"),
		SFInstanceURL:         os.Getenv("SF_INSTANCE_URL"),
		SFAccessToken:         os.Getenv("SF_ACCESS_TOKEN"),
		SFClientID:            os.Getenv("SF_CLIENT_ID"),
		SFUsername:            os.Getenv("SF_USERNAME"),
		SFPrivateKey:          os.Getenv("SF_PRIVATE_KEY"),
		CommonRoomAPIKey:      os.Getenv("COMMONROOM_API_KEY"),
		CommonRoomCommunityID: os.Getenv("COMMONROOM_COMMUNITY_ID"),
		LushaAPIKey:         os.Getenv("LUSHA_API_KEY"),
		ApolloAPIKey:        os.Getenv("APOLLO_API_KEY"),
		GoogleClientID:      os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:  os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRefreshToken:  os.Getenv("GOOGLE_REFRESH_TOKEN"),
		GoogleDriveFolderID: os.Getenv("GOOGLE_DRIVE_FOLDER_ID"),
		GongAccessKey:       os.Getenv("GONG_ACCESS_KEY"),
		GongAccessKeySecret: os.Getenv("GONG_ACCESS_KEY_SECRET"),
		NotionToken:         os.Getenv("NOTION_TOKEN"),
		BraveSearchAPIKey:   os.Getenv("BRAVE_SEARCH_API_KEY"),
		ScheduleCron:        os.Getenv("SCHEDULE_CRON"),
	}

	if cfg.ScheduleCron == "" {
		cfg.ScheduleCron = "0 6 * * 1-5"
	}
	if cfg.SlackChannel == "" {
		cfg.SlackChannel = "ai-prospecting-v2"
	}
	if cfg.AWSRegion == "" {
		cfg.AWSRegion = "us-west-2"
	}

	required := []struct{ name, value string }{
		{"SLACK_BOT_TOKEN", cfg.SlackBotToken},
		{"SLACK_APP_TOKEN", cfg.SlackAppToken},
		{"AWS_BEARER_TOKEN_BEDROCK", cfg.AWSBedrockBearerToken},
	}
	for _, r := range required {
		if r.value == "" {
			return nil, fmt.Errorf("missing required env var: %s", r.name)
		}
	}

	return cfg, nil
}

// LoadPartial loads config without requiring Slack tokens — used by CLI mode.
// Only AWS_BEARER_TOKEN_BEDROCK and AWS_REGION are required; all other keys are optional.
func LoadPartial() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		SlackBotToken:         os.Getenv("SLACK_BOT_TOKEN"),
		SlackAppToken:         os.Getenv("SLACK_APP_TOKEN"),
		SlackChannel:          os.Getenv("SLACK_CHANNEL"),
		AWSBedrockBearerToken: os.Getenv("AWS_BEARER_TOKEN_BEDROCK"),
		AWSRegion:             os.Getenv("AWS_REGION"),
		SFInstanceURL:         os.Getenv("SF_INSTANCE_URL"),
		SFAccessToken:         os.Getenv("SF_ACCESS_TOKEN"),
		SFClientID:            os.Getenv("SF_CLIENT_ID"),
		SFUsername:            os.Getenv("SF_USERNAME"),
		SFPrivateKey:          os.Getenv("SF_PRIVATE_KEY"),
		CommonRoomAPIKey:      os.Getenv("COMMONROOM_API_KEY"),
		CommonRoomCommunityID: os.Getenv("COMMONROOM_COMMUNITY_ID"),
		LushaAPIKey:           os.Getenv("LUSHA_API_KEY"),
		ApolloAPIKey:          os.Getenv("APOLLO_API_KEY"),
		GoogleClientID:        os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:    os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRefreshToken:    os.Getenv("GOOGLE_REFRESH_TOKEN"),
		GoogleDriveFolderID:   os.Getenv("GOOGLE_DRIVE_FOLDER_ID"),
		GongAccessKey:         os.Getenv("GONG_ACCESS_KEY"),
		GongAccessKeySecret:   os.Getenv("GONG_ACCESS_KEY_SECRET"),
		NotionToken:           os.Getenv("NOTION_TOKEN"),
		BraveSearchAPIKey:     os.Getenv("BRAVE_SEARCH_API_KEY"),
		ScheduleCron:          os.Getenv("SCHEDULE_CRON"),
	}

	if cfg.ScheduleCron == "" {
		cfg.ScheduleCron = "0 6 * * 1-5"
	}
	if cfg.SlackChannel == "" {
		cfg.SlackChannel = "ai-prospecting-v2"
	}
	if cfg.AWSRegion == "" {
		cfg.AWSRegion = "us-west-2"
	}

	if cfg.AWSBedrockBearerToken == "" {
		return nil, fmt.Errorf("missing required env var: AWS_BEARER_TOKEN_BEDROCK")
	}

	return cfg, nil
}
