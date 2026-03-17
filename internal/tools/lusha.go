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

const lushaBaseURL = "https://api.lusha.com"

// EnrichContact calls the Lusha API to find email and phone for a contact.
// domain is optional but improves match accuracy (e.g. "stripe.com").
func EnrichContact(ctx context.Context, cfg *config.Config, firstName, lastName, company, linkedInURL string) (string, error) {
	return enrichContactFull(ctx, cfg, firstName, lastName, company, linkedInURL, "")
}

// EnrichContactWithDomain enriches a contact with an optional company domain for better accuracy.
func EnrichContactWithDomain(ctx context.Context, cfg *config.Config, firstName, lastName, company, domain, linkedInURL string) (string, error) {
	return enrichContactFull(ctx, cfg, firstName, lastName, company, linkedInURL, domain)
}

func enrichContactFull(ctx context.Context, cfg *config.Config, firstName, lastName, company, linkedInURL, domain string) (string, error) {
	if cfg.LushaAPIKey == "" {
		return "Lusha not configured: set LUSHA_API_KEY. Ask the SDR to enrich contacts manually via the Lusha browser extension.", nil
	}

	// Lusha person endpoint
	endpoint := fmt.Sprintf("%s/person?firstName=%s&lastName=%s&company=%s", lushaBaseURL,
		urlEncode(firstName), urlEncode(lastName), urlEncode(company))
	if domain != "" {
		endpoint += "&companyDomain=" + urlEncode(domain)
	}
	if linkedInURL != "" {
		endpoint += "&linkedInUrl=" + urlEncode(linkedInURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("api_key", cfg.LushaAPIKey)

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
		return "Lusha auth failed: LUSHA_API_KEY is invalid or expired.", nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Sprintf("Lusha: no contact data found for %s %s at %s. Try Sales Navigator for manual lookup.", firstName, lastName, company), nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Lusha API error %d for %s %s. Try Sales Navigator.", resp.StatusCode, firstName, lastName), nil
	}

	var result struct {
		Data struct {
			Emails []struct {
				Email     string `json:"email"`
				EmailType string `json:"emailType"`
				IsValid   bool   `json:"isValid"`
			} `json:"emails"`
			Phones []struct {
				InternationalNumber string `json:"internationalNumber"`
				LocalNumber         string `json:"localNumber"`
				PhoneType           string `json:"phoneType"`
			} `json:"phones"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Lusha: could not parse response for %s %s. Raw: %s", firstName, lastName, truncate(string(body), 200)), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Lusha enrichment for %s %s at %s:**\n", firstName, lastName, company))

	if len(result.Data.Emails) > 0 {
		sb.WriteString("Emails:\n")
		for _, e := range result.Data.Emails {
			valid := ""
			if e.IsValid {
				valid = " ✓"
			}
			sb.WriteString(fmt.Sprintf("  - %s (%s)%s\n", e.Email, e.EmailType, valid))
		}
	} else {
		sb.WriteString("No emails found in Lusha.\n")
	}

	if len(result.Data.Phones) > 0 {
		sb.WriteString("Phones:\n")
		for _, p := range result.Data.Phones {
			num := p.InternationalNumber
			if num == "" {
				num = p.LocalNumber
			}
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", num, p.PhoneType))
		}
	} else {
		sb.WriteString("No phone numbers found in Lusha.\n")
	}

	return sb.String(), nil
}

// BulkEnrichContacts enriches up to 5 contacts at once.
// contacts is a JSON array of objects with first_name, last_name, company, domain (optional).
func BulkEnrichContacts(ctx context.Context, cfg *config.Config, contacts []map[string]string) (string, error) {
	if len(contacts) > 5 {
		contacts = contacts[:5]
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Lusha bulk enrichment (%d contacts):\n\n", len(contacts)))
	for _, c := range contacts {
		result, err := enrichContactFull(ctx, cfg,
			c["first_name"], c["last_name"], c["company"],
			c["linkedin_url"], c["domain"],
		)
		if err != nil {
			result = fmt.Sprintf("Error: %v", err)
		}
		sb.WriteString(result)
		sb.WriteString("\n---\n")
	}
	return sb.String(), nil
}

// EnrichCompanyLusha calls the Lusha company API to get firmographic data.
// Returns employee count, industry, revenue, HQ location, and tech stack.
// Use to verify ICP fit and supplement Salesforce account data.
func EnrichCompanyLusha(ctx context.Context, cfg *config.Config, companyName, domain string) (string, error) {
	if cfg.LushaAPIKey == "" {
		return "Lusha not configured: set LUSHA_API_KEY.", nil
	}

	endpoint := fmt.Sprintf("%s/company?name=%s", lushaBaseURL, urlEncode(companyName))
	if domain != "" {
		endpoint = fmt.Sprintf("%s/company?domain=%s", lushaBaseURL, urlEncode(domain))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("api_key", cfg.LushaAPIKey)

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
		return "Lusha auth failed: LUSHA_API_KEY is invalid or expired.", nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Sprintf("Lusha: no company data found for '%s'. Try web search to verify headcount.", companyName), nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Lusha company API error %d for '%s'.", resp.StatusCode, companyName), nil
	}

	var result struct {
		Data struct {
			Name        string   `json:"name"`
			Domain      string   `json:"domain"`
			Employees   int      `json:"numberOfEmployees"`
			Industry    string   `json:"industry"`
			Revenue     string   `json:"revenue"`
			Founded     int      `json:"founded"`
			Description string   `json:"description"`
			HQ          struct {
				City    string `json:"city"`
				State   string `json:"state"`
				Country string `json:"country"`
			} `json:"headquarters"`
			Technologies []string `json:"technologies"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Lusha: could not parse company response for '%s'. Raw: %s", companyName, truncate(string(body), 200)), nil
	}

	d := result.Data
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Lusha company data for %s:**\n", companyName))

	if d.Name != "" {
		sb.WriteString(fmt.Sprintf("Company: %s\n", d.Name))
	}
	if d.Domain != "" {
		sb.WriteString(fmt.Sprintf("Domain: %s\n", d.Domain))
	}
	if d.Employees > 0 {
		icpNote := ""
		if d.Employees < 1000 {
			icpNote = " ⚠️ Below ICP minimum (1,000)"
		} else if d.Employees > 14900 {
			icpNote = " ⚠️ Above ICP maximum (14,900)"
		} else {
			icpNote = " ✅ In ICP range"
		}
		sb.WriteString(fmt.Sprintf("Employees: %d%s\n", d.Employees, icpNote))
	}
	if d.Industry != "" {
		sb.WriteString(fmt.Sprintf("Industry: %s\n", d.Industry))
	}
	if d.Revenue != "" {
		sb.WriteString(fmt.Sprintf("Revenue: %s\n", d.Revenue))
	}
	if d.HQ.City != "" || d.HQ.State != "" {
		hq := strings.TrimSpace(d.HQ.City + ", " + d.HQ.State)
		if d.HQ.Country != "" && d.HQ.Country != "United States" {
			hq += ", " + d.HQ.Country
		}
		sb.WriteString(fmt.Sprintf("HQ: %s\n", hq))
	}
	if d.Founded > 0 {
		sb.WriteString(fmt.Sprintf("Founded: %d\n", d.Founded))
	}
	if len(d.Technologies) > 0 {
		sb.WriteString(fmt.Sprintf("Tech stack: %s\n", strings.Join(d.Technologies, ", ")))
	}
	if d.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", truncate(d.Description, 200)))
	}
	return sb.String(), nil
}

func urlEncode(s string) string {
	// Simple URL encoding for query parameters
	replacer := strings.NewReplacer(" ", "+", "&", "%26", "=", "%3D", "#", "%23")
	return replacer.Replace(s)
}
