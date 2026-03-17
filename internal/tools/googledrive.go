package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

const (
	googleTokenRefreshURL = "https://oauth2.googleapis.com/token"
	googleDriveAPIURL     = "https://www.googleapis.com/drive/v3"
)

// getGoogleAccessToken exchanges the stored refresh token for a short-lived access token.
func getGoogleAccessToken(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg.GoogleRefreshToken == "" {
		return "", fmt.Errorf("not authenticated")
	}

	data := url.Values{}
	data.Set("client_id", cfg.GoogleClientID)
	data.Set("client_secret", cfg.GoogleClientSecret)
	data.Set("refresh_token", cfg.GoogleRefreshToken)
	data.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenRefreshURL,
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("token refresh failed: %s", truncate(string(body), 200))
	}
	if result.Error != "" {
		return "", fmt.Errorf("%s: %s", result.Error, result.ErrorDesc)
	}
	return result.AccessToken, nil
}

func googleNotReady(cfg *config.Config) string {
	if cfg.GoogleClientID == "" || cfg.GoogleClientSecret == "" {
		return "Google Drive not configured: set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET in .env."
	}
	if cfg.GoogleRefreshToken == "" {
		return "Google Drive: not signed in. Type 'auth google' to sign in with your Google account."
	}
	return ""
}

