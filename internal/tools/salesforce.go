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

const sfAPIVersion = "v59.0"

func sfGet(ctx context.Context, cfg *config.Config, soql string) (map[string]any, error) {
	token, err := getAccessToken(ctx, cfg)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/services/data/%s/query?q=%s", sfBaseURL(cfg), sfAPIVersion, url.QueryEscape(soql))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
		return nil, fmt.Errorf("Salesforce auth failed (401): SF_ACCESS_TOKEN may be expired — reconnect or refresh the token")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Salesforce API error %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Salesforce response: %w", err)
	}
	return result, nil
}

// SearchAccounts queries Salesforce for accounts matching the given name query.
func SearchAccounts(ctx context.Context, cfg *config.Config, query string) (string, error) {
	// Escape single quotes in the query to prevent SOQL injection
	safe := strings.ReplaceAll(query, "'", "\\'")
	soql := fmt.Sprintf(
		"SELECT Id, Name, NumberOfEmployees, LastActivityDate, Industry, BillingState, BillingCountry, Website FROM Account WHERE Name LIKE '%%%s%%' ORDER BY LastActivityDate DESC NULLS LAST LIMIT 20",
		safe,
	)

	result, err := sfGet(ctx, cfg, soql)
	if err != nil {
		return "", err
	}

	records := extractRecords(result)
	if len(records) == 0 {
		return fmt.Sprintf("No Salesforce accounts found matching '%s'.", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Salesforce accounts matching '%s' (%d results):\n\n", query, len(records)))
	for _, rec := range records {
		sb.WriteString(fmt.Sprintf("- **%s** (ID: %s)\n", strField(rec, "Name"), strField(rec, "Id")))
		sb.WriteString(fmt.Sprintf("  Employees: %s | Industry: %s | State: %s\n",
			numField(rec, "NumberOfEmployees"), strField(rec, "Industry"), strField(rec, "BillingState")))
		sb.WriteString(fmt.Sprintf("  Last Activity: %s | Website: %s\n\n",
			strField(rec, "LastActivityDate"), strField(rec, "Website")))
	}
	return sb.String(), nil
}

// GetOpportunities returns Salesforce opportunities for an account (by ID or name).
func GetOpportunities(ctx context.Context, cfg *config.Config, accountID, accountName string) (string, error) {
	var soql string
	if accountID != "" {
		safe := strings.ReplaceAll(accountID, "'", "\\'")
		soql = fmt.Sprintf(
			"SELECT Id, Name, StageName, CloseDate, OwnerId, Owner.Name, Amount FROM Opportunity WHERE AccountId = '%s' ORDER BY CloseDate DESC LIMIT 10",
			safe,
		)
	} else if accountName != "" {
		safe := strings.ReplaceAll(accountName, "'", "\\'")
		soql = fmt.Sprintf(
			"SELECT Id, Name, StageName, CloseDate, OwnerId, Owner.Name, Amount, Account.Name FROM Opportunity WHERE Account.Name LIKE '%%%s%%' ORDER BY CloseDate DESC LIMIT 10",
			safe,
		)
	} else {
		return "", fmt.Errorf("provide either account_id or account_name")
	}

	result, err := sfGet(ctx, cfg, soql)
	if err != nil {
		return "", err
	}

	records := extractRecords(result)
	if len(records) == 0 {
		name := accountID
		if accountName != "" {
			name = accountName
		}
		return fmt.Sprintf("No Salesforce opportunities found for '%s'. Account appears to be greenfield.", name), nil
	}

	var sb strings.Builder
	name := accountID
	if accountName != "" {
		name = accountName
	}
	sb.WriteString(fmt.Sprintf("Salesforce opportunities for '%s' (%d found):\n\n", name, len(records)))
	for _, rec := range records {
		stage := strField(rec, "StageName")
		sb.WriteString(fmt.Sprintf("- **%s**\n", strField(rec, "Name")))
		sb.WriteString(fmt.Sprintf("  Stage: %s | Close Date: %s | Owner: %s\n\n",
			stage, strField(rec, "CloseDate"), ownerName(rec)))
	}
	return sb.String(), nil
}

// GetContacts returns Salesforce contacts for an account.
func GetContacts(ctx context.Context, cfg *config.Config, accountID, accountName string) (string, error) {
	var soql string
	if accountID != "" {
		safe := strings.ReplaceAll(accountID, "'", "\\'")
		soql = fmt.Sprintf(
			"SELECT Id, FirstName, LastName, Title, Email, Phone, LastActivityDate FROM Contact WHERE AccountId = '%s' ORDER BY LastActivityDate DESC NULLS LAST LIMIT 20",
			safe,
		)
	} else if accountName != "" {
		safe := strings.ReplaceAll(accountName, "'", "\\'")
		soql = fmt.Sprintf(
			"SELECT Id, FirstName, LastName, Title, Email, Phone, LastActivityDate, Account.Name FROM Contact WHERE Account.Name LIKE '%%%s%%' ORDER BY LastActivityDate DESC NULLS LAST LIMIT 20",
			safe,
		)
	} else {
		return "", fmt.Errorf("provide either account_id or account_name")
	}

	result, err := sfGet(ctx, cfg, soql)
	if err != nil {
		return "", err
	}

	records := extractRecords(result)
	if len(records) == 0 {
		return "No contacts found in Salesforce for this account.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Salesforce contacts (%d found):\n\n", len(records)))
	for _, rec := range records {
		email := strField(rec, "Email")
		if email == "" {
			email = "not in SF"
		}
		phone := strField(rec, "Phone")
		if phone == "" {
			phone = "not in SF"
		}
		sb.WriteString(fmt.Sprintf("- **%s %s** — %s\n", strField(rec, "FirstName"), strField(rec, "LastName"), strField(rec, "Title")))
		sb.WriteString(fmt.Sprintf("  Email: %s | Phone: %s | Last Activity: %s\n\n",
			email, phone, strField(rec, "LastActivityDate")))
	}
	return sb.String(), nil
}

// GetAccountActivity returns recent activity history for a Salesforce account.
// Salesforce INACTIVITY (5-6+ months) is a HIGH PRIORITY signal — untouched territory.
// Recent activity means someone is working the account — deprioritize.
func GetAccountActivity(ctx context.Context, cfg *config.Config, accountID string, daysBack int) (string, error) {
	if daysBack <= 0 {
		daysBack = 180
	}
	safe := strings.ReplaceAll(accountID, "'", "\\'")
	soql := fmt.Sprintf(
		"SELECT Id, Subject, ActivityDate, Description, Type, WhoId FROM ActivityHistory WHERE WhatId = '%s' ORDER BY ActivityDate DESC LIMIT 10",
		safe,
	)

	result, err := sfGet(ctx, cfg, soql)
	if err != nil {
		return "", err
	}

	records := extractRecords(result)
	if len(records) == 0 {
		return fmt.Sprintf("No activity history found for account %s. 🔥 HIGH PRIORITY SIGNAL: This account appears untouched — no SDR has engaged recently.", accountID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Activity history for account %s (%d records):\n\n", accountID, len(records)))
	for _, rec := range records {
		desc := strField(rec, "Description")
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("- **%s** (%s) — %s\n", strField(rec, "Subject"), strField(rec, "Type"), strField(rec, "ActivityDate")))
		if desc != "" {
			sb.WriteString(fmt.Sprintf("  %s\n", desc))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// DescribeSalesforceObject returns all fields (standard and custom) for a Salesforce object.
// Call this before query_salesforce when you need to know what fields are available,
// especially for custom fields (ending in __c) or unfamiliar objects.
func DescribeSalesforceObject(ctx context.Context, cfg *config.Config, objectName string) (string, error) {
	token, err := getAccessToken(ctx, cfg)
	if err != nil {
		return "", err
	}

	safe := strings.ReplaceAll(objectName, "/", "")
	endpoint := fmt.Sprintf("%s/services/data/%s/sobjects/%s/describe", sfBaseURL(cfg), sfAPIVersion, safe)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

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
		return "", fmt.Errorf("Salesforce auth failed (401): token may be expired")
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Sprintf("Salesforce object '%s' not found. Check the API name (custom objects end in __c, e.g. 'My_Object__c').", objectName), nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Salesforce describe error %d for '%s': %s", resp.StatusCode, objectName, truncate(string(body), 300)), nil
	}

	var describe struct {
		Name   string `json:"name"`
		Label  string `json:"label"`
		Fields []struct {
			Name        string `json:"name"`
			Label       string `json:"label"`
			Type        string `json:"type"`
			Custom      bool   `json:"custom"`
			Nillable    bool   `json:"nillable"`
			Updateable  bool   `json:"updateable"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(body, &describe); err != nil {
		return fmt.Sprintf("Salesforce: could not parse describe response for '%s'.", objectName), nil
	}

	var standard, custom []string
	for _, f := range describe.Fields {
		entry := fmt.Sprintf("  %-45s %-20s %s", f.Name, "("+f.Type+")", f.Label)
		if f.Custom {
			custom = append(custom, entry)
		} else {
			standard = append(standard, entry)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Salesforce object: %s (%s)**\n", describe.Label, describe.Name))
	sb.WriteString(fmt.Sprintf("Total fields: %d (%d standard, %d custom)\n\n", len(describe.Fields), len(standard), len(custom)))

	if len(custom) > 0 {
		sb.WriteString("**Custom Fields:**\n")
		for _, f := range custom {
			sb.WriteString(f + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("**Standard Fields:**\n")
	for _, f := range standard {
		sb.WriteString(f + "\n")
	}

	return sb.String(), nil
}

// QuerySalesforce executes an arbitrary SOQL query and returns the results.
// Use this for any query requiring custom fields, specific filters, or objects
// not covered by the other Salesforce tools.
func QuerySalesforce(ctx context.Context, cfg *config.Config, soql string) (string, error) {
	result, err := sfGet(ctx, cfg, soql)
	if err != nil {
		return "", err
	}

	records := extractRecords(result)
	totalSize := 0
	if v, ok := result["totalSize"].(float64); ok {
		totalSize = int(v)
	}

	if len(records) == 0 {
		return "Salesforce query returned no records.", nil
	}

	// Collect all field names across records (preserves encounter order via slice+set).
	seen := map[string]bool{}
	var fields []string
	for _, rec := range records {
		for k := range rec {
			if k == "attributes" {
				continue
			}
			if !seen[k] {
				seen[k] = true
				fields = append(fields, k)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Salesforce query results (%d records", len(records)))
	if totalSize > len(records) {
		sb.WriteString(fmt.Sprintf(" of %d total — increase LIMIT to see more", totalSize))
	}
	sb.WriteString("):\n\n")

	for i, rec := range records {
		sb.WriteString(fmt.Sprintf("**Record %d:**\n", i+1))
		for _, f := range fields {
			v := rec[f]
			if v == nil {
				continue
			}
			// Nested objects (e.g. Owner.Name) — pretty-print as JSON.
			switch val := v.(type) {
			case map[string]any:
				if b, err := json.Marshal(val); err == nil {
					sb.WriteString(fmt.Sprintf("  %s: %s\n", f, string(b)))
				}
			default:
				sb.WriteString(fmt.Sprintf("  %s: %v\n", f, val))
			}
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// --- helpers ---

func extractRecords(result map[string]any) []map[string]any {
	raw, ok := result["records"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if rec, ok := item.(map[string]any); ok {
			out = append(out, rec)
		}
	}
	return out
}

func strField(rec map[string]any, key string) string {
	v, ok := rec[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func numField(rec map[string]any, key string) string {
	v, ok := rec[key]
	if !ok || v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.0f", v)
}

func ownerName(rec map[string]any) string {
	owner, ok := rec["Owner"].(map[string]any)
	if !ok {
		return ""
	}
	return strField(owner, "Name")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
