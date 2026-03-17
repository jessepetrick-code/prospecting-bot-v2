package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

const commonRoomBaseURL = "https://api.commonroom.io/community/v1"

// Lead score IDs used in Common Room for ConductorOne (community 10853-conductor-one)
const (
	lsAccountTiering  = "ls_15886" // Account Tiering Q3FY26
	ls3rdPartyIntent  = "ls_9512"  // 3rd Party Intent
	lsWebsiteIntent   = "ls_4732"  // V1 Website Intent
)

// ── REST API helpers ─────────────────────────────────────────────────────────

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

// ── MCP-backed queries ────────────────────────────────────────────────────────

// GetHighIntentAccounts returns prioritized accounts using the Common Room MCP API
// with rich lead score filtering. Falls back to REST API if MCP auth fails.
// states is an optional list of US state abbreviations to filter by (e.g. ["NY","MA","CT"]).
func GetHighIntentAccounts(ctx context.Context, cfg *config.Config, minScore int, limit int, states []string) (string, error) {
	if cfg.CommonRoomAPIKey == "" {
		return "Common Room not configured: set COMMONROOM_API_KEY.", nil
	}
	if limit <= 0 {
		limit = 25
	}

	// Try MCP first — gives us lead score percentile filtering.
	result, err := getHighIntentViaMCP(ctx, cfg, limit, states)
	if err == nil {
		return result, nil
	}

	// MCP failed (likely auth) — fall back to REST.
	return getHighIntentViaREST(ctx, cfg, limit)
}

