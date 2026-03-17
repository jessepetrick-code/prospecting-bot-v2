package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	callbackPort   = "9999"
	redirectURI    = "http://localhost:" + callbackPort + "/callback"
	driveScope     = "https://www.googleapis.com/auth/drive.readonly"
)

// GoogleOAuthFlow runs the full OAuth2 authorization code flow.
// Opens the Google SSO login URL, waits for the SDR to authenticate,
// captures the code via a local callback server, exchanges it for tokens,
// and persists the refresh token to .env.
func GoogleOAuthFlow(cfg *config.Config) error {
	if cfg.GoogleClientID == "" || cfg.GoogleClientSecret == "" {
		return fmt.Errorf("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be set in .env before authenticating")
	}

	// Build the authorization URL
	params := url.Values{}
	params.Set("client_id", cfg.GoogleClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", driveScope)
	params.Set("access_type", "offline")
	params.Set("prompt", "consent") // forces refresh_token to be returned every time
	authURL := googleAuthURL + "?" + params.Encode()

	fmt.Println("\n🔐 Google Drive Authentication")
	fmt.Println("─────────────────────────────────────────────────────")
	fmt.Println("Opening your browser to sign in with Google SSO...")
	fmt.Println("If it doesn't open automatically, paste this URL into your browser:\n")
	fmt.Println("  " + authURL)
	fmt.Println("\nWaiting for you to complete sign-in...")

	// Try to open the browser
	openBrowser(authURL)

	// Start local callback server to capture the auth code
	code, err := waitForAuthCode()
	if err != nil {
		// Fallback: ask the user to paste the code manually
		fmt.Println("\n⚠️  Could not start local callback server.")
		fmt.Println("After signing in, Google will redirect to a page that may show an error.")
		fmt.Println("Copy the 'code' parameter from the URL and paste it here:")
		fmt.Print("> ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			code = strings.TrimSpace(scanner.Text())
			// Handle if they pasted the full URL
			if strings.Contains(code, "code=") {
				parsed, parseErr := url.ParseRequestURI(code)
				if parseErr == nil {
					code = parsed.Query().Get("code")
				}
			}
		}
		if code == "" {
			return fmt.Errorf("no authorization code received")
		}
	}

	// Exchange the authorization code for tokens
	refreshToken, err := exchangeCodeForToken(cfg, code)
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Persist the refresh token to .env
	if err := saveRefreshToken(refreshToken); err != nil {
		// If we can't write to .env, print it for manual entry
		fmt.Printf("\n⚠️  Could not save to .env automatically.\nAdd this to your .env file:\n\nGOOGLE_REFRESH_TOKEN=%s\n", refreshToken)
	} else {
		fmt.Println("\n✅ Google Drive authenticated successfully! Refresh token saved to .env.")
	}

	// Update the in-memory config so the current session works immediately
	cfg.GoogleRefreshToken = refreshToken
	return nil
}

// waitForAuthCode starts a temporary HTTP server and waits for the OAuth callback.
func waitForAuthCode() (string, error) {
	listener, err := net.Listen("tcp", ":"+callbackPort)
	if err != nil {
		return "", fmt.Errorf("could not bind to port %s: %w", callbackPort, err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	server := &http.Server{}
	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		errParam := r.URL.Query().Get("error")
		if errParam != "" {
			fmt.Fprintf(w, "<html><body><h2>❌ Authentication failed: %s</h2><p>You can close this tab.</p></body></html>", errParam)
			errCh <- fmt.Errorf("OAuth error: %s", errParam)
			return
		}
		if code == "" {
			fmt.Fprintf(w, "<html><body><h2>❌ No code received.</h2><p>You can close this tab.</p></body></html>")
			errCh <- fmt.Errorf("no code in callback")
			return
		}
		fmt.Fprintf(w, "<html><body><h2>✅ Signed in successfully!</h2><p>You can close this tab and return to the bot.</p></body></html>")
		codeCh <- code
	})

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait up to 3 minutes for the callback
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		server.Shutdown(shutCtx) //nolint:errcheck
	}()

	select {
	case code := <-codeCh:
		return code, nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", fmt.Errorf("timed out waiting for Google sign-in (3 min)")
	}
}

// exchangeCodeForToken exchanges an authorization code for access + refresh tokens.
func exchangeCodeForToken(cfg *config.Config, code string) (string, error) {
	data := url.Values{}
	data.Set("client_id", cfg.GoogleClientID)
	data.Set("client_secret", cfg.GoogleClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")

	resp, err := http.PostForm(googleTokenURL, data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("could not parse token response: %s", string(body))
	}
	if result.Error != "" {
		return "", fmt.Errorf("%s: %s", result.Error, result.ErrorDesc)
	}
	if result.RefreshToken == "" {
		return "", fmt.Errorf("no refresh token returned — ensure 'prompt=consent' and 'access_type=offline' are set")
	}
	return result.RefreshToken, nil
}

// saveRefreshToken writes GOOGLE_REFRESH_TOKEN into the .env file.
func saveRefreshToken(token string) error {
	envPath := ".env"
	data, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "GOOGLE_REFRESH_TOKEN=") {
			lines[i] = "GOOGLE_REFRESH_TOKEN=" + token
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, "GOOGLE_REFRESH_TOKEN="+token)
	}

	return os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0600)
}

// openBrowser attempts to open the URL in the default browser (best effort).
func openBrowser(authURL string) {
	// Try common browser-open commands across platforms
	cmds := [][]string{
		{"open", authURL},           // macOS
		{"xdg-open", authURL},       // Linux
		{"cmd", "/c", "start", authURL}, // Windows
	}
	for _, args := range cmds {
		if tryExec(args[0], args[1:]...) == nil {
			return
		}
	}
}

func tryExec(name string, args ...string) error {
	return exec.Command(name, args...).Start()
}
