# FeedWatch

[简体中文](README.md) ｜ **English**

A Go daemon: polls `releases.atom` feeds as configured and sends notifications when it discovers **new stable versions**. Notification channels include Resend email plus WeCom, Feishu, DingTalk, Telegram, Slack, Discord, Teams, Bark, ntfy and a generic webhook — **you can push to several channels at once**. Data sources, notification channels and state storage are all pluggable interfaces (both Atom/RSS and GitHub Releases are supported as sources).

**Two image variants**: the unsuffixed base image stays on pure manual `config.yaml` configuration (no frontend assets, no HTTP server, no SQLite); the `-webui` variant additionally provides a multi-user web admin UI, login/authentication and database storage (SQLite / MySQL / PostgreSQL), **starts from an empty database and needs no config file at all**, and prints the initial admin password once in the container logs. Both are isolated by the same `//go:build webui` switch — the base binary links no WebUI / SQLite code. See [WebUI Variant](docs/en/webui.md).

Three repositories are monitored by default (all verified working):

| Project | Atom feed |
| --- | --- |
| golang/go | `https://github.com/golang/go/releases.atom` |
| rust-lang/rust | `https://github.com/rust-lang/rust/releases.atom` |
| python/cpython | `https://github.com/python/cpython/releases.atom` |

<a id="toc"></a>

## Documentation Index

The main README keeps only the "project overview + core behavior + quick start"; the finer details on configuration, deployment, WebUI, releases and development are split by topic into the [`docs/en/` directory](docs/en/README.md) ([中文目录](docs/README.md)). The two sets correspond file-for-file. Links below point both to **sections inside this main file** and to **sub-files and sections inside them**.

**Inside this main file**

