package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/conductorone/prospecting-bot/internal/config"
)

const braveSearchURL = "https://api.search.brave.com/res/v1/web/search"

// VerifyEmployeeCount searches the web for a company's current headcount.
// REQUIRED before including any account — ICP range is 1,000–14,900 employees.
// Do not rely on Salesforce employee data alone; it is often outdated.
func VerifyEmployeeCount(ctx context.Context, cfg *config.Config, companyName string) (string, error) {
	query := companyName + " number of employees company size 2024 2025"
	result, err := WebSearch(ctx, cfg, query)
	if err != nil {
		return "", err
	}
	return "**Employee Count Verification for " + companyName + "**\nICP range: 1,000–14,900 employees only.\n\n" + result, nil
}

// SearchCompanyNews searches the web for buying signals: security incidents, compliance prep,
// leadership changes, M&A, job postings for identity/security/IT roles.
func SearchCompanyNews(ctx context.Context, cfg *config.Config, companyName string) (string, error) {
	query := companyName + " security compliance SOC2 hiring identity access management CISO 2025"
	result, err := WebSearch(ctx, cfg, query)
	if err != nil {
		return "", err
	}
	return "**Buying Signal Research for " + companyName + "**\nLooking for: security incidents, compliance (SOC 2, ISO 27001), new CISO/IT leadership, M&A, headcount changes, SaaS/cloud expansion.\n\n" + result, nil
}

// WebSearch calls the Brave Search API to look up public signals.
func WebSearch(ctx context.Context, cfg *config.Config, query string) (string, error) {
	if cfg.BraveSearchAPIKey == "" {
		return "Web search not configured: set BRAVE_SEARCH_API_KEY. Search manually at https://search.brave.com", nil
	}

	endpoint := fmt.Sprintf("%s?q=%s&count=20&freshness=pm6", braveSearchURL, urlEncode(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Subscription-Token", cfg.BraveSearchAPIKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return "Web search auth failed: BRAVE_SEARCH_API_KEY is invalid.", nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return "Web search rate limited. Try again shortly.", nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Web search error %d for query '%s'.", resp.StatusCode, query), nil
	}

	var result struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Web search: could not parse results for '%s'.", query), nil
	}

	if len(result.Web.Results) == 0 {
		return fmt.Sprintf("No web results found for '%s'.", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web search results for '%s':\n\n", query))
	for _, r := range result.Web.Results {
		age := ""
		if r.Age != "" {
			age = " (" + r.Age + ")"
		}
		sb.WriteString(fmt.Sprintf("- **%s**%s\n  %s\n  %s\n\n", r.Title, age, r.Description, r.URL))
	}
	return sb.String(), nil
}
