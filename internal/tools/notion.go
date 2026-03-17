package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

const notionBaseURL = "https://api.notion.com/v1"
const notionVersion = "2022-06-28"

func notionGet(ctx context.Context, cfg *config.Config, path string) ([]byte, error) {
	if cfg.NotionToken == "" {
		return nil, fmt.Errorf("Notion not configured: set NOTION_TOKEN")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, notionBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.NotionToken)
	req.Header.Set("Notion-Version", notionVersion)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("Notion auth failed: verify NOTION_TOKEN")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Notion API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func notionPost(ctx context.Context, cfg *config.Config, path string, payload any) ([]byte, error) {
	if cfg.NotionToken == "" {
		return nil, fmt.Errorf("Notion not configured: set NOTION_TOKEN")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, notionBaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.NotionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", notionVersion)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("Notion auth failed: verify NOTION_TOKEN")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Notion API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

// SearchNotion searches Notion for internal collateral matching the query.
func SearchNotion(ctx context.Context, cfg *config.Config, query string) (string, error) {
	payload := map[string]any{
		"query": query,
		"sort": map[string]any{
			"direction": "descending",
			"timestamp": "last_edited_time",
		},
		"page_size": 10,
	}

	body, err := notionPost(ctx, cfg, "/search", payload)
	if err != nil {
		return fmt.Sprintf("Notion search unavailable: %v", err), nil
	}

	var result struct {
		Results []struct {
			ID         string `json:"id"`
			Object     string `json:"object"`
			URL        string `json:"url"`
			Properties map[string]any `json:"properties"`
			Title      []struct {
				PlainText string `json:"plain_text"`
			} `json:"title"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Notion: could not parse search results for '%s'.", query), nil
	}

	if len(result.Results) == 0 {
		return fmt.Sprintf("No Notion pages found for '%s'. Check the internal resource links in the system prompt.", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Notion search results for '%s' (%d found):\n\n", query, len(result.Results)))
	for _, page := range result.Results {
		title := extractNotionTitle(page.Properties, page.Title)
		sb.WriteString(fmt.Sprintf("- **%s**\n  %s\n\n", title, page.URL))
	}
	return sb.String(), nil
}

// GetPageContent fetches the full text content of a Notion page by ID.
// Use when search returns a page URL and the SDR needs the actual content.
func GetPageContent(ctx context.Context, cfg *config.Config, pageID string) (string, error) {
	// Strip hyphens from ID if present
	pageID = strings.ReplaceAll(pageID, "-", "")

	body, err := notionGet(ctx, cfg, "/blocks/"+pageID+"/children")
	if err != nil {
		return fmt.Sprintf("Notion: could not retrieve page %s: %v", pageID, err), nil
	}

	var result struct {
		Results []struct {
			Type      string         `json:"type"`
			Paragraph map[string]any `json:"paragraph"`
			Heading1  map[string]any `json:"heading_1"`
			Heading2  map[string]any `json:"heading_2"`
			Heading3  map[string]any `json:"heading_3"`
			BulletItem map[string]any `json:"bulleted_list_item"`
			NumberItem map[string]any `json:"numbered_list_item"`
			Quote     map[string]any `json:"quote"`
			Callout   map[string]any `json:"callout"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Notion: could not parse page content for %s.", pageID), nil
	}

	var parts []string
	for _, block := range result.Results {
		var richText []any
		switch block.Type {
		case "paragraph":
			richText, _ = block.Paragraph["rich_text"].([]any)
		case "heading_1":
			richText, _ = block.Heading1["rich_text"].([]any)
		case "heading_2":
			richText, _ = block.Heading2["rich_text"].([]any)
		case "heading_3":
			richText, _ = block.Heading3["rich_text"].([]any)
		case "bulleted_list_item":
			richText, _ = block.BulletItem["rich_text"].([]any)
		case "numbered_list_item":
			richText, _ = block.NumberItem["rich_text"].([]any)
		case "quote":
			richText, _ = block.Quote["rich_text"].([]any)
		case "callout":
			richText, _ = block.Callout["rich_text"].([]any)
		}
		var text string
		for _, rt := range richText {
			if m, ok := rt.(map[string]any); ok {
				if pt, ok := m["plain_text"].(string); ok {
					text += pt
				}
			}
		}
		if text != "" {
			parts = append(parts, text)
		}
	}

	if len(parts) == 0 {
		return "Notion page has no readable text content.", nil
	}

	content := strings.Join(parts, "\n")
	if len(content) > 3000 {
		content = content[:3000] + "\n...(truncated)"
	}
	return content, nil
}

func extractNotionTitle(properties map[string]any, fallback []struct{ PlainText string `json:"plain_text"` }) string {
	// Try to get title from properties first
	for _, prop := range properties {
		if m, ok := prop.(map[string]any); ok {
			if propType, ok := m["type"].(string); ok && propType == "title" {
				if titleArr, ok := m["title"].([]any); ok && len(titleArr) > 0 {
					if titleObj, ok := titleArr[0].(map[string]any); ok {
						if pt, ok := titleObj["plain_text"].(string); ok && pt != "" {
							return pt
						}
					}
				}
			}
		}
	}
	// Fallback to title array
	var parts []string
	for _, t := range fallback {
		if t.PlainText != "" {
			parts = append(parts, t.PlainText)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "")
	}
	return "Untitled"
}
