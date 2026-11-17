# WebUI Variant

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../webui.md)

<a id="webui"></a>

| | base | webui |
| --- | --- | --- |
| Image tag | version / `latest` | version plus `-webui` (e.g. `1.2.3-webui`), moving pointer is `webui` |
| Config source | reads `config.yaml` on every start | **does not read `config.yaml`**: starts from an empty DB, subscriptions / channels / secrets are all configured in the UI |
| UI / API | none | Vue 3 SPA + `/api/*`, listens on `:8080` by default |
| State | `data/state.json` (atomic JSON writes) | relational DB (SQLite WAL `/app/data/feedwatch.db` plus `-wal` / `-shm` by default, or MySQL / PostgreSQL) + `secret.key`; the DB also holds the subscription catalog (`catalog_entries`), review submissions (`submissions`), remote preset state (`catalog_preset_state`) and third-party login bindings (`user_identities` / `auth_providers`) tables (schema v8) |
| Users | single user (config is all subscriptions) | multi-user + admin, subscriptions and channels isolated per user |
| Secret source | environment variables (`${ENV_VAR}` expansion) | entered in the UI, AES-256-GCM encrypted into the DB, never readable back |

> **Run one or the other**: do not start `docker-compose.yml` and `docker-compose.webui.yml` at the same time. Each has its own poller and state, so the same releases would be notified twice.

<a id="webui-start"></a>

## Startup

```bash
cp .env.example .env                   # optional; channel secrets are not configured here
docker compose -f docker-compose.webui.yml up -d --build
docker logs -f feedwatch-webui         # the random initial admin password is printed here once
```

