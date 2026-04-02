package tools

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

const sfLoginURL = "https://login.salesforce.com/services/oauth2/token"

type sfTokenCache struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

var sfCache sfTokenCache

// getAccessToken returns a valid Salesforce access token.
// If SF_CLIENT_ID + SF_USERNAME + SF_PRIVATE_KEY are set, uses JWT Bearer flow
// and auto-refreshes before expiry. Falls back to SF_ACCESS_TOKEN if JWT vars absent.
func getAccessToken(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg.SFInstanceURL == "" {
		return "", fmt.Errorf("Salesforce not configured: set SF_INSTANCE_URL")
	}

	// JWT Bearer flow takes priority — fully automatic token management.
	if cfg.SFClientID != "" && cfg.SFUsername != "" && cfg.SFPrivateKey != "" {
		return sfCache.get(ctx, cfg)
	}

	// Fall back to static token.
	if cfg.SFAccessToken == "" {
		return "", fmt.Errorf("Salesforce not configured: set SF_ACCESS_TOKEN (or SF_CLIENT_ID + SF_USERNAME + SF_PRIVATE_KEY for auto-refresh)")
	}
	return cfg.SFAccessToken, nil
}

// get returns the cached token, refreshing via JWT Bearer when close to expiry.
func (c *sfTokenCache) get(ctx context.Context, cfg *config.Config) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token == "" || time.Now().Add(5*time.Minute).After(c.expiresAt) {
		token, expiry, err := fetchJWTBearerToken(ctx, cfg)
		if err != nil {
			return "", err
		}
		c.token = token
		c.expiresAt = expiry
	}
	return c.token, nil
}

// fetchJWTBearerToken builds a signed JWT and exchanges it for a Salesforce access token.
func fetchJWTBearerToken(ctx context.Context, cfg *config.Config) (string, time.Time, error) {
	jwt, err := buildSFJWT(cfg)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Salesforce JWT build failed: %w", err)
	}

	data := url.Values{}
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	data.Set("assertion", jwt)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sfLoginURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Salesforce token exchange failed: %w", err)
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
		return "", time.Time{}, fmt.Errorf("Salesforce JWT auth failed (%d): %s — %s", resp.StatusCode, errResp.Error, errResp.ErrorDescription)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("Salesforce: could not parse token response")
	}

	// Salesforce JWT Bearer tokens last ~2 hours; we refresh at 115 minutes.
	return tokenResp.AccessToken, time.Now().Add(115 * time.Minute), nil
}

// buildSFJWT creates a signed RS256 JWT for the Salesforce JWT Bearer flow.
func buildSFJWT(cfg *config.Config) (string, error) {
	// Handle \n literals that result from storing PEM in an env var.
	pemData := strings.ReplaceAll(cfg.SFPrivateKey, `\n`, "\n")
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return "", fmt.Errorf("SF_PRIVATE_KEY is not valid PEM — ensure newlines are encoded as \\n in the env var")
	}

	key, err := parseRSAKey(block.Bytes)
	if err != nil {
		return "", err
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	exp := time.Now().Add(5 * time.Minute).Unix()
	claimsJSON := fmt.Sprintf(`{"iss":%q,"sub":%q,"aud":"https://login.salesforce.com","exp":%d}`,
		cfg.SFClientID, cfg.SFUsername, exp)
	claims := base64.RawURLEncoding.EncodeToString([]byte(claimsJSON))

	signingInput := header + "." + claims
	h := sha256.New()
	h.Write([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("JWT signing failed: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAKey tries PKCS8 first, then PKCS1.
func parseRSAKey(der []byte) (*rsa.PrivateKey, error) {
	if pk, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if rsaKey, ok := pk.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, fmt.Errorf("SF_PRIVATE_KEY: PKCS8 key is not RSA")
	}
	if rsaKey, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return rsaKey, nil
	}
	return nil, fmt.Errorf("SF_PRIVATE_KEY: could not parse key — must be RSA in PKCS8 or PKCS1 PEM format")
}
