# WebUI Development & Upgrade

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../webui-development.md)

<a id="webui-dev"></a>

<a id="notification-semantics"></a>

## Notification Semantics

- Each URL is fetched only once globally per round (`url_key = sha256(normalized URL)`); afterwards results are routed per user by their own filters and `always` / `on_update` policies. `digest` / `per_feed` is decided by the **account**, not the subscription: all of one user's subscriptions share a single form, so a round never produces both "one summary" and "one per project" for the same user.
- One-click subscribe in the catalog uses **exactly the same three-write transaction** as manual creation (`subscriptions` + `feed_pool` + copy of the current `user_seen` baseline), likewise starting from the current baseline with no historical backfill; it is therefore "copying a template", not referencing one. The catalog's "subscribed" badge is decided by `url_key`, so after a user edits the URL under "My Subscriptions", that catalog card shows as unsubscribed again.
- **No cross-user merging**: one independent request per user × per channel, with each user's own credentials, so every user sees their own sends in backends like Resend. Rate limiting is computed **per channel type** (one independent gate for email / telegram / wecom …); different services do not block each other.
- Channel secrets are **encrypted into the DB; no environment variable is read at run time**. The page submits the field values, and the server `Seal`s them into `channel_secrets`; list and drill-down endpoints return only `secret_fields` (which fields are configured), `secret_hints` (just `scheme://host`) and `secret_state` (`ok` / `missing` / `error` / `none`) — no plaintext and no `fwsec:` envelope ever appears in a response or log. When editing, **blank = keep unchanged**; to clear, pass `clear_secrets` explicitly; putting a secret into an ordinary field is rejected with 400 `secret_in_options`.
- Notification content is materialized into the DB right after a successful fetch; a delivery failure only retries the already-rendered delivery group — it does not re-fetch feeds or block the global seen set.

<a id="pages-and-dev"></a>

## Pages & Local Development

Pages: `/login`, Overview, **Catalog `/catalog`** (sidebar placed before "My Subscriptions"), My Subscriptions, Notification Channels, Account Settings; **Pending `/catalog/pending`** is a sub-page inside the Catalog, entered from the "Pending links" link in the catalog header and navigable back — it is no longer a separate sidebar item, and while on it "Catalog" stays highlighted in the sidebar. Admins additionally have User Management (including a per-user drill-down at `/admin/users/:id`, read-only + enable/disable), **Subscription Management**, Poll Pool, Rate Limit Status, Global Settings and Audit Log. Subscription Management is a **contextual group** in the sidebar: its 5 indented sub-items expand only while on `/admin/catalog*`; other admin pages keep the short 8-item sidebar. The parent "Subscription Management" is only a group entry (never highlighted as the current page), with the current sub-item highlighted on its own. The old `/admin/catalog` address remains as a compatibility redirect to "Entries", so old bookmarks and the Overview shortcut keep working. Regular users see no admin entry points; admins see no subscription / channel / catalog entry points — backend role guards enforce both directions.

The catalog supports keyword search (name / URL / description), category filtering, and sorting by subscriber count / added time / name (category lookup is delegated to the adjacent category dropdown; keywords no longer match category names); cards show "Subscribed" and an unsubscribe action directly. Below the page are "Submit a new subscription" and "My Submissions" (status, rejection reason, before/after comparison, withdraw). The Pending sub-page is read-only, offering only "Copy info" with a persistent risk notice. Subscription Management is split into 5 focused sub-pages, each with one or two cards: **Entries `/admin/catalog/entries`** (manual add + entries table with edit / disable / delete / preset status / unprotect, plus category filter and sorting by ID / subscriber count ascending / descending), **Review Queue `/admin/catalog/reviews`** (pending and full history, before/after comparison, approve / reject with reason), **Categories `/admin/catalog/categories`** (add / rename / delete), **Import / Export `/admin/catalog/transfer`** (download JSON, conflict strategy, import results), **Remote Presets `/admin/catalog/presets`** (sync button, per-file results, nearby explanation of sync rules). The sub-pages share response types, request wrappers, notice state and submenu definitions in `web/src/catalogAdmin.ts`, but each owns its own error / success notices so a message from the previous page does not carry over. The Entries page defaults to ascending ID order — the backend actually returns name order, which is why the ID column used to look like it skipped values; you can switch to "Subscriber count" and toggle ascending / descending, and the title shows "matched / total" after filtering. Filtering and sorting are computed **locally in the frontend** (the full catalog arrives in one response), consistent with the existing list pages.

```bash
make build-webui                 # or sh web/build.sh; output goes to internal/webui/embedded/ (gitignored)
go build -tags webui -o feedwatch-webui.exe ./cmd/feedwatch
FEEDWATCH_DB_PATH=data/feedwatch.db FEEDWATCH_HTTP_ADDR=127.0.0.1:8080 ./feedwatch-webui.exe
FEEDWATCH_DB_DRIVER=postgres FEEDWATCH_DB_DSN='postgres://fw:fw@127.0.0.1:5432/fw?sslmode=disable' ./feedwatch-webui.exe
./feedwatch-webui.exe -once      # offline smoke test: fetch one round → build delivery plan → deliver one round → exit
```

In PowerShell use the equivalent `web/build.ps1` (works with both Windows PowerShell 5.1 and PowerShell 7), with no need for `sh` / MSYS:

```powershell
.\web\build.ps1                  # output also goes to internal\webui\embedded\
.\web\build.ps1 -SkipDeps        # never touch dependencies (equivalent to FW_SKIP_DEPS=1)
.\web\build.ps1 -ForceDeps       # force npm ci, rewrites node_modules (equivalent to FW_FORCE_DEPS=1)
.\web\build.ps1 -OutDir .\out    # change the output directory
```