// getHighIntentViaMCP uses two MCP queries (location-filtered + lead-score-filtered)
// and merges the results, deduplicating by domain.
func getHighIntentViaMCP(ctx context.Context, cfg *config.Config, limit int, states []string) (string, error) {
	mcp, err := newCRMCPClient(ctx, cfg.CommonRoomAPIKey)
	if err != nil {
		return "", err
	}

	seen := map[string]*acct{}

	// Query 1: location-filtered, sorted by member count.
	if len(states) > 0 {
		locIDs := statesToLocationIDs(states)
		if len(locIDs) > 0 {
			args := map[string]any{
				"objectType": "Group",
				"filters": []any{
					map[string]any{
						"field": "groupLocationId",
						"filterType": "stringListFilter",
						"value": locIDs,
					},
					map[string]any{
						"field": "groupEmployeeCount",
						"filterType": "numberRangeFilter",
						"value": map[string]any{"min": 1000, "max": 15000},
					},
				},
				"sort": map[string]any{
					"field":     "memberCount",
					"direction": "DESC",
				},
				"limit": limit,
			}
			text, err := mcp.CallTool(ctx, "commonroom_list_objects", args)
			if err == nil {
				parseGroupsFromMCP(text, "location", seen)
			}
		}
	}

	// Query 2: lead-score sorted, ICP employee range.
	args2 := map[string]any{
		"objectType": "Group",
		"filters": []any{
			map[string]any{
				"field": "groupEmployeeCount",
				"filterType": "numberRangeFilter",
				"value": map[string]any{"min": 1000, "max": 15000},
			},
		},
		"sort": map[string]any{
			"field":     lsAccountTiering,
			"direction": "DESC",
		},
		"limit": limit,
	}
	text2, err := mcp.CallTool(ctx, "commonroom_list_objects", args2)
	if err == nil {
		parseGroupsFromMCP(text2, "intent", seen)
	}

	if len(seen) == 0 {
		return "", fmt.Errorf("no results from MCP queries")
	}

	// Convert map to slice and sort by tiered score desc.
	accts := make([]*acct, 0, len(seen))
	for _, a := range seen {
		accts = append(accts, a)
	}
	sort.Slice(accts, func(i, j int) bool {
		scoreI := accts[i].TieredScore + accts[i].IntentScore + accts[i].WebScore
		scoreJ := accts[j].TieredScore + accts[j].IntentScore + accts[j].WebScore
		return scoreI > scoreJ
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Common Room high-intent accounts (%d results", len(accts)))
	if len(states) > 0 {
		sb.WriteString(fmt.Sprintf(", territory: %s", strings.Join(states, "/")))
	}
	sb.WriteString("):\n\n")

	for _, a := range accts {
		heat := "❄️"
		totalScore := a.TieredScore + a.IntentScore + a.WebScore
		if totalScore > 200 {
			heat = "🔥"
		} else if totalScore > 100 {
			heat = "🌡️"
		}
		location := a.State
		if location == "" {
			location = a.Country
		}
		sb.WriteString(fmt.Sprintf("%s **%s**", heat, a.Name))
		if a.Domain != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", a.Domain))
		}
		if location != "" {
			sb.WriteString(fmt.Sprintf(" | %s", location))
		}
		if a.Employees > 0 {
			sb.WriteString(fmt.Sprintf(" | %d employees", a.Employees))
		}
		sb.WriteString(fmt.Sprintf(" | Tiering: %d | 3P Intent: %d | Web: %d\n",
			a.TieredScore, a.IntentScore, a.WebScore))
	}
	return sb.String(), nil
}

// parseGroupsFromMCP parses the text returned by commonroom_list_objects (Group objects)
// and merges results into the seen map, deduplicating by domain or name.
// The MCP response is JSON embedded in the text field.
func parseGroupsFromMCP(text, source string, seen map[string]*acct) {
	// The MCP tool returns JSON as plain text — extract it.
	// It may be a JSON array or an object with an "objects" or "items" key.
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	// Try to extract JSON block if wrapped in markdown code fences.
	if idx := strings.Index(text, "```json"); idx != -1 {
		end := strings.Index(text[idx+7:], "```")
		if end != -1 {
			text = text[idx+7 : idx+7+end]
		}
	} else if idx := strings.Index(text, "```"); idx != -1 {
		end := strings.Index(text[idx+3:], "```")
		if end != -1 {
			text = text[idx+3 : idx+3+end]
		}
	}

	// Try array first.
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		// Try object wrapper.
		var wrapper struct {
			Objects []json.RawMessage `json:"objects"`
			Items   []json.RawMessage `json:"items"`
			Groups  []json.RawMessage `json:"groups"`
		}
		if err2 := json.Unmarshal([]byte(text), &wrapper); err2 == nil {
			if len(wrapper.Objects) > 0 {
				items = wrapper.Objects
			} else if len(wrapper.Items) > 0 {
				items = wrapper.Items
			} else if len(wrapper.Groups) > 0 {
				items = wrapper.Groups
			}
		}
	}

	for _, raw := range items {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		mergeGroupIntoSeen(obj, source, seen)
	}
}

type acct struct {
	Name        string
	Domain      string
	Employees   int
	Country     string
	State       string
	TieredScore int
	IntentScore int
	WebScore    int
	Source      string
}

func mergeGroupIntoSeen(obj map[string]any, source string, seen map[string]*acct) {
	name, _ := obj["name"].(string)
	if name == "" {
		name, _ = obj["groupName"].(string)
	}
	domain, _ := obj["domain"].(string)
	if domain == "" {
		domain, _ = obj["groupDomain"].(string)
	}
	key := domain
	if key == "" {
		key = strings.ToLower(name)
	}
	if key == "" {
		return
	}

	a, exists := seen[key]
	if !exists {
		a = &acct{Name: name, Domain: domain, Source: source}
		seen[key] = a
	}

	// Employee count
	if emp, ok := getIntField(obj, "groupEmployeeCount", "employeeCount", "employees"); ok {
		if emp > a.Employees {
			a.Employees = emp
		}
	}

	// Location
	if state, ok := obj["groupLocation"].(string); ok && state != "" {
		a.State = state
	}
	if country, ok := obj["groupCountry"].(string); ok && country != "" {
		a.Country = country
	}

	// Lead scores — stored in a nested leadScores map or as top-level keys
	if scores, ok := obj["leadScores"].(map[string]any); ok {
		if v, ok := scores[lsAccountTiering].(map[string]any); ok {
			if p, ok := getIntField(v, "percentile", "value"); ok {
				a.TieredScore = p
			}
		}
		if v, ok := scores[ls3rdPartyIntent].(map[string]any); ok {
			if p, ok := getIntField(v, "percentile", "value"); ok {
				a.IntentScore = p
			}
		}
		if v, ok := scores[lsWebsiteIntent].(map[string]any); ok {
			if p, ok := getIntField(v, "percentile", "value"); ok {
				a.WebScore = p
			}
		}
	}
	// Also check top-level score fields (ls_XXXXX_percentile pattern)
	if v, ok := getIntField(obj, lsAccountTiering+"_percentile"); ok && v > a.TieredScore {
		a.TieredScore = v
	}
	if v, ok := getIntField(obj, ls3rdPartyIntent+"_percentile"); ok && v > a.IntentScore {
		a.IntentScore = v
	}
	if v, ok := getIntField(obj, lsWebsiteIntent+"_percentile"); ok && v > a.WebScore {
		a.WebScore = v
	}
}

func getIntField(obj map[string]any, keys ...string) (int, bool) {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			switch val := v.(type) {
			case float64:
				return int(val), true
			case int:
				return val, true
			case int64:
				return int(val), true
			}
		}
	}
	return 0, false
}

