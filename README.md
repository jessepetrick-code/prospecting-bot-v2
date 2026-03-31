# C1ProspectingBot v2

AI-powered SDR prospecting assistant for the ConductorOne sales team. Built in Go, powered by Claude, deployed as a Slack bot.

The bot runs an agentic research loop — given a rep's name or a company, it pulls intent signals from Common Room, checks Salesforce for active opportunities, verifies ICP fit, finds and enriches contacts via Apollo and Lusha, and surfaces relevant collateral from Google Drive — all in one response.

---

## What It Does

- **Morning kickoff** — posts a daily top-10 account list to `#ai-prospecting` at 6am PT (Mon–Fri), scoped to each rep's territory
- **On-demand research** — `@C1ProspectingBot research Acme Corp` triggers a full account brief
- **Contact enrichment** — finds verified work email, personal email, and phone via Apollo + Lusha
- **Opportunity gating** — never recommends an account with an active Salesforce opportunity
- **ICP filtering** — enforces 1,000–15,000 employee range; verifies headcount via web search
- **Territory awareness** — automatically scopes results to the asking SDR's states
- **Collateral lookup** — searches the Google Drive collateral folder for relevant case studies and templates

---

## Architecture

```
Slack (Socket Mode)
      │ ▲
      ▼ │
┌─────────────┐     ┌───────────────────┐
│  Go Bot     │────▶│  Claude API       │
│  (main.go)  │◀────│  (tool_use loop)  │
└─────────────┘     └───────────────────┘
      │
      ▼
┌────────────────────────────────────────────┐
│              Tool Implementations           │
│  Salesforce  │  Common Room  │  Apollo.io  │
│  Lusha       │  Gong         │  Notion     │
│  Google Drive│  Brave Search │             │
└────────────────────────────────────────────┘
```

**Flow:**
1. Slack `@mention` → Socket Mode event → bot handler
2. User message + system prompt sent to Claude with 21 tool definitions
3. Claude returns `tool_use` blocks; bot executes tools against real APIs
4. Results fed back to Claude; loop continues until `end_turn`
5. Final text response posted to Slack thread

---

## SDR Territory Map

| SDR | Regular States | Strategic States | Daily Target |
|-----|---------------|-----------------|-------------|
| Jessica | CA, OR, WA, ID, MT, WY, AK | — | 10 accounts |
| Siena | MS, TN, AL, GA, SC, FL | — | 10 accounts |
| Claire Sulek | ME, VT, NH, NY, MA, CT, RI | PA, NJ, DE, MD, DC, VA, NC, SC, GA, FL | 7 regular + 3 strategic |
| Cole Pammer | PA, NJ, DE, MD, DC, VA, NC, SC, GA, FL, HI, CA | CA, OR, WA, ID, MT, WY, AK | 7 regular + 3 strategic |
| Jonathan | ND, SD, MN, IA, WI, IL, MI, IN, OH, KY, WV | — | 10 accounts |

---

## Project Structure

```
prospecting-bot-v2/
├── cmd/bot/main.go              # Entry point: -mode=slack (default) or -mode=cli
├── internal/
│   ├── auth/
│   │   └── google.go            # Google OAuth2 browser flow (one-time sign-in)
│   ├── bot/
│   │   ├── handler.go           # Slack Socket Mode event handler
│   │   └── poster.go            # Slack reply helpers
│   ├── cli/
│   │   └── repl.go              # Local REPL for testing without Slack
│   ├── config/
│   │   └── config.go            # Env var loading
│   ├── llm/
│   │   ├── client.go            # Anthropic SDK wrapper
│   │   ├── loop.go              # Agentic tool-use loop
│   │   └── prompt.go            # System prompt + morning kickoff prompt
│   ├── scheduler/
│   │   └── scheduler.go         # Cron job for daily morning kickoff
│   └── tools/
│       ├── registry.go          # Tool definitions + dispatch (21 tools)
│       ├── salesforce.go        # Salesforce REST API
│       ├── commonroom.go        # Common Room REST API
│       ├── lusha.go             # Lusha contact + company enrichment
│       ├── apollo.go            # Apollo.io contact enrichment
│       ├── gong.go              # Gong call history
│       ├── notion.go            # Notion search
│       ├── googledrive.go       # Google Drive search + read
│       └── websearch.go         # Brave Search web search
├── Dockerfile                   # Multi-stage build → ~10MB image
├── docker-compose.yml
├── Makefile
├── .env.example                 # All env vars documented
└── go.mod
```

---

## Setup

### Prerequisites

