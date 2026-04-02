package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

// sfBaseURL returns a sanitized Salesforce instance base URL (ensures https:// prefix, no trailing slash).
// Strips any existing scheme first to handle typos like "ttps://" or missing "h".
func sfBaseURL(cfg *config.Config) string {
	base := strings.TrimRight(cfg.SFInstanceURL, "/")
	if idx := strings.Index(base, "://"); idx != -1 {
		base = base[idx+3:]
	}
	return "https://" + base
}

// sfTokenURL returns the OAuth token endpoint for this org.
// Salesforce orgs with My Domain enabled require using the My Domain URL,
// not login.salesforce.com — otherwise you get "request not supported on this domain".
func sfTokenURL(cfg *config.Config) string {
	return sfBaseURL(cfg) + "/services/oauth2/token"
}

type sfTokenCache struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

var sfCache sfTokenCache

// getAccessToken returns a valid Salesforce access token.
// Uses OAuth 2.0 Client Credentials flow (Consumer Key + Secret) if configured,
// otherwise falls back to the static SF_ACCESS_TOKEN.
func getAccessToken(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg.SFInstanceURL == "" {
		return "", fmt.Errorf("Salesforce not configured: set SF_INSTANCE_URL")
	}

	// Client Credentials flow — auto-refreshing, no expiry management needed.
	if cfg.SFClientID != "" && cfg.SFClientSecret != "" {
		return sfCache.get(ctx, cfg)
	}

	// Fall back to static token.
	if cfg.SFAccessToken == "" {
		return "", fmt.Errorf("Salesforce not configured: set SF_CLIENT_ID + SF_CLIENT_SECRET (or SF_ACCESS_TOKEN as fallback)")
	}
	return cfg.SFAccessToken, nil
}

// get returns the cached token, refreshing via Client Credentials when close to expiry.
func (c *sfTokenCache) get(ctx context.Context, cfg *config.Config) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token == "" || time.Now().Add(5*time.Minute).After(c.expiresAt) {
		token, expiry, err := fetchClientCredentialsToken(ctx, cfg)
		if err != nil {
			return "", err
		}
		c.token = token
		c.expiresAt = expiry
	}
	return c.token, nil
}

// fetchClientCredentialsToken exchanges Consumer Key + Secret for an access token.
func fetchClientCredentialsToken(ctx context.Context, cfg *config.Config) (string, time.Time, error) {
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", cfg.SFClientID)
	data.Set("client_secret", cfg.SFClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sfTokenURL(cfg), strings.NewReader(data.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Salesforce token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errResp)
		return "", time.Time{}, fmt.Errorf("Salesforce auth failed (%d): %s — %s", resp.StatusCode, errResp.Error, errResp.ErrorDescription)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("Salesforce: could not parse token response: %s", truncate(string(body), 200))
	}

	// Salesforce access tokens last ~2 hours; refresh at 115 minutes.
	return tokenResp.AccessToken, time.Now().Add(115 * time.Minute), nil
}