// getHighIntentViaREST is the fallback when MCP is unavailable.
func getHighIntentViaREST(ctx context.Context, cfg *config.Config, limit int) (string, error) {
	path := fmt.Sprintf("/organizations?limit=%d", limit)
	if cfg.CommonRoomCommunityID != "" {
		path += "&communityId=" + cfg.CommonRoomCommunityID
	}

	body, err := crGet(ctx, cfg, path)
	if err != nil {
		return fmt.Sprintf("Common Room: could not retrieve accounts. Error: %v", err), nil
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
		} `json:"organizations"`
		Items []struct {
			Name        string  `json:"name"`
			Domain      string  `json:"domain"`
			IntentScore float64 `json:"intentScore"`
			Score       float64 `json:"score"`
			Employees   int     `json:"employeeCount"`
			Industry    string  `json:"industry"`
			LastSeen    string  `json:"lastActivityAt"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Sprintf("Common Room: could not parse accounts response. Raw: %s", truncate(string(body), 400)), nil
	}

	type org struct {
		Name     string
		Domain   string
		Score    float64
		Emp      int
		Industry string
		LastSeen string
	}
	var orgs []org
	for _, o := range resp.Organizations {
		s := o.IntentScore
		if s == 0 {
			s = o.Score
		}
		orgs = append(orgs, org{o.Name, o.Domain, s, o.Employees, o.Industry, o.LastSeen})
	}
	for _, o := range resp.Items {
		s := o.IntentScore
		if s == 0 {
			s = o.Score
		}
		orgs = append(orgs, org{o.Name, o.Domain, s, o.Employees, o.Industry, o.LastSeen})
	}

	if len(orgs) == 0 {
		return "No Common Room accounts found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Common Room accounts (%d found, via REST API — add COMMONROOM_API_KEY for MCP/lead-score filtering):\n\n", len(orgs)))
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

// ── Account signals ───────────────────────────────────────────────────────────

// GetIntentSignals returns detailed buying signals for a specific account.
func GetIntentSignals(ctx context.Context, cfg *config.Config, companyName string) (string, error) {
	if cfg.CommonRoomAPIKey == "" {
		return "Common Room not configured: set COMMONROOM_API_KEY.", nil
	}

	// Try MCP first.
	if result, err := getSignalsViaMCP(ctx, cfg, companyName); err == nil {
		return result, nil
	}

	// Fall back to REST.
	return getSignalsViaREST(ctx, cfg, companyName)
}

func getSignalsViaMCP(ctx context.Context, cfg *config.Config, companyName string) (string, error) {
	mcp, err := newCRMCPClient(ctx, cfg.CommonRoomAPIKey)
	if err != nil {
		return "", err
	}

	args := map[string]any{
		"objectType": "Group",
		"filters": []any{
			map[string]any{
				"field":      "name",
				"filterType": "stringFilter",
				"value":      companyName,
			},
		},
		"limit": 5,
	}
	text, err := mcp.CallTool(ctx, "commonroom_list_objects", args)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("No Common Room data found for '%s'.", companyName), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Common Room signals for %s:**\n\n", companyName))
	sb.WriteString(text)
	return sb.String(), nil
}

func getSignalsViaREST(ctx context.Context, cfg *config.Config, companyName string) (string, error) {
	payload := map[string]any{
		"query": companyName,
		"limit": 5,
	}
	body, err := crPost(ctx, cfg, "/organizations/search", payload)
	if err != nil {
		return fmt.Sprintf("Common Room: could not retrieve signals for '%s'. Error: %v", companyName, err), nil
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

	heat := "❄️ Cold"
	switch {
	case org.IntentScore >= 82:
		heat = "🔥 Hot"
	case org.IntentScore >= 70:
		heat = "🌡️ Warm"
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
	}
	if len(org.RecentActivity) > 0 {
		sb.WriteString("**Recent Activity:**\n")
		for _, a := range org.RecentActivity {
			sb.WriteString(fmt.Sprintf("- %s: %s (%s)\n", a.MemberName, a.Activity, a.Date))
		}
	}
	return sb.String(), nil
}

// ── Contacts ──────────────────────────────────────────────────────────────────

// GetContactsWithSignals returns engaged contacts at an account.
func GetContactsWithSignals(ctx context.Context, cfg *config.Config, accountName string, limit int) (string, error) {
	if cfg.CommonRoomAPIKey == "" {
		return "Common Room not configured: set COMMONROOM_API_KEY.", nil
	}
	if limit <= 0 {
		limit = 15
	}

	// Try MCP first.
	if result, err := getContactsViaMCP(ctx, cfg, accountName, limit); err == nil {
		return result, nil
	}

	// Fall back to REST.
	return getContactsViaREST(ctx, cfg, accountName, limit)
}

func getContactsViaMCP(ctx context.Context, cfg *config.Config, accountName string, limit int) (string, error) {
	mcp, err := newCRMCPClient(ctx, cfg.CommonRoomAPIKey)
	if err != nil {
		return "", err
	}

	args := map[string]any{
		"objectType": "Member",
		"filters": []any{
			map[string]any{
				"field":      "groupName",
				"filterType": "stringFilter",
				"value":      accountName,
			},
		},
		"sort": map[string]any{
			"field":     "activityCount",
			"direction": "DESC",
		},
		"limit": limit,
	}
	text, err := mcp.CallTool(ctx, "commonroom_list_objects", args)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("No contacts found for '%s' in Common Room.", accountName), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Common Room contacts at %s:**\n\n", accountName))
	sb.WriteString(text)
	return sb.String(), nil
}

func getContactsViaREST(ctx context.Context, cfg *config.Config, accountName string, limit int) (string, error) {
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
			FullName        string `json:"fullName"`
			FirstName       string `json:"firstName"`
			LastName        string `json:"lastName"`
			Title           string `json:"title"`
			JobTitle        string `json:"jobTitle"`
			Email           string `json:"email"`
			LinkedIn        string `json:"linkedinUrl"`
			ActivityCount   int    `json:"activityCount"`
			TotalActivities int    `json:"totalActivities"`
			LastActivity    string `json:"lastActivityAt"`
			LastSeen        string `json:"lastSeen"`
		} `json:"members"`
		Items []struct {
			FullName        string `json:"fullName"`
			FirstName       string `json:"firstName"`
			LastName        string `json:"lastName"`
			Title           string `json:"title"`
			JobTitle        string `json:"jobTitle"`
			Email           string `json:"email"`
			LinkedIn        string `json:"linkedinUrl"`
			ActivityCount   int    `json:"activityCount"`
			TotalActivities int    `json:"totalActivities"`
			LastActivity    string `json:"lastActivityAt"`
			LastSeen        string `json:"lastSeen"`
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
	appendMembers := func(name, title, email, linkedin, lastActivity, lastSeen string, actCount, totalAct int) {
		count := actCount
		if count == 0 {
			count = totalAct
		}
		if count == 0 {
			return
		}
		if name == "" {
			return
		}
		last := lastActivity
		if last == "" {
			last = lastSeen
		}
		engaged = append(engaged, member{name, title, email, linkedin, count, last})
	}

	for _, m := range resp.Members {
		n := m.FullName
		if n == "" {
			n = strings.TrimSpace(m.FirstName + " " + m.LastName)
		}
		t := m.Title
		if t == "" {
			t = m.JobTitle
		}
		appendMembers(n, t, m.Email, m.LinkedIn, m.LastActivity, m.LastSeen, m.ActivityCount, m.TotalActivities)
	}
	for _, m := range resp.Items {
		n := m.FullName
		if n == "" {
			n = strings.TrimSpace(m.FirstName + " " + m.LastName)
		}
		t := m.Title
		if t == "" {
			t = m.JobTitle
		}
		appendMembers(n, t, m.Email, m.LinkedIn, m.LastActivity, m.LastSeen, m.ActivityCount, m.TotalActivities)
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
