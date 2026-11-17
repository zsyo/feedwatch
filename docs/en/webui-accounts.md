# Accounts, Secrets & Data

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../webui-accounts.md)

<a id="accounts"></a>

<a id="registration"></a>

## Registration & Third-party Login

Two entry points, both disabled by default, enabled by an admin on the "Login & Registration" page:

| Entry point | Behavior once enabled |
| --- | --- |
| Self-registration | A "Register" link appears on the login page; a visitor enters a username + password (≥8 chars), a regular account is created and **logged in immediately**, without admin approval |
| GitHub / linux.do login | A "Sign in with X" button appears. The first login with a given third-party account **creates and binds an account automatically**; later clicks just sign in. Logged-in users can bind/unbind under "Account Settings → Third-party Login" |

- **Disabling self-registration also disables automatic third-party account creation**: both entry points write to `users` and share the single `webui.allow_registration` switch. With it off, a third-party account never bound to a local one returns to the login page with "self-registration is not open".
- **Credentials live in the DB, encrypted**: `auth_providers` has one row per platform; `client_secret` is sealed with the master key in a `fwsec:v1:` envelope (AAD bound to the platform name), never echoed by any endpoint, and after saving the UI shows only "saved; leave blank to keep unchanged". Rotating the master key only invalidates these two secrets — re-enter them; already bound users are unaffected.
- **The callback URL is derived from the request and must match what is registered with the platform**: `{current access address}/api/auth/oauth/{github|linuxdo}/callback`. The admin page displays it with a one-click copy. Behind a reverse proxy, the proxy must pass `X-Forwarded-Proto` and `X-Forwarded-Host`, otherwise it is computed as `http://` or an internal address. GitHub uses an OAuth App (`Settings → Developer settings → OAuth Apps`) with only the `read:user user:email` scope; linux.do is registered at "My Applications" on `connect.linux.do` with scope `user`.
- **PKCE is used only where supported**: GitHub sends `code_challenge` (S256) and `code_verifier`; linux.do Connect's docs have no PKCE, so only `state` guards against forgery.
- **`state` is a row in the DB** (`oauth_flows`, storing only a sha256 hash): single-use, expires after 10 minutes, deleted before validation on callback, so replays fail immediately. In the "bind" flow the target user is written into that row **when the flow starts**; the callback URL carries no user identifier, so tampering with parameters cannot bind someone else's third-party account to yours.
- **Admin accounts are entirely excluded**: `GET /api/me/identities` and the binding entry points use `accessMember`, so an admin gets 403 `role_not_allowed`; even if a binding pointing at an admin exists in the DB, the callback rejects the login with `admin_not_allowed`. Admins can only use username + password.
- Third-party-created accounts have no password, so the "Change Password" page shows "Set a login password" for them and **does not ask for the old one** (there is nothing to verify; the credential is the current session). Account settings also **refuse to unbind the last login method**: unbinding the only third-party account while no password exists is rejected (400 `last_login_method`).
- Login names are derived from the third-party username (keeping only `[a-zA-Z0-9._-]`, max 24 chars, min 2); on collision `-2` … `-10` is appended in turn. Chinese usernames degrade to usable names like `github-user` / `linuxdo-user`. Third-party email is display-only (linux.do does not return one).
- The two public write endpoints are rate-limited per IP: login 10/min, registration 10/min, OAuth start/callback 20/min.

<a id="secrets-and-backup"></a>

## Data, Master Key & Backup

