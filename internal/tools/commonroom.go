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

const commonRoomBaseURL = "https://api.commonroom.io/community/v1"

func crGet(ctx context.Context, cfg *config.Config, path string) ([]byte, error) {
	if cfg.CommonRoomAPIKey == "" {
		return nil, fmt.Errorf("Common Room not configured: set COMMONROOM_API_KEY")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, commonRoomBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.CommonRoomAPIKey)
	req.Header.Set("Content-Type", "application/json")

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
		return nil, fmt.Errorf("Common Room auth failed: verify COMMONROOM_API_KEY")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Common Room API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func crPost(ctx context.Context, cfg *config.Config, path string, payload any) ([]byte, error) {
	if cfg.CommonRoomAPIKey == "" {
		return nil, fmt.Errorf("Common Room not configured: set COMMONROOM_API_KEY")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, commonRoomBaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.CommonRoomAPIKey)
	req.Header.Set("Content-Type", "application/json")

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
		return nil, fmt.Errorf("Common Room auth failed: verify COMMONROOM_API_KEY")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("Common Room API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

// GetHighIntentAccounts returns all accounts from Common Room with intent score >= minScore.
// This is the PRIMARY tool for building a prospecting list — call before Salesforce.
func GetHighIntentAccounts(ctx context.Context, cfg *config.Config, minScore int, limit int) (string, error) {
	if cfg.CommonRoomAPIKey == "" {
		return "Common Room not configured: set COMMONROOM_API_KEY.", nil
	}
	if minScore <= 0 {
		minScore = 70
	}
	if limit <= 0 {
		limit = 20
	}

	path := fmt.Sprintf("/organizations?limit=%d", limit)
	if cfg.CommonRoomCommunityID != "" {
		path += "&communityId=" + cfg.CommonRoomCommunityID
	}

	body, err := crGet(ctx, cfg, path)
	if err != nil {
		return fmt.Sprintf("Common Room: could not retrieve high-intent accounts. Error: %v", err), nil
	}

	var resp struct {
		Organizations []struct {
			Name        string  `json:"name"`
			Domain      string  `json:"domain"`
			IntentScore float64 `json:"intentScore"`
			Score       float64 `json:"score"`
			Employees   int     `json:"employeeCount"`
			Industry    string  `json:"industry"`
			LastSeen    string  `json:"lastActivityAt"`
			Signals     []any   `json:"recentSignals"`
		} `json:"organizations"`
		Items []struct {
			Name        string  `json:"name"`
			Domain      string  `json:"domain"`
			IntentScore float64 `json:"intentScore"`
			Score       float64 `json:"score"`
			Employees   int     `json:"employeeCount"`
			Industry    string  `json:"industry"`
			LastSeen    string  `json:"lastActivityAt"`
			Signals     []any   `json:"recentSignals"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Sprintf("Common Room: could not parse high-intent accounts response. Raw: %s", truncate(string(body), 500)), nil
	}

	type org struct {
		Name     string
		Domain   string
		Score    float64
		Emp      int
		Industry string
		LastSeen string
		Signals  int
	}
	var orgs []org
	for _, o := range resp.Organizations {
		s := o.IntentScore
		if s == 0 {
			s = o.Score
		}
		if s >= float64(minScore) {
			orgs = append(orgs, org{o.Name, o.Domain, s, o.Employees, o.Industry, o.LastSeen, len(o.Signals)})
		}
	}
	for _, o := range resp.Items {
		s := o.IntentScore
		if s == 0 {
			s = o.Score
		}
		if s >= float64(minScore) {
			orgs = append(orgs, org{o.Name, o.Domain, s, o.Employees, o.Industry, o.LastSeen, len(o.Signals)})
		}
	}

	if len(orgs) == 0 {
		return fmt.Sprintf("No Common Room accounts found with intent score >= %d.", minScore), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Common Room high-intent accounts (score >= %d): %d found\n\n", minScore, len(orgs)))
	for _, o := range orgs {
		heat := "❄️"
		if o.Score >= 82 {
			heat = "🔥"
		} else if o.Score >= 70 {
			heat = "🌡️"
		}
		sb.WriteString(fmt.Sprintf("%s **%s** (score: %.0f) — %s | %d employees | Last: %s\n",
			heat, o.Name, o.Score, o.Domain, o.Emp, o.LastSeen))
	}
	return sb.String(), nil
}

// GetContactsWithSignals returns contacts at an account who have actual engagement signals.
// Only returns contacts with real activity — do NOT recommend contacts with zero activity.
func GetContactsWithSignals(ctx context.Context, cfg *config.Config, accountName string, limit int) (string, error) {
	if cfg.CommonRoomAPIKey == "" {
		return "Common Room not configured: set COMMONROOM_API_KEY.", nil
	}
	if limit <= 0 {
		limit = 15
	}

	path := fmt.Sprintf("/members?organizationName=%s&limit=%d", urlEncode(accountName), limit)
	if cfg.CommonRoomCommunityID != "" {
		path += "&communityId=" + cfg.CommonRoomCommunityID
	}

	body, err := crGet(ctx, cfg, path)
	if err != nil {
		return fmt.Sprintf("Common Room: could not retrieve contacts for '%s'. Error: %v", accountName, err), nil
	}

	var resp struct {
		Members []struct {
			FullName       string  `json:"fullName"`
			FirstName      string  `json:"firstName"`
			LastName       string  `json:"lastName"`
			Title          string  `json:"title"`
			JobTitle       string  `json:"jobTitle"`
			Email          string  `json:"email"`
			LinkedIn       string  `json:"linkedinUrl"`
			ActivityCount  int     `json:"activityCount"`
			TotalActivities int    `json:"totalActivities"`
			LastActivity   string  `json:"lastActivityAt"`
			LastSeen       string  `json:"lastSeen"`
			Organization   string  `json:"organization"`
		} `json:"members"`
		Items []struct {
			FullName       string  `json:"fullName"`
			FirstName      string  `json:"firstName"`
			LastName       string  `json:"lastName"`
			Title          string  `json:"title"`
			JobTitle       string  `json:"jobTitle"`
			Email          string  `json:"email"`
			LinkedIn       string  `json:"linkedinUrl"`
			ActivityCount  int     `json:"activityCount"`
			TotalActivities int    `json:"totalActivities"`
			LastActivity   string  `json:"lastActivityAt"`
			LastSeen       string  `json:"lastSeen"`
			Organization   string  `json:"organization"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Sprintf("Common Room: could not parse members for '%s'. Raw: %s", accountName, truncate(string(body), 300)), nil
	}

	type member struct {
		Name     string
		Title    string
		Email    string
		LinkedIn string
		Activity int
		LastSeen string
	}
	var engaged []member
	for _, m := range resp.Members {
		count := m.ActivityCount
		if count == 0 {
			count = m.TotalActivities
		}
		if count > 0 {
			name := m.FullName
			if name == "" {
				name = strings.TrimSpace(m.FirstName + " " + m.LastName)
			}
			title := m.Title
			if title == "" {
				title = m.JobTitle
			}
			last := m.LastActivity
			if last == "" {
				last = m.LastSeen
			}
			engaged = append(engaged, member{name, title, m.Email, m.LinkedIn, count, last})
		}
	}
	for _, m := range resp.Items {
		count := m.ActivityCount
		if count == 0 {
			count = m.TotalActivities
		}
		if count > 0 {
			name := m.FullName
			if name == "" {
				name = strings.TrimSpace(m.FirstName + " " + m.LastName)
			}
			title := m.Title
			if title == "" {
				title = m.JobTitle
			}
			last := m.LastActivity
			if last == "" {
				last = m.LastSeen
			}
			engaged = append(engaged, member{name, title, m.Email, m.LinkedIn, count, last})
		}
	}

	if len(engaged) == 0 {
		return fmt.Sprintf("No contacts with engagement signals found for '%s' in Common Room.", accountName), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Common Room engaged contacts at '%s' (%d with activity):\n\n", accountName, len(engaged)))
	for _, m := range engaged {
		sb.WriteString(fmt.Sprintf("- **%s** — %s\n", m.Name, m.Title))
		sb.WriteString(fmt.Sprintf("  Activity: %d signals | Last: %s\n", m.Activity, m.LastSeen))
		if m.Email != "" {
			sb.WriteString(fmt.Sprintf("  Email: %s\n", m.Email))
		}
		if m.LinkedIn != "" {
			sb.WriteString(fmt.Sprintf("  LinkedIn: %s\n", m.LinkedIn))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// GetIntentSignals queries Common Room for intent signals and account activity.
func GetIntentSignals(ctx context.Context, cfg *config.Config, companyName string) (string, error) {
	// Search for the organization in Common Room
	payload := map[string]any{
		"query": companyName,
		"limit": 5,
	}
	body, err := crPost(ctx, cfg, "/organizations/search", payload)
	if err != nil {
		return fmt.Sprintf("Common Room: could not retrieve intent signals for '%s'. Error: %v", companyName, err), nil
	}

	var searchResp struct {
		Organizations []struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Domain      string  `json:"domain"`
			IntentScore float64 `json:"intentScore"`
			Employees   int     `json:"employees"`
			Signals     []struct {
				Type      string `json:"type"`
				Source    string `json:"source"`
				CreatedAt string `json:"createdAt"`
				Details   string `json:"details"`
			} `json:"signals"`
			RecentActivity []struct {
				MemberName string `json:"memberName"`
				Activity   string `json:"activity"`
				Date       string `json:"date"`
			} `json:"recentActivity"`
		} `json:"organizations"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return fmt.Sprintf("Common Room: received response for '%s' but could not parse it. Raw: %s", companyName, truncate(string(body), 500)), nil
	}

	if len(searchResp.Organizations) == 0 {
		return fmt.Sprintf("Common Room: no data found for '%s'. They may not be in your Common Room community.", companyName), nil
	}

	org := searchResp.Organizations[0]
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Common Room data for %s (matched: %s)**\n\n", companyName, org.Name))
	sb.WriteString(fmt.Sprintf("📊 Intent Score: **%.0f/100**\n", org.IntentScore))

	var heat string
	switch {
	case org.IntentScore >= 82:
		heat = "🔥 Hot"
	case org.IntentScore >= 70:
		heat = "🌡️ Warm"
	default:
		heat = "❄️ Cold"
	}
	sb.WriteString(fmt.Sprintf("Signal Strength: %s\n", heat))
	if org.Employees > 0 {
		sb.WriteString(fmt.Sprintf("Employees: %d\n", org.Employees))
	}
	sb.WriteString("\n")

	if len(org.Signals) > 0 {
		sb.WriteString("**Recent Signals:**\n")
		for _, s := range org.Signals {
			sb.WriteString(fmt.Sprintf("- %s (%s) — %s: %s\n", s.Type, s.Source, s.CreatedAt, s.Details))
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("No recent signals found in Common Room.\n\n")
	}

	if len(org.RecentActivity) > 0 {
		sb.WriteString("**Recent Activity:**\n")
		for _, a := range org.RecentActivity {
			sb.WriteString(fmt.Sprintf("- %s: %s (%s)\n", a.MemberName, a.Activity, a.Date))
		}
	}

	return sb.String(), nil
}