- Go 1.23+
- A Slack workspace where you can create apps
- Anthropic API key — [console.anthropic.com](https://console.anthropic.com/settings/keys)

### 1. Clone and install dependencies

```bash
git clone https://github.com/conductorone/prospecting-bot-v2
cd prospecting-bot-v2
go mod tidy
go build ./cmd/bot   # verify it compiles
```

### 2. Configure environment

```bash
cp .env.example .env
# Fill in .env with your credentials (see below)
```

**Minimum required to run:**
```bash
ANTHROPIC_API_KEY=sk-ant-...
SLACK_BOT_TOKEN=xoxb-...
SLACK_APP_TOKEN=xapp-...
```

**Each data source needs its own key** — the bot runs gracefully without any of them (returns a helpful "not configured" message instead of crashing).

### 3. Create the Slack App

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From manifest**
2. Paste this YAML manifest:

```yaml
display_information:
  name: C1ProspectingBot
  description: SDR prospecting assistant powered by Claude
features:
  bot_user:
    display_name: C1ProspectingBot
    always_online: true
oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - chat:write
      - channels:read
      - channels:history
      - groups:read
      - groups:history
settings:
  event_subscriptions:
    bot_events:
      - app_mention
      - message.channels
      - message.groups
  interactivity:
    is_enabled: false
  socket_mode_enabled: true
  token_rotation_enabled: false
```

3. **Install to workspace** → copy **Bot User OAuth Token** → `SLACK_BOT_TOKEN`
4. **Basic Information** → **App-Level Tokens** → create token with `connections:write` → `SLACK_APP_TOKEN`
5. Invite `@C1ProspectingBot` to `#ai-prospecting`

### 4. Connect Google Drive (one-time)

1. [Google Cloud Console](https://console.cloud.google.com) → **APIs & Services → Library** → enable **Google Drive API**
2. **Credentials** → **Create OAuth 2.0 Client ID**
   - Type: **Desktop app**
   - Redirect URI: `http://localhost:9999/callback`
3. Copy Client ID and Secret into `.env`
4. Run `make cli` and type `auth google` — browser opens, sign in with Google SSO, done
5. Refresh token is saved automatically to `.env`

---

## Running

### Local CLI (testing — no Slack needed)

```bash
make cli
# or
./bin/bot -mode=cli
```

Type prompts at the `>` cursor. Try:
- `I'm Claire Sulek, show me my hottest accounts today`
- `Research Acme Corp`
- `Find contacts at Stripe`
- `Draft a cold email for the CISO at Cloudflare`
- `morning kickoff`
- `auth google` — connect Google Drive
- `tools` — show all tools and credential status

### Slack Bot

```bash
make run
# or
./bin/bot
```

In `#ai-prospecting`: `@C1ProspectingBot what are my hottest accounts this week?`

### Docker

```bash
make docker-run
# or
docker compose up --build
```

---

## Tools (21 total)

| Tool | Source | What it does |
|------|--------|-------------|
| `search_salesforce_accounts` | Salesforce | Find accounts by name |
| `check_salesforce_opportunities` | Salesforce | Gate: check for active opps before recommending |
| `get_salesforce_contacts` | Salesforce | Existing contacts at an account |
| `get_salesforce_account_activity` | Salesforce | Recent activity (inactivity = high priority) |
| `get_common_room_high_intent_accounts` | Common Room | Accounts with intent score ≥ 70 |
| `get_common_room_account_signals` | Common Room | "Why now" signals for a specific account |
| `get_common_room_contacts` | Common Room | Contacts with real engagement activity |
| `enrich_contact_lusha` | Lusha | Work email + phone for a single contact |
| `bulk_enrich_contacts_lusha` | Lusha | Batch enrich up to 5 contacts |
| `enrich_company_lusha` | Lusha | Firmographics: headcount, industry, revenue, tech stack |
| `enrich_contact_apollo` | Apollo.io | Work email, personal email, phone for a single contact |
| `bulk_enrich_contacts_apollo` | Apollo.io | Batch enrich up to 10 contacts |
| `list_gong_calls` | Gong | Recent call history for a company |
| `search_notion` | Notion | Search internal collateral and playbooks |
| `get_notion_page` | Notion | Read full content of a Notion page |
| `list_collateral_folder` | Google Drive | Full directory tree of the collateral folder |
| `search_google_drive` | Google Drive | Full-text search within the collateral folder |
| `read_google_drive_file` | Google Drive | Read a Google Doc, Sheet, or Slides file |
| `verify_employee_count` | Brave Search | Confirm headcount is in ICP range (1K–15K) |
| `search_company_news` | Brave Search | Buying signals: security news, leadership changes, compliance |
| `web_search` | Brave Search | General web lookup |

---

## Account Priority Scoring

| Priority | Criteria |
|----------|---------|
| 🔥 High | Common Room score 70+, signals in last 7 days, no SF activity 5+ months, no active opp |
| 🌡️ Medium | Score 70+, signals in last 14 days, SF activity 3–6 months ago |
| ❄️ Low | Score below 70, no recent signals |
| ⚠️ Skip | Active SF opportunity or Closed Won |

---

## Scheduled Morning Kickoff

Runs at **6am PT, Mon–Fri** by default. Posts a top-10 account list with intent signals, contacts, and outreach suggestions to `SLACK_CHANNEL`.

To test locally:
```bash
# In .env:
SCHEDULE_CRON=* * * * *   # fires every minute
```

---

## Docker Deployment

The Dockerfile uses a multi-stage build — final image is ~10MB (distroless base).

```bash
docker build -t c1prospecting-bot .
docker run --env-file .env c1prospecting-bot
```

Or with compose:
```bash
docker compose up -d
```