| Section | Contents |
| --- | --- |
| [Behavior](#behavior) | First-run baseline / silent restart / 304 / retry on failure / rate limiting |
| [Quick Start](#quick-start) | Local Windows exe build, generating `config.yaml`, dry-run debugging |
| [CLI Flags & Environment Variables](#cli) | Flag / env-var precedence, secrets read only from env vars |
| [Configuration at a Glance](#config-overview) | Config file layers, minimal edits to add a feed / channel |

**Detailed docs (`docs/en/`)**

| Document | Contents / direct links |
| --- | --- |
| [Configuration](docs/en/configuration.md#configuration) | feeds / filters / GitHub Releases source, version token extraction, baseline version |
| [Notification Channels](docs/en/notifications.md#notifications) | [Built-in channels](docs/en/notifications.md#built-in-channels) · [Generic Webhook template](docs/en/notifications.md#webhook-template) · [Partial failure policy](docs/en/notifications.md#partial-failure) |
| [Concurrency & Rate Limiting](docs/en/rate-limiting.md#rate-limiting) | Three throttling layers, bounded fetch concurrency, per-channel-type delivery gates |
| [Docker Deployment](docs/en/docker.md#docker) | compose / named volume, [bind mount ownership](docs/en/docker.md#bind-mount) · [rootless uid mapping](docs/en/docker.md#rootless-docker) |
| [WebUI Variant](docs/en/webui.md#webui) | [Startup](docs/en/webui.md#webui-start) · [Database backends](docs/en/webui.md#database-backends) · [Roles & permissions](docs/en/webui.md#roles) |
| [Accounts, Secrets & Data](docs/en/webui-accounts.md#accounts) | [Registration & third-party login](docs/en/webui-accounts.md#registration) · [Data, master key & backup](docs/en/webui-accounts.md#secrets-and-backup) |
| [Catalog & Remote Presets](docs/en/webui-catalog.md#catalog) | [Remote presets](docs/en/webui-catalog.md#remote-presets): sync rules, version / protection / tombstone, SSRF |
| [WebUI Development & Upgrade](docs/en/webui-development.md#webui-dev) | [Notification semantics](docs/en/webui-development.md#notification-semantics) · [Pages & local development](docs/en/webui-development.md#pages-and-dev) · [Upgrading from 1.0](docs/en/webui-development.md#upgrade) |
| [Publishing Images](docs/en/release.md#release) | [New release](docs/en/release.md#new-release) · [rc flow](docs/en/release.md#rc-flow) · [Prebuilt images](docs/en/release.md#prebuilt-images) · [Multi-arch](docs/en/release.md#multi-arch) |
| [Architecture & Extending](docs/en/architecture.md#architecture) | [Project layout](docs/en/architecture.md#project-layout) · [How to extend](docs/en/architecture.md#extending) |
| [Testing](docs/en/testing.md#testing) | Dual build-tag tests; base / WebUI / catalog / OAuth / presets suites |
| [Known Limitations](docs/en/limitations.md#limitations) | Single instance, rate limits, residual SSRF risk, read-only WebUI settings |

> Read the other language: [简体中文](README.md) ([Chinese docs index](docs/README.md)).

<a id="behavior"></a>

## Behavior

| Scenario | Behavior |
| --- | --- |
| **First start** (no state file) | Fetches all feeds and sends **1 summary notification with the "current version baseline"**, listing the latest stable version of each project; only after a successful send are all fetched entries marked as seen |
| **Every later round** | Notifies only newly added stable versions; `digest` (default) merges them into 1 notification per round, `per_feed` splits them per project (with the project name in the title) |
| **Container / process restart with no new version** | **Completely silent** — no notification channel is called |
| **First-round baseline** | **Always exactly 1 summary**, unaffected by `per_feed` |
| One batch fails to send | **Only the feeds involved in that batch are left for retry**: successfully delivered batches still commit seen IDs and ETags, so the next round will not push them again (at-least-once) |
| Partial failure across channels | Default `best_effort`: state is committed as soon as one channel succeeds (failed channels only log, no retry, no duplicate pushes); `strict` fails the whole batch and retries |
| One feed fails to fetch | That feed is skipped; other feeds notify normally; if any feed fails on the first round, the whole baseline notification is postponed |
| HTTP 304 | No diff, no change to the seen set, no notification |
| Prerelease / non-version tag | Not notified by default (`go1.27rc3`, `v3.16.0a1`, CPython's `3.10` branch tags, etc.), but the ID is still marked as seen |
| State directory not writable (e.g. wrong bind mount permissions) | **Fails at startup**: `jsonfile.New` probes once with `CreateTemp` and deletes it immediately, so it never sends a notification before discovering it cannot save state |
| Directory owner looks right yet still "not writable" | Most likely **rootless Docker**: container uids map to `100000+` on the host, unrelated to `id -u`; use a named volume or `user: "0:0"`, see [rootless Docker uid mapping](docs/en/docker.md#rootless-docker) |
| Many feeds / many notifications in one round | Fetching is bounded by `poll.concurrency` + paced by `poll.request_spacing`; channel delivery is paced by `notify.send_spacing` (**per channel type**, one gate shared by the same type), see [Concurrency & Rate Limiting](docs/en/rate-limiting.md) |

> Deduplication uses only the entry's stable ID (Atom `<id>`, the numeric GitHub release id), not the version number. GitHub's feed keeps only the latest ~10 entries, so intermediate versions missed during a long outage are never backfilled — this is intentional; use `type: github` (REST API, paginated) if you need older records.

<a id="quick-start"></a>

## Quick Start (local Windows exe, no Docker needed)

```bash
go mod tidy
go build -o feedwatch.exe ./cmd/feedwatch
```

`config.yaml` is a **local private file, already in `.gitignore`**. Generate it from the template on first use:

```bash
cp config.example.yaml config.yaml   # PowerShell: Copy-Item config.example.yaml config.yaml
```

> If you have GNU make, `make init` also works (idempotent, never overwrites an existing config). Git Bash shipped with Windows has no make by default.

The default template configures only one `email` (Resend email) entry in `notify.channels`: change `email.from` (must be a domain verified with Resend) and `email.to` under that entry to your own values. To push to WeCom / Feishu / Telegram etc., see [Notification Channels](docs/en/notifications.md) — replace or append entries in `channels` and put the corresponding webhook URL / token into environment variables.

```powershell
# 1) Safest: preview the baseline email, zero network sends, never touches the real state file
.\feedwatch.exe -config config.yaml -once -dry-run
.\feedwatch.exe -config config.yaml -once -dry-run   # second run: should be completely silent (verifies restart semantics)

# 2) Full local debugging: keep only "- type: log" in channels, still no real email
#    (copy config.yaml to config.local.yaml, change channels and store.path: data/state.local.json)
.\feedwatch.exe -config config.local.yaml -once

# 3) Real sending
$env:RESEND_API_KEY="re_xxx"
.\feedwatch.exe -config config.yaml -once    # sends one baseline email
.\feedwatch.exe -config config.yaml          # resident polling
```

`-dry-run` replaces the **entire notification fan-out** with a log notifier (so a multi-channel config cannot send anything by accident and needs no secrets at all) and switches the state file to `data/state.dryrun.json` (a separate file). You can therefore replay the baseline / silent / new-version paths repeatedly without polluting real state or accidentally suppressing the real baseline notification.

<a id="cli"></a>

## CLI Flags & Environment Variables

Precedence: **flag > environment variable > config.yaml > built-in default**.

| Flag | Environment variable | Description |
| --- | --- | --- |
| `-config` | `FEEDWATCH_CONFIG` | Config file path, default `config.yaml` |
| `-once` | — | Run one round and exit |
| `-dry-run` | `FEEDWATCH_DRY_RUN` | Print an email preview only, no sending, isolated state |
| `-state-path` | `FEEDWATCH_STATE_PATH` | Override `store.path` (must point inside the volume under Docker) |
| `-poll-interval` | `FEEDWATCH_POLL_INTERVAL` | Override `poll.interval`, e.g. `30m` |
| `-log-level` | `FEEDWATCH_LOG_LEVEL` | `debug/info/warn/error` |
| `-log-format` | `FEEDWATCH_LOG_FORMAT` | `text/json` (json recommended for containers) |
| `-version` | — | Print the version |

Secrets are read only from environment variables: `RESEND_API_KEY` (the name can be changed via the email channel entry's `email.api_key_env`), plus each channel's `*_WEBHOOK_URL` / `*_TOKEN` / `*_SECRET`. Write a `${NAME}` placeholder in the config; it is expanded at startup, and a missing value reports the variable name immediately. See `.env.example`. **Never put real secrets into YAML.**

<a id="config-overview"></a>

## Configuration at a Glance

- `config.example.yaml`: the **only config template committed to the repo**, with every field and comments.
- `config.yaml`: the local runtime config copied from the template, containing your domain, recipients and feed list; ignored by `.gitignore`, so editing it produces no git diff. `.env` and `config.local.yaml` are likewise uncommitted.
- Secrets are never written into YAML; they are read only from environment variables.
- Adding a monitored project = copy a `feeds` block and change `name` / `url`, **zero code changes** (full URL for Atom, `owner/repo` for GitHub Releases).
- Adding a notification channel = append a block in `notify.channels` and set its environment variables, **zero code changes**; several channels can coexist.

Full fields, filter rules and `type: github` usage: [Configuration](docs/en/configuration.md). Channel parameters: [Notification Channels](docs/en/notifications.md). Throttling: [Concurrency & Rate Limiting](docs/en/rate-limiting.md). Deployment: [Docker Deployment](docs/en/docker.md).

---

[Back to Documentation Index](#toc) · [简体中文](README.md)
