# QX-Scraper — Backend

Go backend for the QX-Scraper job-discovery platform. It stores search
configurations defined by an admin, enriches them with AI (OpenRouter),
scrapes job boards on a cron schedule (Apify actors), filters the raw posts
against the config's keywords, and serves the resulting jobs over a JSON API
consumed by the [Next.js frontend](../frontend/README.md).

- **HTTP API:** [chi](https://github.com/go-chi/chi) v5
- **Database:** PostgreSQL via [pgx/v5](https://github.com/jackc/pgx) (Neon or local)
- **Scheduling:** [robfig/cron](https://github.com/robfig/cron) v3
- **Auth:** JWT ([golang-jwt](https://github.com/golang-jwt/jwt)) + bcrypt — single super-admin role
- **Scraping:** [Apify](https://apify.com) actors (one actor = one "source")
- **AI:** [OpenRouter](https://openrouter.ai) chat-completions (job-type enrichment + post classification)

---

## Table of contents

- [Architecture](#architecture)
- [Project structure](#project-structure)
- [Prerequisites](#prerequisites)
- [Quick start](#quick-start)
- [Configuration reference](#configuration-reference)
- [Database & migrations](#database--migrations)
- [Super-admin seeding](#super-admin-seeding)
- [HTTP API reference](#http-api-reference)
- [Scraping sources (Apify)](#scraping-sources-apify)
- [AI features (OpenRouter)](#ai-features-openrouter)
- [The pipeline](#the-pipeline)
- [Scheduler](#scheduler)
- [Testing](#testing)
- [Known issues](#known-issues)
- [Troubleshooting](#troubleshooting)

---

## Architecture

`main.go` boots in a fixed order:

1. **Config** — `.env` / environment variables are loaded and validated
   (`internal/config`). `DATABASE_URL` and `JWT_SECRET` are required.
2. **Database** — pgx pool is opened and embedded SQL migrations run
   automatically on every start (`internal/db`, `migrations/`).
3. **Seed** — the super admin is created *only if the database has none*.
4. **Dependencies** — OpenRouter client, Apify source registry, pipeline,
   and cron scheduler (re-loads every enabled search config).
5. **HTTP server** — chi router on `SERVER_ADDR` (default `:8080`) until
   `SIGINT`/`SIGTERM`, then a graceful 15 s shutdown.

Data flow for a scheduled run:

```
SearchConfig (DB, cron)
   └─▶ pipeline.Run
         ├─▶ collector: for each enabled source → Apify actor run → raw posts
         ├─▶ normalizer: per-source JSON → canonical job fields
         ├─▶ filter: keyword match / exclude keywords / employment & work-mode
         ├─▶ AI (optional): classify ambiguous posts, confidence ≥ 0.7
         └─▶ store: dedupe → INSERT jobs + SearchRun stats (raw/new/rejected)
```

## Project structure

```
backend/
├── main.go                  # entrypoint (boot order above)
├── migrations/              # embedded SQL migrations (go:embed)
│   ├── 0001_init.sql        # admins, search_configs, jobs, search_runs
│   ├── 0002_jobs_search_config_id.sql
│   └── migrations.go
└── internal/
    ├── ai/                  # OpenRouter client: enrichment + classification
    ├── api/                 # chi router, handlers, JWT middleware, CORS
    ├── auth/                # password hashing, JWT issue/verify
    ├── collector/           # source registry, Apify client, actor presets,
    │                        #   input templating, self-healing coercion, normalizer
    ├── config/              # env/.env loader + validation
    ├── db/                  # pgx pool + migration runner
    ├── model/               # shared structs (Job, SearchConfig, SearchRun, …)
    ├── pipeline/            # run lifecycle: fetch → filter → classify → store
    ├── scheduler/           # cron wrapper (per-config entries, run-now, status)
    ├── seeder/              # first-boot super-admin seeding
    └── store/               # all SQL queries
```

## Prerequisites

| Requirement | Notes |
|---|---|
| **Go 1.27+** | `go.mod` declares `go 1.27` |
| **PostgreSQL 14+** | local (`docker compose up -d`) or a hosted instance (e.g. Neon) |
| **Apify token** | optional — without it, only the mock source is available |
| **OpenRouter key** | optional — without it, AI enrichment/classification are disabled and the app runs on rules only |

## Quick start

```powershell
# 1. Database — either use local Postgres in Docker…
cd backend
docker compose up -d

# …or skip Docker and point DATABASE_URL at a hosted Postgres (Neon, RDS, …)

# 2. Configuration
Copy-Item .env.example .env      # then edit .env (see table below)
#   Minimum viable .env: DATABASE_URL, JWT_SECRET
#   Full features:      + APIFY_TOKEN, APIFY_ACTORS, OPENROUTER_API_KEY

# 3. Run
go run .
# or build a binary:
go build -o bin/qx-scraper.exe .
.\bin\qx-scraper.exe
```

The server listens on `http://localhost:8080` by default. Health check:

```powershell
curl http://localhost:8080/health
```

> **Note:** SQL migrations are compiled in with `go:embed`. A binary built
> *before* a migration was added will not know about it — after pulling
> changes with new migration files, **rebuild** (`go build -o bin/qx-scraper.exe .`).
> `go run .` always re-embeds.

## Configuration reference

All configuration comes from environment variables; `.env` is loaded
automatically if present (`godotenv`). Copy `.env.example` → `.env` to start.

| Variable | Required | Default | Description |
|---|---|---|---|
| `SERVER_ADDR` | no | `:8080` | HTTP listen address |
| `DATABASE_URL` | **yes** | — | PostgreSQL DSN, e.g. `postgresql://user:pass@host:5432/db?sslmode=require` |
| `SUPER_ADMIN_EMAIL` | first boot¹ | `admin@example.com` | Seeded super-admin email |
| `SUPER_ADMIN_PASSWORD` | first boot¹ | — | Seeded super-admin password (bcrypt-hashed) |
| `SUPER_ADMIN_NAME` | no | `Super Admin` | Display name |
| `JWT_SECRET` | **yes** | — | HMAC key for JWTs — use a long random string |
| `JWT_EXPIRY` | no | `24h` | Go duration string |
| `OPENROUTER_API_KEY` | no | — | Empty ⇒ AI features disabled (rules only) |
| `OPENROUTER_MODEL` | no | — | e.g. `openrouter/free` or `openai/gpt-4o-mini` |
| `AI_CLASSIFY` | no | `false` | `true` ⇒ AI classifies ambiguous posts during runs |
| `APIFY_TOKEN` | no | — | Empty ⇒ no Apify sources (mock only, if enabled) |
| `APIFY_ACTORS` | no | — | Comma-separated actors (see [below](#scraping-sources-apify)) |
| `APIFY_INPUTS` | no | — | JSON override of actor input shapes (rarely needed) |
| `MOCK_SOURCES` | no | `false` | `true` ⇒ register a local mock source for dev |
| `TZ` | no | `UTC` | IANA timezone used to interpret cron expressions |
| `RUN_TIMEOUT` | no | `30m` | Max duration of one run before cancellation |
| `SMTP_HOST` | no | — | SMTP server for user-invitation emails; empty ⇒ email disabled (credentials returned in the API response instead) |
| `SMTP_PORT` | no | `587` | STARTTLS port (Gmail app passwords, Mailgun, Postmark, SES SMTP) |
| `SMTP_USERNAME` | no | — | SMTP auth user (empty ⇒ no auth) |
| `SMTP_PASSWORD` | no | — | SMTP auth password |
| `SMTP_FROM` | no | `QX Scraper <no-reply@localhost>` | From header, `Name <addr>` form |
| `APP_URL` | no | `http://localhost:3000` | Frontend URL used for the sign-in button in invitation emails |

¹ Only used when the database contains no super admin — see
[Super-admin seeding](#super-admin-seeding).

## Database & migrations

- **Local:** `docker compose up -d` starts PostgreSQL 16 on `localhost:5432`
  (user/password/db: `postgres`/`postgres`/`qx_scraper`). Then set
  `DATABASE_URL=postgresql://postgres:postgres@localhost:5432/qx_scraper?sslmode=disable`.
- **Hosted:** paste any Postgres DSN into `DATABASE_URL` (Neon works as-is,
  including the pooler URL with `sslmode=require`).
- **Migrations** live in `migrations/*.sql`, are applied automatically at
  startup in filename order, and are tracked in a schema-migrations table.
  They are embedded into the binary via `go:embed` — **rebuild after adding
  or pulling new migrations**.

Tables: `admins` · `search_configs` · `jobs` · `search_runs`.

## Super-admin seeding

On first boot, if no super admin exists, one is created from
`SUPER_ADMIN_EMAIL` / `SUPER_ADMIN_PASSWORD` / `SUPER_ADMIN_NAME`
(password bcrypt-hashed). If a super admin already exists, **these values are
ignored and nothing is re-seeded** — change credentials from the Account page
(email/name/password) once logged in.

Additional users are created from the **Users** page (super admin only): the
super admin enters the person's email, the backend generates a password and
emails it to them as an HTML invitation (SMTP). Each user is either a
**manager** (`can_manage_configs: true` — may create/edit search configs) or
**view-only** (may only browse the job feed). Without SMTP configured, the
generated password is returned in the API response instead so it can be
shared manually.

## HTTP API reference

All responses are JSON. CORS is open (any origin). Everything except
`/health` and login requires `Authorization: Bearer <token>` (token from
`/api/auth/login`, 24 h expiry) — **the whole app is private**.

### Public

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Liveness check |
| `POST` | `/api/auth/login` | `{email, password}` → `{token, expires_at, admin}` |

### Any authenticated user (Bearer)

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/auth/me` | Current account (`admin` object incl. `can_manage_configs`) |
| `GET` | `/api/jobs` | List jobs. Query: `q`, `location`, `work_mode`, `employment_type`, `source`, `limit`, `offset` |
| `GET` | `/api/jobs/{id}` | Single job |
| `GET` | `/api/sources` | `{ sources: [...], ai: bool }` — registered source names + AI availability |
| `PUT` | `/api/admin/profile` | Change own email/name (`{email?, name?}`) → admin |
| `PUT` | `/api/admin/profile/password` | Change own password (`{current_password, new_password}`) |

### Managers (`can_manage_configs: true`)

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/admin/search-configs` | List configs (annotated with `running`) |
| `POST` | `/api/admin/search-configs` | Create config |
| `GET` | `/api/admin/search-configs/{id}` | One config |
| `PUT` | `/api/admin/search-configs/{id}` | Update config |
| `DELETE` | `/api/admin/search-configs/{id}` | Delete config (jobs remain) |
| `POST` | `/api/admin/search-configs/{id}/enable` | Enable + reschedule |
| `POST` | `/api/admin/search-configs/{id}/disable` | Disable + unschedule |
| `POST` | `/api/admin/search-configs/{id}/run` | Trigger a run immediately |
| `GET` | `/api/admin/search-configs/{id}/runs` | Run history for one config |
| `GET` | `/api/admin/runs` | Run history across all configs |
| `POST` | `/api/admin/search-configs/enrich` | `{job_type}` → AI-generated profile (keywords, excludes, locations, schedule) |

### Super admin only (user management)

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/admin/users` | List all accounts |
| `POST` | `/api/admin/users` | Create account `{email, name?, can_manage_configs}` — generates a password and emails it (HTML); response `{admin, emailed, password?, message}` (`password` only when the email could not be sent) |
| `GET` | `/api/admin/users/{id}` | One account |
| `PUT` | `/api/admin/users/{id}` | Update `{email?, name?, can_manage_configs?}` |
| `DELETE` | `/api/admin/users/{id}` | Delete (self and last super admin are blocked) |
| `POST` | `/api/admin/users/{id}/reset-password` | Generate + email a fresh password → same response shape as create |

Guards use fresh DB state per request, so permission changes or deletions
take effect immediately even while an old JWT is still valid.

## Scraping sources (Apify)

A "source" is one Apify actor. Configure them with `APIFY_ACTORS`, two forms
accepted (comma-separated, `/` or `~` actor-ID separators both work):

```dotenv
# 1. Bare actor ID — source name = last path segment ("job-search")
APIFY_ACTORS=hirebase/job-search

# 2. Explicit name — use for custom names or when derived names would collide
APIFY_ACTORS=li=curious_coder/linkedin-jobs-scraper,rd=trudax/reddit-scraper-lite
```

`GET /api/sources` returns the derived names; an admin selects which sources a
search config uses.

### Built-in input presets

Actor input schemas differ, so known actors ship with presets
(`internal/collector/collector.go`). Resolution order per run:

1. `APIFY_INPUTS` override (exact match on source name)
2. Built-in preset for the actor ID
3. Generic default (`keywords`, `locations`, `maxItems`, `jobType`…)

| Actor | Preset behavior |
|---|---|
| `hirebase/job-search` | `keywords`/`locations` arrays, `postedWithinDays` from lookback |
| `trudax/reddit-scraper-lite` | native `searches` field (no `startUrls`) |
| `curious_coder/linkedin-jobs-scraper` | **one search URL per keyword** + first location, `limitPerSource`, `datePosted` from lookback |
| `apidojo/tweet-scraper` | standard search input |

### Self-healing input coercion

When Apify rejects an input with a type error (`Field input.X must be string`,
`must be array`, an element `must be object`, including indexed paths like
`startUrls.0`), the collector coerces the value to the requested type and
retries the run automatically — most schema drift fixes itself. **Enum errors
are not retried** (the actor rejects a value, not a type).

### `APIFY_INPUTS` override (rarely needed)

JSON map of *source name → input object*; placeholders are substituted at run
time. Wrap the whole value in single quotes inside `.env`:

```dotenv
APIFY_INPUTS={'some-source':{"keywords":"{{keywords_str}}","pageCount":1}}
```

| Placeholder | Expands to |
|---|---|
| `{{keywords}}` / `{{locations}}` | JSON **array** of the config's keywords/locations |
| `{{keywords_str}}` / `{{locations_str}}` | comma-**string** |
| `{{query}}` | keywords joined with spaces |
| `{{job_type}}` | the config's job type |
| `{{since}}` | RFC3339 timestamp `now − lookback` |
| `{{lookback_hours}}` / `{{lookback_days}}` | **number** |

## AI features (OpenRouter)

Two jobs, both no-ops when `OPENROUTER_API_KEY` is empty:

1. **Enrichment** (`POST /api/admin/search-configs/enrich`, also used when a
   config is created without keywords): the model turns a job type into a
   full search profile — keywords, exclude keywords, locations, employment
   type, work mode, sources (restricted to registered ones), lookback, cron.
   The response is sanitized server-side:
   - lists lowercased/deduped, keywords capped at 20;
   - **spelling variants are expanded deterministically** — literal keyword
     matching never cross-matches spellings, so `frontend developer` pulls in
     `front end developer`, `fullstack developer` → `full stack developer`,
     `nodejs developer` → `node js developer`, etc.;
   - **bare tech words** are added for role phrases of unambiguous tools
     (`golang developer` → `golang`, also `solidity`, `figma`, `react`,
     `rust`, `web3`) so titles like "Golang Engineer" still match;
   - **self-defeating excludes are dropped**: an exclude term that appears
     inside a keyword (e.g. `contract` vs `smart contract`) would veto every
     matching job, so it is removed.
2. **Classification** (`AI_CLASSIFY=true`): posts that pass the rule filters
   but look ambiguous are scored; `ai_confidence < 0.7` → rejected. Decisions
   are logged with the rejection reason.

## The pipeline

Per run, per source (`internal/pipeline`):

1. **Fetch** — collector starts an Apify run for the config's keywords,
   locations and lookback; raw items are counted (`raw_count`).
2. **Normalize** — source-specific JSON → canonical job fields. Common
   aliases are mapped (`applicationLink`/`jobBoardLink` → `application_url`,
   `descriptionText` → `description`, `locationType` → `work_mode`, …);
   relative posted dates ("2 days ago") are resolved.
3. **Filter** — keyword matching is **strict full-phrase** against
   title + company + location + description (case- and
   punctuation-insensitive: `Node.js` matches `nodejs`). Exclude keywords and
   employment/work-mode preferences are applied; each rejection increments a
   per-reason counter in the run stats.
4. **Classify** (optional) — AI scores ambiguous survivors.
5. **Store** — dedupe against existing jobs (`source` + `source_post_id`);
   new/updated rows are upserted and `last_seen_at` refreshed. Run stats
   (`raw_count`, `new_jobs_count`, `duplicate_count`, `rejected_count`,
   `status`, `error`) are written to `search_runs`.

**Note the asymmetry:** some actors search with OR/fuzzy semantics upstream
(e.g. `hirebase/job-search`), while this pipeline requires the full phrase —
a run can legitimately end as `raw: N, rejected(no_keyword_match): N`.

## Scheduler

- One cron entry per **enabled** config, using `TZ` to interpret the
  expression (5-field, standard syntax, e.g. `0 */6 * * *` = every 6 h).
- Enable/disable/run-now endpoints reschedule entries live; no restart needed.
- Each run is bounded by `RUN_TIMEOUT` and its context is cancelled on
  shutdown; the scheduler waits up to 10 s for in-flight runs to finish.

## Testing

```powershell
cd backend
go vet ./...
go test ./...
```

Per-package:

```powershell
go test ./internal/collector/ ./internal/pipeline/ ./internal/config/ ./migrations/
go test ./internal/ai/          # uses httptest servers; no real API key needed
```

> **Windows / Application Control note:** in environments where WDAC blocks
> executables from `%TEMP%`, `go test` fails with
> `fork/exec …\Temp\go-build…\*.test.exe: An Application Control policy has
> blocked this file`. Compile the test binary into `backend\bin\` and run it
> from there instead:
>
> ```powershell
> go test -c -o bin\ai.test.exe ./internal/ai
> & ".\bin\ai.test.exe" "-test.v"
> ```

## Known issues

- **LinkedIn `datePosted` enum:** the `curious_coder/linkedin-jobs-scraper`
  preset currently sends relative-second values (`r86400`, …) but the actor
  only accepts `"anyTime" | "past24Hours" | "pastWeek" | "pastMonth"` — the
  LinkedIn source fails with an Apify 400 until the preset maps to those
  enums. Other sources are unaffected.
- **`hirebase/job-search` rejection rate:** its OR-style search returns items
  that fail the strict phrase filter (see [The pipeline](#the-pipeline)) —
  expect high `no_keyword_match` counts unless keywords are single, common
  words.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `config: DATABASE_URL is required` | `.env` missing or empty — copy `.env.example` → `.env` |
| `column … does not exist` on startup | Stale binary without the latest embedded migration — **rebuild** |
| `401` on admin endpoints | JWT expired (24 h) — log in again |
| No sources in the config form | `APIFY_TOKEN`/`APIFY_ACTORS` empty; with `MOCK_SOURCES=true` you'll see the mock source only |
| AI enrichment returns profile unchanged / fails | `OPENROUTER_API_KEY` empty or model unavailable — check backend logs (`"AI … disabled"` warning) |
| Runs stay empty / fail on one source | Check `search_runs.error` on the dashboard; type errors self-heal, enum/credential errors do not |
| Port already in use | Change `SERVER_ADDR`, e.g. `SERVER_ADDR=:8081` |
