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

const apolloBaseURL = "https://api.apollo.io/v1"

func apolloPost(ctx context.Context, cfg *config.Config, path string, payload any) ([]byte, error) {
	if cfg.ApolloAPIKey == "" {
		return nil, fmt.Errorf("Apollo not configured: set APOLLO_API_KEY")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apolloBaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.ApolloAPIKey)

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
		return nil, fmt.Errorf("Apollo auth failed: verify APOLLO_API_KEY")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("Apollo rate limited — try again shortly")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Apollo API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

// EnrichContactApollo uses the Apollo people/match endpoint to find contact info.
// Returns email(s) and phone number(s) for the person.
func EnrichContactApollo(ctx context.Context, cfg *config.Config, firstName, lastName, company, domain, linkedInURL, email string) (string, error) {
	if cfg.ApolloAPIKey == "" {
		return "Apollo not configured: set APOLLO_API_KEY. Use Lusha as fallback or look up manually in Apollo.", nil
	}

	payload := map[string]any{
		"first_name":        firstName,
		"last_name":         lastName,
		"organization_name": company,
		"reveal_personal_emails": true,
		"reveal_phone_number":    true,
	}
	if domain != "" {
		payload["domain"] = domain
	}
	if linkedInURL != "" {
		payload["linkedin_url"] = linkedInURL
	}
	if email != "" {
		payload["email"] = email
	}

	body, err := apolloPost(ctx, cfg, "/people/match", payload)
	if err != nil {
		return fmt.Sprintf("Apollo enrichment unavailable: %v", err), nil
	}

	var result struct {
		Person *apolloPerson `json:"person"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Apollo: could not parse response for %s %s.", firstName, lastName), nil
	}
	if result.Person == nil {
		return fmt.Sprintf("Apollo: no match found for %s %s at %s. Try Lusha or Sales Navigator.", firstName, lastName, company), nil
	}
	return formatApolloPerson(result.Person, firstName+" "+lastName, company), nil
}

// BulkEnrichContactsApollo enriches up to 10 contacts in one Apollo API call.
func BulkEnrichContactsApollo(ctx context.Context, cfg *config.Config, contacts []map[string]string) (string, error) {
	if cfg.ApolloAPIKey == "" {
		return "Apollo not configured: set APOLLO_API_KEY.", nil
	}
	if len(contacts) > 10 {
		contacts = contacts[:10]
	}

	details := make([]map[string]any, 0, len(contacts))
	for _, c := range contacts {
		entry := map[string]any{
			"first_name":             c["first_name"],
			"last_name":              c["last_name"],
			"organization_name":      c["company"],
			"reveal_personal_emails": true,
			"reveal_phone_number":    true,
		}
		if v := c["domain"]; v != "" {
			entry["domain"] = v
		}
		if v := c["linkedin_url"]; v != "" {
			entry["linkedin_url"] = v
		}
		if v := c["email"]; v != "" {
			entry["email"] = v
		}
		details = append(details, entry)
	}

	body, err := apolloPost(ctx, cfg, "/people/bulk_match", map[string]any{"details": details})
	if err != nil {
		return fmt.Sprintf("Apollo bulk enrichment unavailable: %v", err), nil
	}

	var result struct {
		Matches []struct {
			Person *apolloPerson `json:"person"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Sprintf("Apollo: could not parse bulk response. Raw: %s", truncate(string(body), 300)), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Apollo bulk enrichment (%d contacts):\n\n", len(contacts)))
	for i, m := range result.Matches {
		name := ""
		company := ""
		if i < len(contacts) {
			name = contacts[i]["first_name"] + " " + contacts[i]["last_name"]
			company = contacts[i]["company"]
		}
		if m.Person == nil {
			sb.WriteString(fmt.Sprintf("- **%s** at %s: no match found in Apollo\n\n", name, company))
		} else {
			sb.WriteString(formatApolloPerson(m.Person, name, company))
			sb.WriteString("\n---\n")
		}
	}
	return sb.String(), nil
}

type apolloPerson struct {
	Name         string `json:"name"`
	Title        string `json:"title"`
	Email        string `json:"email"`
	LinkedIn     string `json:"linkedin_url"`
	Organization struct {
		Name string `json:"name"`
	} `json:"organization"`
	PhoneNumbers []struct {
		RawNumber    string `json:"raw_number"`
		SanitizedNum string `json:"sanitized_number"`
		Type         string `json:"type"`
		Position     int    `json:"position"`
	} `json:"phone_numbers"`
	PersonalEmails []string `json:"personal_emails"`
	EmailStatus    string   `json:"email_status"`
}

func formatApolloPerson(p *apolloPerson, inputName, inputCompany string) string {
	displayName := p.Name
	if displayName == "" {
		displayName = inputName
	}
	displayCompany := p.Organization.Name
	if displayCompany == "" {
		displayCompany = inputCompany
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Apollo enrichment for %s at %s:**\n", displayName, displayCompany))
	if p.Title != "" {
		sb.WriteString(fmt.Sprintf("Title: %s\n", p.Title))
	}

	// Work email
	if p.Email != "" {
		status := ""
		if p.EmailStatus != "" {
			status = " (" + p.EmailStatus + ")"
		}
		sb.WriteString(fmt.Sprintf("Work Email: %s%s\n", p.Email, status))
	} else {
		sb.WriteString("Work Email: not found\n")
	}

	// Personal emails (if revealed)
	if len(p.PersonalEmails) > 0 {
		sb.WriteString("Personal Email(s): ")
		sb.WriteString(strings.Join(p.PersonalEmails, ", "))
		sb.WriteString("\n")
	}

	// Phone numbers
	if len(p.PhoneNumbers) > 0 {
		sb.WriteString("Phone(s):\n")
		for _, ph := range p.PhoneNumbers {
			num := ph.SanitizedNum
			if num == "" {
				num = ph.RawNumber
			}
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", num, ph.Type))
		}
	} else {
		sb.WriteString("Phone: not found\n")
	}

	if p.LinkedIn != "" {
		sb.WriteString(fmt.Sprintf("LinkedIn: %s\n", p.LinkedIn))
	}
	return sb.String()
}