Both entry points share the same fingerprint and `BUILD_ID` rules and can be used interchangeably: dependencies installed under `sh` are not considered stale when building with `ps1`, and vice versa.

The WebUI variant **does not accept `-config`**: passing it only logs `-config is ignored; the WebUI variant is configured entirely in the browser` and continues; `-dry-run` is likewise meaningless. A clean clone also compiles with `-tags webui`, but the page shows only a placeholder (the repo commits just `embedded/stub.html`); the real frontend is produced by `make build-webui` or the Node stage of `Dockerfile.webui`.

Frontend builds need Node **22.12+** (Vite 8's `engines` requirement; `node:22-alpine` satisfies it); dependency versions are in `web/package.json`, and `npm run typecheck` runs `tsc --noEmit` on its own. After changing frontend dependencies, rerun the build script (`sh web/build.sh` or `web/build.ps1`) before `go build -tags webui`, otherwise the old assets are still embedded.

`sh web/build.sh` and `web/build.ps1` **no longer rebuild `node_modules` every time**: they fingerprint the contents of `package.json` / `package-lock.json` together with the Node platform (`platform-arch-abi`) into `web/node_modules/.cache/feedwatch-deps`. If the fingerprint is unchanged, installation is skipped; if it changed, `npm install` incrementally fills the gap (switching the same directory between Windows and WSL changes the platform fingerprint, so each platform's binary packages are added separately). A full `npm ci` happens only when the directory does not exist or `FW_FORCE_DEPS=1` (`-ForceDeps` for ps1) is set; `FW_SKIP_DEPS=1` (`-SkipDeps` for ps1) never touches dependencies at all.

The `BUILD_ID` in the output (`build_id` from `/api/meta`, shown in the sidebar and Overview) looks like `11142242-2f04086`: local build-machine month/day/hour/minute + a short git hash, without seconds, so two builds can be compared at a glance (image builds run in UTC, so Docker artifacts carry UTC timestamps).

> WebUI is still **single-instance**: SQLite uses `SetMaxOpenConns(1)` plus one background orchestrator; multiple instances writing the same database cause write races and duplicate deliveries.

<a id="upgrade"></a>

## Upgrading from WebUI 1.0 and Earlier

Since 1.1.0 `config.yaml` is no longer read, and channel secrets are entered in the UI and stored encrypted. Old databases still open (`PRAGMA user_version` migrates from 1 to 2, only adding tables such as `channel_secrets`, leaving old data untouched), but **old channels still hold environment variable names / placeholders that cannot be converted into ciphertext automatically** — re-enter the credentials once on the "Notification Channels" page, otherwise `secret_state` shows `missing` and delivery reports missing credentials.

Migrations are additive: each step only adds tables / columns or renames columns; existing tables are never rebuilt and business data is never touched (since v7 the version is recorded in `schema_migrations`, while SQLite also maintains `PRAGMA user_version` for old databases):

| schema version | Change |
| --- | --- |
| v2 | `channel_secrets` and other channel-secret tables |
| v3 | `users.timezone` (per-account display timezone) |
| v4 | `users.notify_mode` (notification form becomes account-level); the leftover `subscriptions.mode` column in old DBs is neither read nor written |
| v5 | `catalog_categories` / `catalog_entries` / `submissions` (catalog) |
| v6 | `catalog_preset_state` table + `preset_key` / `preset_version` / `preset_edited` columns and partial unique index on `catalog_entries` (remote presets) |
| v7 | MySQL / PostgreSQL support: `settings.key` → `setting_key`, `notifications.text` → `body` (the two names are respectively a reserved word and a type name in MySQL); version moves to the `schema_migrations` table |
| v8 | Third-party login: `user_identities` (one binding per platform), `auth_providers` (credentials, Secret encrypted), `oauth_flows` (one-time state, hash only) |

v8 **only adds three tables** and does not touch `users` (third-party-created accounts store an empty string in `password_hash`, so password login naturally fails); after upgrading, an old DB simply has three extra empty tables, with registration and both platforms still disabled.

v7 only renames two columns (SQLite's `ALTER TABLE ... RENAME COLUMN`, existing data preserved verbatim) and changes no business semantics; the reason is that `key` is a reserved word and `text` a type name in MySQL, so keeping them would require dialect-specific quoting everywhere.

v5 **only adds three tables**, so upgraded databases do not automatically gain the example catalog (seeding runs only when `users` is empty); to preload examples, load a JSON exported from a fresh DB under "Subscription Management → Import / Export". v6 uses `ensureColumn()` to add three columns to old DBs' `catalog_entries` (defaults `''` / `0` / `0`) and creates the partial unique index `WHERE preset_key <> ''` only after those columns exist (existing rows all have empty strings, so they are unaffected); **no table rebuild, no changes to business data**. The first "Fetch Remote Presets" click after upgrading claims existing example / manual / imported entries by URL and upgrades them by version, never inserting duplicate rows. Everything else works immediately after upgrading with no manual data edits. For backups, take the database and `secret.key` together — catalog and preset state are in the same DB.

If you do not want to keep old data, just delete the volume and start over (`docker compose -f docker-compose.webui.yml down -v`); first start regenerates the admin password and a new key. **The `data/` volume and `secret.key` must be kept or discarded together** — keeping only one is meaningless.

---

[← Back to main README](../../README_EN.md) · [简体中文](../webui-development.md)