// driveGet performs an authenticated GET request to the Drive API.
func driveGet(ctx context.Context, token, endpoint string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

// getAllFolderIDs returns the root folder ID plus all descendant folder IDs recursively.
// This lets us scope searches to the entire folder tree.
func getAllFolderIDs(ctx context.Context, token, rootID string) []string {
	ids := []string{rootID}
	queue := []string{rootID}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		q := fmt.Sprintf("'%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false", current)
		endpoint := fmt.Sprintf("%s/files?q=%s&fields=files(id)&pageSize=100",
			googleDriveAPIURL, url.QueryEscape(q))

		body, status, err := driveGet(ctx, token, endpoint)
		if err != nil || status != http.StatusOK {
			continue
		}

		var result struct {
			Files []struct {
				ID string `json:"id"`
			} `json:"files"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}
		for _, f := range result.Files {
			ids = append(ids, f.ID)
			queue = append(queue, f.ID)
		}
	}
	return ids
}

// SearchGoogleDrive searches within the configured collateral folder (and all subfolders).
// Falls back to all of Drive if no folder is configured.
func SearchGoogleDrive(ctx context.Context, cfg *config.Config, query string, limit int) (string, error) {
	if msg := googleNotReady(cfg); msg != "" {
		return msg, nil
	}

	token, err := getGoogleAccessToken(ctx, cfg)
	if err != nil {
		return fmt.Sprintf("Google Drive auth error: %v. Type 'auth google' to re-authenticate.", err), nil
	}

	if limit <= 0 {
		limit = 15
	}

	safeQuery := strings.ReplaceAll(query, "'", `\'`)

	var q string
	folderNote := ""

	if cfg.GoogleDriveFolderID != "" {
		// Collect all folder IDs in the tree so we can search recursively
		folderIDs := getAllFolderIDs(ctx, token, cfg.GoogleDriveFolderID)

		// Build: ('id1' in parents or 'id2' in parents or ...)
		parentClauses := make([]string, 0, len(folderIDs))
		for _, id := range folderIDs {
			parentClauses = append(parentClauses, fmt.Sprintf("'%s' in parents", id))
		}
		parentFilter := "(" + strings.Join(parentClauses, " or ") + ")"
		q = fmt.Sprintf("fullText contains '%s' and %s and trashed = false", safeQuery, parentFilter)
		folderNote = " (scoped to collateral folder)"
	} else {
		q = fmt.Sprintf("fullText contains '%s' and trashed = false", safeQuery)
	}

	endpoint := fmt.Sprintf("%s/files?q=%s&pageSize=%d&fields=files(id,name,mimeType,modifiedTime,webViewLink)&orderBy=modifiedTime+desc",
		googleDriveAPIURL, url.QueryEscape(q), limit)

	body, status, err := driveGet(ctx, token, endpoint)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		return "Google Drive: session expired. Type 'auth google' to re-authenticate.", nil
	}
	if status != http.StatusOK {
		return fmt.Sprintf("Google Drive search error %d for '%s': %s", status, query, truncate(string(body), 200)), nil
	}

	var result struct {
		Files []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			MimeType     string `json:"mimeType"`
			ModifiedTime string `json:"modifiedTime"`
			WebViewLink  string `json:"webViewLink"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Google Drive: could not parse results for '%s'.", query), nil
	}

	if len(result.Files) == 0 {
		return fmt.Sprintf("No Google Drive files found for '%s'%s.", query, folderNote), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Google Drive results for '%s'%s (%d found):\n\n", query, folderNote, len(result.Files)))
	for _, f := range result.Files {
		modDate := ""
		if len(f.ModifiedTime) >= 10 {
			modDate = f.ModifiedTime[:10]
		}
		sb.WriteString(fmt.Sprintf("- **%s** (%s)\n  ID: %s\n  Modified: %s\n  Link: %s\n\n",
			f.Name, simplifyMimeType(f.MimeType), f.ID, modDate, f.WebViewLink))
	}
	return sb.String(), nil
}

type driveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	ModifiedTime string `json:"modifiedTime"`
	WebViewLink  string `json:"webViewLink"`
}

// listFolderContents returns all files (non-folders) directly inside a folder.
func listFolderContents(ctx context.Context, token, folderID string) ([]driveFile, error) {
	q := fmt.Sprintf("'%s' in parents and mimeType != 'application/vnd.google-apps.folder' and trashed = false", folderID)
	endpoint := fmt.Sprintf("%s/files?q=%s&pageSize=100&fields=files(id,name,mimeType,modifiedTime,webViewLink)&orderBy=name",
		googleDriveAPIURL, url.QueryEscape(q))

	body, status, err := driveGet(ctx, token, endpoint)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("could not list folder %s (status %d)", folderID, status)
	}

	var result struct {
		Files []driveFile `json:"files"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result.Files, nil
}

// listSubfolders returns immediate subfolders of a folder.
func listSubfolders(ctx context.Context, token, folderID string) ([]driveFile, error) {
	q := fmt.Sprintf("'%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false", folderID)
	endpoint := fmt.Sprintf("%s/files?q=%s&pageSize=100&fields=files(id,name,mimeType,webViewLink)&orderBy=name",
		googleDriveAPIURL, url.QueryEscape(q))

	body, status, err := driveGet(ctx, token, endpoint)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("could not list subfolders of %s", folderID)
	}

	var result struct {
		Files []driveFile `json:"files"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result.Files, nil
}

// ListCollateralFolder returns the full directory tree of the configured collateral folder.
// Use this to discover what collateral exists before recommending specific docs.
func ListCollateralFolder(ctx context.Context, cfg *config.Config) (string, error) {
	if msg := googleNotReady(cfg); msg != "" {
		return msg, nil
	}
	if cfg.GoogleDriveFolderID == "" {
		return "No collateral folder configured (GOOGLE_DRIVE_FOLDER_ID not set).", nil
	}

	token, err := getGoogleAccessToken(ctx, cfg)
	if err != nil {
		return fmt.Sprintf("Google Drive auth error: %v. Type 'auth google' to re-authenticate.", err), nil
	}

	var sb strings.Builder
	sb.WriteString("**ConductorOne Collateral Folder — Google Drive Index:**\n\n")

	var walkFolder func(folderID, indent string) error
	walkFolder = func(folderID, indent string) error {
		// List files in this folder
		files, err := listFolderContents(ctx, token, folderID)
		if err != nil {
			sb.WriteString(indent + "(error listing folder)\n")
			return nil
		}
		for _, f := range files {
			modDate := ""
			if len(f.ModifiedTime) >= 10 {
				modDate = " (" + f.ModifiedTime[:10] + ")"
			}
			sb.WriteString(fmt.Sprintf("%s📄 **%s** [%s]%s\n%s   ID: %s\n%s   Link: %s\n",
				indent, f.Name, simplifyMimeType(f.MimeType), modDate,
				indent, f.ID,
				indent, f.WebViewLink))
		}

		// Recurse into subfolders
		subfolders, err := listSubfolders(ctx, token, folderID)
		if err != nil {
			return nil
		}
		for _, sf := range subfolders {
			sb.WriteString(fmt.Sprintf("\n%s📁 **%s/**\n", indent, sf.Name))
			walkFolder(sf.ID, indent+"  ") //nolint:errcheck
		}
		return nil
	}

	if err := walkFolder(cfg.GoogleDriveFolderID, ""); err != nil {
		return fmt.Sprintf("Google Drive: error listing collateral folder: %v", err), nil
	}

	result := sb.String()
	if result == "**ConductorOne Collateral Folder — Google Drive Index:**\n\n" {
		return "The collateral folder appears to be empty or inaccessible. Make sure the folder is shared with your Google account.", nil
	}
	return result, nil
}

// ReadGoogleDriveFile reads the text content of a Google Doc, Sheet, Slides, or PDF.
func ReadGoogleDriveFile(ctx context.Context, cfg *config.Config, fileID string) (string, error) {
	if msg := googleNotReady(cfg); msg != "" {
		return msg, nil
	}

	token, err := getGoogleAccessToken(ctx, cfg)
	if err != nil {
		return fmt.Sprintf("Google Drive auth error: %v. Type 'auth google' to re-authenticate.", err), nil
	}

	// Fetch metadata first to determine file type
	metaURL := fmt.Sprintf("%s/files/%s?fields=id,name,mimeType", googleDriveAPIURL, fileID)
	metaBody, status, err := driveGet(ctx, token, metaURL)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return fmt.Sprintf("Google Drive: file %s not found or you don't have access.", fileID), nil
	}
	if status != http.StatusOK {
		return fmt.Sprintf("Google Drive: could not access file %s (error %d).", fileID, status), nil
	}

	var meta struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
	}
	json.Unmarshal(metaBody, &meta) //nolint:errcheck

	// Choose export format by mime type
	var contentURL string
	switch meta.MimeType {
	case "application/vnd.google-apps.document":
		contentURL = fmt.Sprintf("%s/files/%s/export?mimeType=text/plain", googleDriveAPIURL, fileID)
	case "application/vnd.google-apps.spreadsheet":
		contentURL = fmt.Sprintf("%s/files/%s/export?mimeType=text/csv", googleDriveAPIURL, fileID)
	case "application/vnd.google-apps.presentation":
		contentURL = fmt.Sprintf("%s/files/%s/export?mimeType=text/plain", googleDriveAPIURL, fileID)
	default:
		return fmt.Sprintf("**%s** (Google Drive — %s)\nID: %s\nNote: Binary file — open directly in Drive to read: https://drive.google.com/file/d/%s",
			meta.Name, simplifyMimeType(meta.MimeType), meta.ID, meta.ID), nil
	}

	contentBody, status, err := driveGet(ctx, token, contentURL)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return fmt.Sprintf("Google Drive: could not read content of '%s'.", meta.Name), nil
	}

	text := string(contentBody)
	if len(text) > 4000 {
		text = text[:4000] + "\n...(truncated — use the Drive link to read the full document)"
	}

	return fmt.Sprintf("**%s** (Google Drive):\n\n%s", meta.Name, text), nil
}

func simplifyMimeType(mime string) string {
	switch mime {
	case "application/vnd.google-apps.document":
		return "Google Doc"
	case "application/vnd.google-apps.spreadsheet":
		return "Google Sheet"
	case "application/vnd.google-apps.presentation":
		return "Google Slides"
	case "application/pdf":
		return "PDF"
	case "application/vnd.google-apps.folder":
		return "Folder"
	default:
		parts := strings.Split(mime, "/")
		if len(parts) > 1 {
			return parts[1]
		}
		return mime
	}
}
