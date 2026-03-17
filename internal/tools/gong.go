package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

const gongBaseURL = "https://api.gong.io/v2"

type gongParty struct {
	Name        string `json:"name"`
	Affiliation string `json:"affiliation"` // "Internal" or "External"
	Title       string `json:"title"`
}

type gongCall struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Started    string      `json:"started"`
	DurationMs int64       `json:"duration"`
	Parties    []gongParty `json:"parties"`
}

func gongGet(ctx context.Context, cfg *config.Config, path string) ([]byte, error) {
	if cfg.GongAccessKey == "" || cfg.GongAccessKeySecret == "" {
		return nil, fmt.Errorf("Gong not configured: set GONG_ACCESS_KEY and GONG_ACCESS_KEY_SECRET")
	}
	creds := base64.StdEncoding.EncodeToString([]byte(cfg.GongAccessKey + ":" + cfg.GongAccessKeySecret))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gongBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+creds)

	client := &http.Client{Timeout: 20 * time.Second}
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
		return nil, fmt.Errorf("Gong auth failed: verify GONG_ACCESS_KEY and GONG_ACCESS_KEY_SECRET")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gong API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

// ListGongCalls searches Gong for recent calls associated with a company.
// NOTE: The Gong API has known pagination and truncation issues (~45KB limit).
// For detailed transcript analysis, ask the SDR to paste transcript text directly.
func ListGongCalls(ctx context.Context, cfg *config.Config, companyName string, daysBack int) (string, error) {
	since := time.Now().AddDate(0, 0, -daysBack).Format(time.RFC3339)
	path := fmt.Sprintf("/calls?fromDateTime=%s&limit=20", since)

	body, err := gongGet(ctx, cfg, path)
	if err != nil {
		return fmt.Sprintf("⚠️ Gong API unavailable: %v\n\nWorkaround: Ask the SDR to paste relevant Gong transcript text directly into chat for analysis.", err), nil
	}

	var result struct {
		Calls   []gongCall `json:"calls"`
		Records struct {
			TotalRecords int `json:"totalRecords"`
		} `json:"records"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "⚠️ Gong: could not parse call list. Workaround: SDR can paste Gong transcript text directly for analysis.", nil
	}

	// Filter by company name (Gong doesn't support account-level filtering in basic API)
	company := strings.ToLower(companyName)
	var matched []gongCall
	for _, call := range result.Calls {
		if strings.Contains(strings.ToLower(call.Title), company) {
			matched = append(matched, call)
			continue
		}
		// Check if any external party name matches
		for _, p := range call.Parties {
			if p.Affiliation == "External" && strings.Contains(strings.ToLower(p.Name), company) {
				matched = append(matched, call)
				break
			}
		}
	}

	if len(matched) == 0 {
		return fmt.Sprintf("📞 Gong: no calls found for '%s' in the last %d days.\n\n⚠️ Note: Gong API search is limited — results may be incomplete. SDRs can search Gong directly or paste transcript text for analysis.", companyName, daysBack), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📞 Gong calls for '%s' (%d found, last %d days):\n\n", companyName, len(matched), daysBack))
	sb.WriteString("⚠️ Note: Gong API has pagination limits (~45KB). For full transcript analysis, paste transcript text directly into chat.\n\n")

	for _, call := range matched {
		duration := time.Duration(call.DurationMs) * time.Millisecond
		sb.WriteString(fmt.Sprintf("- **%s**\n", call.Title))
		sb.WriteString(fmt.Sprintf("  Date: %s | Duration: %s\n", call.Started, duration.Round(time.Minute)))
		var externals []string
		for _, p := range call.Parties {
			if p.Affiliation == "External" {
				externals = append(externals, p.Name+" ("+p.Title+")")
			}
		}
		if len(externals) > 0 {
			sb.WriteString(fmt.Sprintf("  Participants: %s\n", strings.Join(externals, ", ")))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}