**No `config.yaml` and no channel secret variables are needed**: a first start is a clean, empty database with only the `admin` row in `users`, one set of default settings, 8 example catalog entries (GitHub repositories' `releases.atom`) and 3 categories. The examples are browsable templates only and **do not create subscriptions for anyone**; upgraded databases do not get them automatically, but they can be loaded under "Subscription Management → Import / Export". Open `http://<host>:8080` in a browser (port controlled by `WEBUI_HTTP_PORT` in `.env`), log in with the `admin` password from the logs — **the first login forces a password change**, which immediately invalidates all old sessions. An admin can create accounts or reset passwords under "User Management" (the new password is shown on screen once). **Self-registration is off by default**; enable it on the "Login & Registration" page (see [Registration & Third-party Login](webui-accounts.md#registration)).

When accessing through an HTTPS reverse proxy, set `FEEDWATCH_WEBUI_COOKIE_SECURE=true` in `.env`; for plain HTTP direct access keep it `false`, otherwise the session cookie carries the `Secure` flag, the browser will not send it back, and login fails. Session lifetime is controlled by `FEEDWATCH_WEBUI_SESSION_TTL` (default `168h`).

<a id="database-backends"></a>

## Database Backends

SQLite (`data/feedwatch.db`) is the default, but MySQL or PostgreSQL work directly as well. Switching is decided entirely by environment variables — **there is no database section in any config file**:

| Variable | Description |
| --- | --- |
| `FEEDWATCH_DB_DRIVER` | `sqlite` (default) / `mysql` / `postgres`; also accepts `mariadb` / `postgresql` / `pg` |
| `FEEDWATCH_DB_DSN` | The driver's own connection string; for sqlite it is the file path. Setting it stops `FEEDWATCH_DB_PATH` from being read |
| `FEEDWATCH_DB_PATH` | sqlite-only path, default `data/feedwatch.db` (backward compatible; equivalent to putting the path into the DSN) |
| `FEEDWATCH_DB_MAX_OPEN_CONNS` | Connection pool ceiling for network backends, default 10; ignored for sqlite (fixed single connection) |
| `FEEDWATCH_DATA_DIR` | Directory holding only `secret.key`; for sqlite it defaults to the DB file's directory, for network backends to `data` |

```bash
# MySQL 8.0.16+ (InnoDB / utf8mb4)
FEEDWATCH_DB_DRIVER=mysql \
FEEDWATCH_DB_DSN='feedwatch:secret@tcp(127.0.0.1:3306)/feedwatch?charset=utf8mb4' \
FEEDWATCH_DATA_DIR=/var/lib/feedwatch \
./feedwatch-webui

# PostgreSQL 12+
FEEDWATCH_DB_DRIVER=postgres \
FEEDWATCH_DB_DSN='postgres://feedwatch:secret@127.0.0.1:5432/feedwatch?sslmode=disable' \
FEEDWATCH_DATA_DIR=/var/lib/feedwatch \
./feedwatch-webui
```

- **Only empty databases are supported**; there is no tool to migrate existing SQLite data. Changing backends means a new database: the admin password is printed again and channel credentials must be re-entered in the UI.
- **`secret.key` and the database must be kept or discarded together**. Network backends have no DB file, so `secret.key` lives in `FEEDWATCH_DATA_DIR` — mount it as a volume and back it up with the DB; losing it makes every stored channel credential undecryptable, and the program **will not** regenerate it.
- All three backends use the same logical schema (18 business/catalog tables + `schema_migrations` + 17 indexes). MySQL has two implementation differences: it has no partial indexes, so the three "unique only for some rows" indexes are implemented equivalently with STORED generated columns plus ordinary unique indexes; `TEXT` cannot carry a literal default, so long text columns use `DEFAULT ('')` (a hard requirement since 8.0.13; with the `CHECK` constraints it effectively needs 8.0.16+). `feed_pool.url` has no UNIQUE constraint on MySQL — 2048 characters cannot be uniquely indexed under utf8mb4, and its identity is already carried by `url_key` (sha256).
- String comparison: MySQL tables use `utf8mb4_bin` to align with SQLite's byte ordering (the default `utf8mb4_0900_ai_ci` would treat `News` / `news` as the same category); PostgreSQL's `ORDER BY` follows the database collation, but all list sorting happens in the frontend, so no difference is visible.
- Passwords in connection strings never reach the logs: the startup line prints only `driver` and a redacted DSN.
- Migration versions are recorded in `schema_migrations`; SQLite also keeps maintaining `PRAGMA user_version`, so 1.1.x databases continue to work with no manual intervention.
- Still **single-instance**: the pool can be enlarged, but there is only one background orchestrator; multiple instances writing the same database produce duplicate deliveries.
- Dialect differences are all contained in `internal/webui/store`: `dialect.go` handles placeholder rewriting (PG's `$1`) and insert-ignore / upsert / returning-id generation; the three DDL files are `schema_sqlite.sql` / `schema_mysql.sql` / `schema_postgres.sql`, and `TestSchemaFilesAgree` pins their tables and columns to agreement. **New queries can be written as usual with `?` placeholders and plain SQL**; only constructs like `ON CONFLICT ... SET <literal>` need a dialect branch.
- To verify against real servers (when none are available locally): `FEEDWATCH_TEST_MYSQL_DSN=... FEEDWATCH_TEST_POSTGRES_DSN=... go test -tags webui -run TestNetworkBackend ./internal/webui/store/`. It creates tables, runs upsert / unique-conflict / partial-unique-index / Chinese round-trip checks, then DROP all tables — **point it only at a throwaway test database**.

<a id="roles"></a>

## Roles & Permissions

| | Admin (`admin`) | Regular user |
| --- | --- | --- |
| Subscriptions / channels | **No entry point**; calling the API directly returns 403 `role_not_allowed` | Create / edit / delete their own subscriptions and channels |
| Catalog / pending reviews | **No entry point**; calling the API directly returns 403 `role_not_allowed` | Browse the catalog, one-click subscribe / unsubscribe, submit and withdraw requests, view all pending links |
| Subscription management (sidebar submenu) | Five sub-pages: Entries (add/edit/disable/delete, preset status, unprotect), Review Queue, Categories, Import/Export, **Remote Presets Sync** | 403 `forbidden` |
| Channel secrets | Only "configured / missing" and a redacted hostname | Only which fields they filled in; values are never echoed |
| Other users' data | Read-only drill-down: subscriptions, channels, delivery records | None |
| Enable/disable others' resources | Can disable / restore a subscription or channel, **cannot edit contents** | None |
| User management | Create users, reset passwords, disable accounts | Can only change their own password |
| Self-registration / third-party login | Password login only; no binding entry point (403 `role_not_allowed`) | Can self-register, log in with GitHub / linux.do and bind accounts |
| Login & registration config | "Login & Registration" page: registration switch + Client ID / Secret for both platforms | Not visible |

**To configure subscriptions for yourself, create a regular user**: the admin is deliberately kept out of business configuration so operations accounts and subscription accounts never mix, and an admin cannot use their privileges to read others' webhooks / tokens. Write operations on drill-down pages accept only `{"enabled": bool}`; any extra field returns 400 `admin_readonly`.

The catalog's "updates require review" rule applies only to **regular users**: the admin is a trusted role, so adding or editing an entry under "Subscription Management → Entries" takes effect immediately without creating a pending request; new/update requests submitted by users must be approved or rejected by an admin on the "Review Queue" sub-page before reaching the catalog. The "Pending" page is open to all regular users but is **view-and-copy only** — no direct subscribing.

---

[← Back to main README](../../README_EN.md) · [简体中文](../webui.md)
