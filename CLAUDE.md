# C1ProspectingBotV2 — Claude Context

## What This Is
A Slack-based AI prospecting bot for the ConductorOne GTM team. It runs in Slack via Socket Mode, listens for @mentions and thread follow-ups, and uses an agentic tool-use loop (Claude via AWS Bedrock) to query Salesforce, Common Room, Lusha, Apollo.io, Notion, Google Drive, Gong, and web search to surface high-intent accounts and contacts for SDRs and the broader GTM team.

**Deployed on:** AWS ECS Fargate, cluster `union-station`, service `ProspectingBot-V2` (us-west-2)
**GitHub repo:** github.com/conductorone/prospecting-bot (branch: main)
**Go binary entrypoint:** `./cmd/bot`

---

## Architecture
- `internal/bot/handler.go` — Slack Socket Mode event loop, handles @mentions + thread follow-ups
- `internal/llm/` — AWS Bedrock client (Claude Opus 4.6 via cross-region inference profile), agentic tool-use loop (max 20 iterations), system prompt
- `internal/tools/` — all tool implementations + registry
- `internal/config/` — env var loading
- `cmd/bot/` — main entrypoint

---

## SDR Territory Map (current)
| SDR | States | Type |
|-----|--------|------|
| Jessica | CA, OR, WA, ID, MT, WY, AK | Non-strategic (1K–15K emp) |
| Sumeet | ME, VT, NH, NY, MA, CT, RI | Non-strategic (1K–15K emp) |
| Cole Pammer | PA, NJ, DE, MD, DC, VA, NC, SC, GA, FL, HI, CA (regular) + CA, OR, WA, ID, MT, WY, AK (strategic) | Strategic: 7 regular + 3 strategic/day |
| Jonathan | ND, SD, MN, IA, WI, IL, MI, IN, OH, KY, WV | Non-strategic (1K–15K emp) |

Territory scoping only applies when an SDR runs a territory-based search ("my accounts", "morning kickoff", etc.). All other queries give full access to all tools and data regardless of who is asking.

---

## Tool Registry (22 tools)
- **Salesforce:** `search_salesforce_accounts`, `check_salesforce_opportunities`, `get_salesforce_contacts`, `get_salesforce_activity`, `query_salesforce` (raw SOQL passthrough)
- **Common Room:** `get_common_room_high_intent_accounts`, `get_intent_signals`, `get_contacts_with_signals`
- **Lusha:** `enrich_contact_lusha`, `enrich_contact_with_domain_lusha`, `bulk_enrich_contacts_lusha`, `enrich_company_lusha`
- **Apollo.io:** `enrich_contact_apollo`, `bulk_match_contacts_apollo`
- **Google Drive:** `list_collateral_folder`, `read_google_drive_file`
- **Gong:** `list_gong_calls`, `get_gong_transcript`
- **Notion:** `search_notion`, `get_notion_page`
- **Web:** `web_search`, `verify_employee_count`, `search_company_news`

---

## Integration Status

| Integration | Status | Notes |
|-------------|--------|-------|
| Slack | ✅ Working | Socket Mode, @mention + thread follow-up |
| AWS Bedrock | ✅ Working | Claude Opus 4.6, cross-region inference profile |
| Lusha | ✅ Working | Contact + company enrichment |
| Apollo.io | ✅ Working | Contact enrichment fallback |
| Common Room | ⚠️ Partial | See issue below |
| Salesforce | ⚠️ Token expires every 2h | Needs OAuth Connected App (pending) |
| Google Drive | ❌ Not configured | GOOGLE_CLIENT_ID/SECRET/REFRESH_TOKEN empty |
| Gong | ❌ Not configured | GONG_ACCESS_KEY/SECRET empty |
| Notion | ❌ Not configured | NOTION_TOKEN only has prefix `secret_` |
| Brave Search | ❌ Not configured | BRAVE_SEARCH_API_KEY empty |

---

## Common Room — Known Issue (ACTIVE)
This is the current blocker being worked on.

**Problem:** The bot uses two paths for Common Room:
1. **MCP path** (primary): `https://mcp.commonroom.io/mcp/` — requires an OAuth token, NOT the REST API key. Currently returns `"Token expired or invalid. Reconnect the MCP server to sign in again."`
2. **REST fallback:** `https://api.commonroom.io/community/v1` — the REST API key is valid and works for `/segments`, but the endpoints the bot uses (`/organizations`, `/organizations/search`) return 404. `/members` returns "input parameter error" with unknown required params.

**What works with the current REST key:**
- `/community/v1/segments` → returns list of segment names/IDs ✅
- Everything else → 404 or 400 ❌

**The MCP path is authenticated in Claude Code locally** (the user has Common Room MCP connected in Claude Desktop/Claude Code settings). The question being investigated is whether that MCP OAuth token can be extracted and used in the Slack bot.

**Credentials in .env:**
- `COMMONROOM_API_KEY` = the REST JWT (works for REST `/segments` only)
- `COMMONROOM_COMMUNITY_ID` = `10853-conductor-one`

**Next steps to investigate:**
- Check `~/.claude/settings.json` for the MCP OAuth token used by Claude Code's Common Room connection
- OR get a fresh MCP OAuth token from Common Room's admin panel / OAuth flow
- OR rewrite REST fallback to use endpoints that actually work (very limited without MCP)

---

## ECS Deployment Notes
- Env vars are set in the ECS task definition (not in .env — that's local only and gitignored)
- After updating the task definition, you must **force new deployment** (ECS → Service → Update → Force new deployment) or the running container won't pick up changes
- The AWS CLI is not installed locally; no AWS credentials configured on the dev machine
- To add/update env vars: AWS Console → ECS → Task Definitions → create new revision → update service

---

## Key Behaviors
- SDR identity is auto-detected from Slack profile (no "which SDR are you?" prompt)
- Bot adds 👀 reaction when processing, removes it when done
- Thread follow-up works without @mention (bot tracks active threads for 24h)
- Enrichment order: Lusha first, Apollo.io fallback
- ICP range: 1,000–14,900 employees
- Contact targets: IT Security, IAM, Identity & Access Management, IT Operations, Information Security, Security Engineering, AI Governance, AI Enablement, Cybersecurity Engineering — Senior/Lead/Manager/Director/VP/C-level only
- Always check Salesforce opportunities before recommending any account

---

## Go Build
```bash
/Users/jessepetrick/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.8.darwin-arm64/bin/go build ./...
```
(standard `go` not in PATH — use full path above)

---

## Pending / Future Work
- [ ] Fix Common Room MCP auth for the Slack bot (current blocker)
- [ ] Fix Common Room REST fallback endpoints
- [ ] Salesforce OAuth refresh token (token expires every 2h; needs SF admin to create Connected App)
- [ ] Connect Google Drive (OAuth setup)
- [ ] Connect Gong (add API keys)
- [ ] Connect Notion (full token, not just prefix)
- [ ] Connect Brave Search (add API key)
- [ ] Lusha intent signals (API add-on required on their plan; schema discovered, implementation ready to add)
- [ ] GitHub Actions CI/CD for auto-deploy to ECS on push