- Both the database and the master key live in the named volume `feedwatch-webui-data:/app/data`. SQLite WAL needs the **directory** to be writable, with the same `CreateTemp` startup probe as base; when switching the volume to a bind mount, use `FW_UID` / `FW_GID` in `.env` for the host owner as well — **rootless Docker is the exception**, where container uids are not host uids: either keep the named volume or write `user: "0:0"`, see [rootless Docker uid mapping](docker.md#rootless-docker).
- **`secret.key` and the database must be backed up and discarded together**. The first start creates 32 bytes with `O_CREATE|O_EXCL` (0600) for the envelope encryption of channel secrets: `fwsec:v1:` + `base64.RawURLEncoding(nonce ‖ ciphertext ‖ tag)`, with AAD bound to the channel id, so copying a ciphertext into another channel's row cannot decrypt it. A read failure **never regenerates the key** (that would turn every stored credential into undecryptable garbage): startup fails immediately with three recovery options; an invalid length or bad base64 is likewise refused outright. To manage it yourself, set `FEEDWATCH_WEBUI_SECRET_KEY` (standard base64 of 32 bytes), which overrides the key file — **do not change it once set**.
- A new user's new subscriptions establish their baseline from the current global seen set, so **no historical versions are backfilled**; deleting and re-subscribing behaves the same.
- **The catalog is only a template**: three tables, `catalog_categories` / `catalog_entries` / `submissions`, with `category_id IS NULL` meaning "uncategorized". One-click subscribe copies a catalog URL into the user's own subscription; later edits, disabling or deleting the catalog entry **do not affect users already subscribed**. Disabling only hides it from the catalog while still occupying `url_key` (the global unique index includes disabled rows) — releasing a URL requires deleting the entry.
- New/update requests submitted by users first go into `submissions` (`pending`); **only admin approval writes to `catalog_entries`**. Rejection must include a reason; withdrawal keeps a `withdrawn` history. Update requests store the original values as a `before_json` snapshot at submission time, and the review page and "My Submissions" show a before/after comparison. `submissions` rows are never physically deleted and carry `reviewed_by` / `reviewed_at` / `reject_reason`.
- Catalog URLs are **shown to all regular users and can be copied**, so input validation is stricter than for ordinary subscriptions: only http/https, no userinfo, no sensitive query keys such as `token` / `secret` / `key` / `password` / `auth` / `sig`, 2048-character limit; outbound responses are redacted once more. Rejected URLs can be resubmitted (the unique index applies only to `pending`); each user may have at most 10 pending requests.
- "Subscription Management → Import / Export" moves catalog metadata only (categories matched by name, no ids), with `skip` (default) / `overwrite` / `error` (roll back the whole batch on duplicates) conflict strategies; import **writes only the catalog tables**, never `subscriptions` / `feed_pool` / `user_seen`, and carries no channel credentials or delivery state. Deleting a category sets its entries and requests to "uncategorized" (FK `ON DELETE SET NULL`, in the same transaction).
- The "Global Settings" page can change polling interval, timeout, concurrency, fetch/send spacing, HTTP parameters, etc.; the server validates each item before writing to the DB, and changes **take effect after a process restart** (no hot reload).
- `webui.cookie_secure` / `webui.session_ttl` are read-only items controlled by environment variables; `notify.mode` no longer has a global row in WebUI — it is an **account-level** preference (`users.notify_mode`), set at the top of the "Notification Channels" page and applying to all of that user's subscriptions and channels together.
- Channel forms are served by the backend: `GET /api/channel-fields` returns the field table for each channel type (key, required, type, options, whether it is a secret), and the frontend renders from that schema. Adding a channel type only requires editing `ChannelFields` in `internal/webui/channelspec.go`; the pages need no change.
- **Display timezone follows the account**: default "Follow server" (the container's `TZ`, `Asia/Shanghai` by default in compose), or any IANA timezone chosen under "Account Settings" and stored in `users.timezone`, synced automatically when logging in from another device. It affects rendering only: all timestamps in the DB remain RFC3339 UTC, and changing the timezone rewrites no data. To set your own default, change `TZ` in `.env`.
- Times in the UI are uniformly rendered as `YYYY-MM-DD HH:mm:ss` (no `T` / `Z`), converted into the selected timezone; `GET /api/meta` also returns `server_timezone` and `server_utc_offset`, so even logged-out pages can label the server default. The WebUI variant additionally `import _ "time/tzdata"`, so IANA names resolve even in a minimal image without tzdata.

---

[← Back to main README](../../README_EN.md) · [简体中文](../webui-accounts.md)
