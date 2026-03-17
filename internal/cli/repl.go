package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/conductorone/prospecting-bot/internal/auth"
	"github.com/conductorone/prospecting-bot/internal/config"
	"github.com/conductorone/prospecting-bot/internal/llm"
	"github.com/conductorone/prospecting-bot/internal/tools"
)

const banner = `
╔══════════════════════════════════════════════════════╗
║        C1ProspectingBot v2 — Local Test Mode         ║
║  Type your prompt and press Enter. 'exit' to quit.   ║
║  'tools' to list available tools. 'help' for tips.   ║
╚══════════════════════════════════════════════════════╝
`

const helpText = `
Example prompts to try:
  • What are the hottest accounts this week?
  • Research <company name>
  • Find contacts at <company name>
  • What case studies do we have for fintech?
  • Draft a cold email for <contact name> at <company>
  • What Gong calls have we had with <company>?
  • morning kickoff

Commands:
  • auth google  — sign in to Google Drive (one-time, browser-based)
  • tools        — list all registered tools and their status
  • help         — show this message

Tips:
  • Tools with missing API keys will return a helpful message instead of failing.
  • Set keys in .env to activate each data source.
`

// Run starts an interactive REPL session using the same pipeline as the Slack bot.
func Run(cfg *config.Config) {
	fmt.Print(banner)

	llmClient := llm.New(cfg)
	registry := tools.New(cfg)

	fmt.Println("Loaded tools:", toolNames(registry))
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		switch strings.ToLower(input) {
		case "exit", "quit", "q":
			fmt.Println("Bye!")
			return
		case "help":
			fmt.Println(helpText)
			continue
		case "tools":
			printToolStatus(cfg, registry)
			continue
		case "auth google":
			if err := auth.GoogleOAuthFlow(cfg); err != nil {
				fmt.Printf("❌ Google auth failed: %v\n\n", err)
			}
			continue
		case "morning kickoff":
			input = llm.MorningKickoffPrompt
		}

		fmt.Println()
		start := time.Now()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		response, err := llmClient.Process(ctx, input, registry)
		cancel()

		elapsed := time.Since(start).Round(time.Millisecond)

		if err != nil {
			fmt.Printf("❌ Error: %v\n\n", err)
			continue
		}

		fmt.Println("─────────────────────────────────────────────────────")
		fmt.Println(response)
		fmt.Printf("\n─────────────────────────────────────────────────────\n")
		fmt.Printf("⏱  %s\n\n", elapsed)
	}
}

func toolNames(registry *tools.Registry) string {
	names := registry.ToolNames()
	return strings.Join(names, ", ")
}

func printToolStatus(cfg *config.Config, registry *tools.Registry) {
	fmt.Println("\nRegistered tools and credential status:")
	sf := credStatus(cfg.SFInstanceURL != "" && cfg.SFAccessToken != "", "SF_INSTANCE_URL + SF_ACCESS_TOKEN")
	cr := credStatus(cfg.CommonRoomAPIKey != "", "COMMONROOM_API_KEY")
	statuses := map[string]string{
		"search_salesforce_accounts":          sf,
		"check_salesforce_opportunities":      sf,
		"get_salesforce_contacts":             sf,
		"get_salesforce_account_activity":     sf,
		"get_common_room_high_intent_accounts": cr,
		"get_common_room_account_signals":     cr,
		"get_common_room_contacts":            cr,
		"enrich_contact_lusha":                credStatus(cfg.LushaAPIKey != "", "LUSHA_API_KEY"),
		"bulk_enrich_contacts_lusha":          credStatus(cfg.LushaAPIKey != "", "LUSHA_API_KEY"),
		"enrich_company_lusha":                credStatus(cfg.LushaAPIKey != "", "LUSHA_API_KEY"),
		"enrich_contact_apollo":               credStatus(cfg.ApolloAPIKey != "", "APOLLO_API_KEY"),
		"bulk_enrich_contacts_apollo":         credStatus(cfg.ApolloAPIKey != "", "APOLLO_API_KEY"),
		"list_gong_calls":                     credStatus(cfg.GongAccessKey != "" && cfg.GongAccessKeySecret != "", "GONG_ACCESS_KEY + GONG_ACCESS_KEY_SECRET"),
		"search_notion":                       credStatus(cfg.NotionToken != "" && cfg.NotionToken != "secret_", "NOTION_TOKEN"),
		"get_notion_page":                     credStatus(cfg.NotionToken != "" && cfg.NotionToken != "secret_", "NOTION_TOKEN"),
		"search_google_drive":                 credStatus(cfg.GoogleClientID != "" && cfg.GoogleRefreshToken != "", "GOOGLE_CLIENT_ID + auth google"),
		"read_google_drive_file":              credStatus(cfg.GoogleClientID != "" && cfg.GoogleRefreshToken != "", "GOOGLE_CLIENT_ID + auth google"),
		"list_collateral_folder":              credStatus(cfg.GoogleClientID != "" && cfg.GoogleRefreshToken != "", "GOOGLE_CLIENT_ID + auth google"),
		"verify_employee_count":               credStatus(cfg.BraveSearchAPIKey != "", "BRAVE_SEARCH_API_KEY"),
		"search_company_news":                 credStatus(cfg.BraveSearchAPIKey != "", "BRAVE_SEARCH_API_KEY"),
		"web_search":                          credStatus(cfg.BraveSearchAPIKey != "", "BRAVE_SEARCH_API_KEY"),
	}
	for _, name := range registry.ToolNames() {
		status := statuses[name]
		fmt.Printf("  %-42s %s\n", name, status)
	}
	fmt.Println()
}

func credStatus(configured bool, varName string) string {
	if configured {
		return "✅ configured"
	}
	return "⚠️  not set (" + varName + ")"
}
