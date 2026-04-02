package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/conductorone/prospecting-bot/internal/config"
)

// ToolFunc is the function signature for a tool implementation.
type ToolFunc func(ctx context.Context, inputJSON json.RawMessage) (string, error)

// toolDef holds the definition and implementation of a tool.
type toolDef struct {
	name        string
	description string
	inputSchema map[string]any
	fn          ToolFunc
}

// Registry holds all registered tools and dispatches calls from Claude.
type Registry struct {
	tools map[string]toolDef
}

// New creates a Registry wired up to all data source tools.
func New(cfg *config.Config) *Registry {
	r := &Registry{tools: make(map[string]toolDef)}

	// ── Salesforce ──────────────────────────────────────────────────────────

	r.register("search_salesforce_accounts",
		"Search Salesforce for accounts by company name. Use as primary source when looking for accounts. Always run check_salesforce_opportunities after finding accounts. Returns account name, ID, employee count, industry, last activity date, billing state.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Company name or partial name to search for (e.g. 'Acme', 'Electronic Arts')",
				},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return SearchAccounts(ctx, cfg, p.Query)
		},
	)

	r.register("check_salesforce_opportunities",
		"CRITICAL: Check for active opportunities on an account before recommending it. Must run before every account recommendation. Identifies active opps (Prospect/Discover/Prove/Propose+Contract), current customers (Closed Won), and closed-lost accounts. Returns opp name, stage, close date, owner.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{
					"type":        "string",
					"description": "Salesforce Account ID (18-char). More precise — use if available.",
				},
				"account_name": map[string]any{
					"type":        "string",
					"description": "Account name — searches name variations (e.g. 'EA', 'Electronic Arts').",
				},
			},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				AccountID   string `json:"account_id"`
				AccountName string `json:"account_name"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return GetOpportunities(ctx, cfg, p.AccountID, p.AccountName)
		},
	)

	r.register("get_salesforce_contacts",
		"Get contacts for an account from Salesforce. Use as first step when finding contacts at a company. Returns name, title, email, phone, last activity.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{
					"type":        "string",
					"description": "Salesforce Account ID (optional, more precise)",
				},
				"account_name": map[string]any{
					"type":        "string",
					"description": "Account name fallback if account_id not available",
				},
			},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				AccountID   string `json:"account_id"`
				AccountName string `json:"account_name"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return GetContacts(ctx, cfg, p.AccountID, p.AccountName)
		},
	)

	r.register("get_salesforce_account_activity",
		"Get recent activity history for an account. Salesforce INACTIVITY (5-6+ months) is a HIGH PRIORITY signal — means the account is untouched. Recent activity means someone is working it — deprioritize.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{
					"type":        "string",
					"description": "Salesforce Account ID",
				},
				"days_back": map[string]any{
					"type":        "integer",
					"description": "How many days back to look (default 180)",
				},
			},
			"required": []string{"account_id"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				AccountID string `json:"account_id"`
				DaysBack  int    `json:"days_back"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return GetAccountActivity(ctx, cfg, p.AccountID, p.DaysBack)
		},
	)

	r.register("query_salesforce",
		"Execute a raw SOQL query against Salesforce. Use this for any query requiring custom fields, specific filters, date ranges, or objects not covered by the other Salesforce tools (e.g. Lead, Task, Event, Campaign, custom objects). Construct valid SOQL and pass it directly. Use the specialized tools (search_salesforce_accounts, check_salesforce_opportunities, get_salesforce_contacts, get_salesforce_account_activity) for their intended purposes — use this tool when those are insufficient.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"soql": map[string]any{
					"type":        "string",
					"description": "A valid SOQL query string, e.g. \"SELECT Id, Name, CustomField__c FROM Account WHERE CreatedDate = LAST_N_DAYS:30 LIMIT 50\"",
				},
			},
			"required": []string{"soql"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				SOQL string `json:"soql"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return QuerySalesforce(ctx, cfg, p.SOQL)
		},
	)

	r.register("describe_salesforce_object",
		"Discover all fields (standard AND custom) available on any Salesforce object. Call this BEFORE using query_salesforce when you need to know field names — especially for custom fields (ending in __c) or objects you haven't queried before. Works for standard objects (Account, Contact, Opportunity, Lead, Task, Event, Campaign) and any custom objects. Returns field API name, label, type, and whether it's custom.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"object_name": map[string]any{
					"type":        "string",
					"description": "Salesforce object API name, e.g. \"Account\", \"Contact\", \"Opportunity\", \"Lead\", or a custom object like \"My_Object__c\"",
				},
			},
			"required": []string{"object_name"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				ObjectName string `json:"object_name"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return DescribeSalesforceObject(ctx, cfg, p.ObjectName)
		},
	)

	// ── Common Room ─────────────────────────────────────────────────────────

	r.register("get_common_room_high_intent_accounts",
		"PRIMARY source for account prioritization. Returns accounts from Common Room ranked by lead score percentiles (Account Tiering, 3rd Party Intent, Website Intent). Supports territory filtering by US state. Use this to build prospecting lists before checking Salesforce.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"min_score": map[string]any{
					"type":        "integer",
					"description": "Minimum intent score threshold (default 70, used for REST fallback only)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max accounts to return (default 25)",
				},
				"states": map[string]any{
					"type":        "array",
					"description": "Optional list of US state abbreviations to filter territory (e.g. [\"NY\",\"MA\",\"CT\",\"RI\",\"VT\",\"NH\",\"ME\"]). Leave empty for all territories.",
					"items":       map[string]any{"type": "string"},
				},
			},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				MinScore int      `json:"min_score"`
				Limit    int      `json:"limit"`
				States   []string `json:"states"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return GetHighIntentAccounts(ctx, cfg, p.MinScore, p.Limit, p.States)
		},
	)

	r.register("get_common_room_account_signals",
		"Get detailed intent signals and buying activity for a specific account. Use to get 'why now' context: website visits, content downloads, job changes, product sign-ups, tech stack changes. Always include the intent score in your response.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"company_name": map[string]any{
					"type":        "string",
					"description": "Company name to look up in Common Room",
				},
			},
			"required": []string{"company_name"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				CompanyName string `json:"company_name"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return GetIntentSignals(ctx, cfg, p.CompanyName)
		},
	)

	r.register("get_common_room_contacts",
		"Get contacts at an account who have actual engagement signals (website visits, content downloads, etc.). Only returns contacts with real activity — do NOT recommend contacts with zero activity.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_name": map[string]any{
					"type":        "string",
					"description": "Company name to find engaged contacts for",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max contacts to return (default 15)",
				},
			},
			"required": []string{"account_name"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				AccountName string `json:"account_name"`
				Limit       int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return GetContactsWithSignals(ctx, cfg, p.AccountName, p.Limit)
		},
	)

	// ── Lusha ───────────────────────────────────────────────────────────────

	r.register("enrich_contact_lusha",
		"Enrich a contact with verified email address and phone number from Lusha. Use after finding contacts in Salesforce or Common Room to get their direct contact details. domain is optional but improves match accuracy.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"first_name": map[string]any{
					"type":        "string",
					"description": "Contact first name",
				},
				"last_name": map[string]any{
					"type":        "string",
					"description": "Contact last name",
				},
				"company": map[string]any{
					"type":        "string",
					"description": "Company the contact works at",
				},
				"domain": map[string]any{
					"type":        "string",
					"description": "Company website domain (optional, improves accuracy — e.g. 'stripe.com')",
				},
				"linkedin_url": map[string]any{
					"type":        "string",
					"description": "LinkedIn profile URL (optional, improves match accuracy)",
				},
			},
			"required": []string{"first_name", "last_name", "company"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				FirstName   string `json:"first_name"`
				LastName    string `json:"last_name"`
				Company     string `json:"company"`
				Domain      string `json:"domain"`
				LinkedInURL string `json:"linkedin_url"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return EnrichContactWithDomain(ctx, cfg, p.FirstName, p.LastName, p.Company, p.Domain, p.LinkedInURL)
		},
	)

	r.register("bulk_enrich_contacts_lusha",
		"Enrich up to 5 contacts at once with verified email and phone from Lusha. Use when you have a list of contacts to enrich in one shot.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"contacts": map[string]any{
					"type":        "array",
					"description": "List of contacts to enrich (max 5)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"first_name":   map[string]any{"type": "string"},
							"last_name":    map[string]any{"type": "string"},
							"company":      map[string]any{"type": "string"},
							"domain":       map[string]any{"type": "string"},
							"linkedin_url": map[string]any{"type": "string"},
						},
						"required": []string{"first_name", "last_name", "company"},
					},
				},
			},
			"required": []string{"contacts"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Contacts []map[string]string `json:"contacts"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return BulkEnrichContacts(ctx, cfg, p.Contacts)
		},
	)

	r.register("enrich_company_lusha",
		"Get firmographic data for a company from Lusha: employee count (with ICP range check), industry, revenue, HQ location, founded year, and tech stack. Use to verify ICP fit and supplement Salesforce account data. Prefer domain over name for accuracy.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"company_name": map[string]any{
					"type":        "string",
					"description": "Company name to look up",
				},
				"domain": map[string]any{
					"type":        "string",
					"description": "Company website domain (optional, more accurate than name — e.g. 'stripe.com')",
				},
			},
			"required": []string{"company_name"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				CompanyName string `json:"company_name"`
				Domain      string `json:"domain"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return EnrichCompanyLusha(ctx, cfg, p.CompanyName, p.Domain)
		},
	)

	// ── Apollo.io ───────────────────────────────────────────────────────────

	r.register("enrich_contact_apollo",
		"Enrich a contact with verified email and phone number from Apollo.io. Use alongside or instead of Lusha — Apollo often has broader coverage and can reveal personal emails. Provide domain or LinkedIn URL for best match accuracy.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"first_name": map[string]any{
					"type":        "string",
					"description": "Contact first name",
				},
				"last_name": map[string]any{
					"type":        "string",
					"description": "Contact last name",
				},
				"company": map[string]any{
					"type":        "string",
					"description": "Company the contact works at",
				},
				"domain": map[string]any{
					"type":        "string",
					"description": "Company website domain (optional, improves accuracy — e.g. 'stripe.com')",
				},
				"linkedin_url": map[string]any{
					"type":        "string",
					"description": "LinkedIn profile URL (optional, improves match accuracy)",
				},
				"email": map[string]any{
					"type":        "string",
					"description": "Known email address to use as match hint (optional)",
				},
			},
			"required": []string{"first_name", "last_name", "company"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				FirstName   string `json:"first_name"`
				LastName    string `json:"last_name"`
				Company     string `json:"company"`
				Domain      string `json:"domain"`
				LinkedInURL string `json:"linkedin_url"`
				Email       string `json:"email"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return EnrichContactApollo(ctx, cfg, p.FirstName, p.LastName, p.Company, p.Domain, p.LinkedInURL, p.Email)
		},
	)

	r.register("bulk_enrich_contacts_apollo",
		"Enrich up to 10 contacts at once with email and phone from Apollo.io. Prefer this over repeated single calls when you have a list. Returns work email, personal emails (if available), and phone numbers.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"contacts": map[string]any{
					"type":        "array",
					"description": "List of contacts to enrich (max 10)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"first_name":   map[string]any{"type": "string"},
							"last_name":    map[string]any{"type": "string"},
							"company":      map[string]any{"type": "string"},
							"domain":       map[string]any{"type": "string", "description": "optional"},
							"linkedin_url": map[string]any{"type": "string", "description": "optional"},
							"email":        map[string]any{"type": "string", "description": "optional hint"},
						},
						"required": []string{"first_name", "last_name", "company"},
					},
				},
			},
			"required": []string{"contacts"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Contacts []map[string]string `json:"contacts"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return BulkEnrichContactsApollo(ctx, cfg, p.Contacts)
		},
	)

	// ── Gong ────────────────────────────────────────────────────────────────

	r.register("list_gong_calls",
		"Search Gong for recent calls associated with a company or account. NOTE: Gong API has known pagination/truncation issues (~45kb limit). Results may be incomplete. If the SDR has a specific transcript, ask them to paste it for analysis instead.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"company_name": map[string]any{
					"type":        "string",
					"description": "Company name to search calls for",
				},
				"days_back": map[string]any{
					"type":        "integer",
					"description": "Number of days to look back for calls (default: 90)",
				},
			},
			"required": []string{"company_name"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				CompanyName string `json:"company_name"`
				DaysBack    int    `json:"days_back"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			if p.DaysBack == 0 {
				p.DaysBack = 90
			}
			return ListGongCalls(ctx, cfg, p.CompanyName, p.DaysBack)
		},
	)

	// ── Notion ──────────────────────────────────────────────────────────────

	r.register("search_notion",
		"Search Notion for internal collateral, case studies, battlecards, outreach templates, playbooks, and competitive intel. Use when SDR asks about messaging for a specific vertical, needs a case study, or wants email templates.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query (e.g. 'fintech case study', 'CISO email template', 'competitive intel sailpoint')",
				},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return SearchNotion(ctx, cfg, p.Query)
		},
	)

	r.register("get_notion_page",
		"Get the full text content of a specific Notion page by ID. Use when search returns a page URL and the SDR needs the actual content of a case study, template, or playbook.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"page_id": map[string]any{
					"type":        "string",
					"description": "Notion page ID (UUID from the URL, e.g. '2d94694ad8468179bcf4fcbb0757f616')",
				},
			},
			"required": []string{"page_id"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				PageID string `json:"page_id"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return GetPageContent(ctx, cfg, p.PageID)
		},
	)

	// ── Web Search ──────────────────────────────────────────────────────────

	r.register("verify_employee_count",
		"REQUIRED before including any account in recommendations. Verify a company's current employee count via web search. ICP range is strictly 1,000–14,900 employees. Do not trust Salesforce employee data alone — search to confirm.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"company_name": map[string]any{
					"type":        "string",
					"description": "Company name to verify headcount for",
				},
			},
			"required": []string{"company_name"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				CompanyName string `json:"company_name"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return VerifyEmployeeCount(ctx, cfg, p.CompanyName)
		},
	)

	r.register("search_company_news",
		"Search the web for recent company news and buying signals: security incidents, compliance prep (SOC 2, ISO 27001), new CISO/IT leadership, M&A activity, headcount changes, SaaS/cloud expansion, job postings for identity/security/IT roles. Use to build 'why now' narrative.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"company_name": map[string]any{
					"type":        "string",
					"description": "Company name to research buying signals for",
				},
			},
			"required": []string{"company_name"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				CompanyName string `json:"company_name"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return SearchCompanyNews(ctx, cfg, p.CompanyName)
		},
	)

	// ── Google Drive ────────────────────────────────────────────────────────

	r.register("search_google_drive",
		"Search Google Drive for internal documents, case studies, playbooks, competitive intel, or templates by keyword. Returns file name, type, ID, and link. Use when looking for internal collateral not found in Notion.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query (e.g. 'fintech case study', 'objection handling', 'competitive salesforce')",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max results to return (default 10)",
				},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return SearchGoogleDrive(ctx, cfg, p.Query, p.Limit)
		},
	)

	r.register("read_google_drive_file",
		"Read the full text content of a Google Doc, Sheet, or Slides file from Google Drive. Use the file ID returned by search_google_drive or list_collateral_folder. Supports Google Docs (text), Sheets (CSV), and Slides (text).",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_id": map[string]any{
					"type":        "string",
					"description": "Google Drive file ID (from search_google_drive or list_collateral_folder results)",
				},
			},
			"required": []string{"file_id"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				FileID string `json:"file_id"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return ReadGoogleDriveFile(ctx, cfg, p.FileID)
		},
	)

	r.register("list_collateral_folder",
		"List the full directory tree of the ConductorOne collateral Google Drive folder. Use this FIRST before search_google_drive when recommending collateral or outreach strategy — it shows exactly what case studies, templates, battlecards, and playbooks are available. Returns file names, types, IDs, and direct links.",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			return ListCollateralFolder(ctx, cfg)
		},
	)

	// ── Web Search ──────────────────────────────────────────────────────────

	r.register("web_search",
		"General web search for any query — use for ad-hoc lookups not covered by verify_employee_count or search_company_news.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query",
				},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", err
			}
			return WebSearch(ctx, cfg, p.Query)
		},
	)

	return r
}

func (r *Registry) register(name, description string, schema map[string]any, fn ToolFunc) {
	r.tools[name] = toolDef{name: name, description: description, inputSchema: schema, fn: fn}
}

// AnthropicTools returns the tool definitions formatted for the Claude API.
func (r *Registry) AnthropicTools() []anthropic.ToolUnionParam {
	params := make([]anthropic.ToolUnionParam, 0, len(r.tools))
	for _, t := range r.tools {
		props, _ := t.inputSchema["properties"].(map[string]any)
		tool := anthropic.ToolParam{
			Name:        t.name,
			Description: anthropic.String(t.description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: props,
			},
		}
		params = append(params, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return params
}

// ToolNames returns the names of all registered tools in sorted order.
func (r *Registry) ToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Execute dispatches a tool call by name.
func (r *Registry) Execute(ctx context.Context, name string, inputJSON json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.fn(ctx, inputJSON)
}
