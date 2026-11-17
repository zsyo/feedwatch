# Known Limitations

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../limitations.md)

<a id="limitations"></a>

- Single-instance design: multiple instances writing the same state file race; the same applies to the WebUI variant (one orchestrator; SQLite additionally fixes `SetMaxOpenConns(1)`, while MySQL / PostgreSQL only make the pool tunable — concurrent instances still duplicate deliveries).
- Unauthenticated GitHub access is rate-limited (REST 60 req/h; GraphQL requires a token); the default 30-minute × 3 sources is about 6 req/h, with ETag/304, `poll.concurrency` / `request_spacing` peak shaving and exponential backoff on top. With a token, REST rises to 5000 req/h; with many GitHub subscriptions you can enable `github.graphql` to batch them in one GraphQL request (full data every round, no 304, automatically falls back to REST on failure).
- `From` must be a domain verified with Resend, otherwise sending returns 403.
- Under `best_effort`, a channel that keeps failing drops its subsequent notifications (leaving only ERROR logs) so other channels are not spammed with duplicates; use `strict` if no notification may be missed.
- Each channel has its own frequency cap (WeCom / DingTalk 20 messages/min, Feishu 100 messages/min and 5/s, Telegram about 1/s per chat, Teams 4/s; Resend defaults to 10/s per team). The default 30-minute polling + one digest per round is far below those limits; `notify.send_spacing`'s default 250ms is only a generic protection per channel type — switching `mode` to `per_feed`, adding many channels of the same type or shortening the interval still requires tuning to that type's strictest limit.
- Fetch pacing is global and does not distinguish domains; delivery pacing is per channel type; neither does cross-round quota budgeting (each period is computed independently).
- If the state file is corrupted, the program errors and skips that round; it does **not** silently treat it as a first run and resend the baseline email. Inspect `data/state.json` manually.
- WebUI "Global Settings" writes to the database and takes effect only **after a process restart**; `FEEDWATCH_WEBUI_COOKIE_SECURE` / `FEEDWATCH_WEBUI_SESSION_TTL` are read from env vars only at startup and are marked read-only in the UI.
- The WebUI variant's `-dry-run` is meaningless (ignored with one log line); `-once` runs one round of "fetch → build delivery plan → deliver" and exits.
- Catalog search / filtering / sorting is computed locally in the browser over the full catalog returned in one response; if the catalog grows significantly, move to backend pagination and **remove the local filter at the same time** (otherwise it only filters the current page).
- The "Pending" page shows pending URLs in plaintext to all regular users and lets them copy them, so the http/https, no-userinfo, no-sensitive-query validation on catalog URLs **must not be relaxed**; FeedWatch does not verify the content, availability or safety of those links — judge for yourself before manually copying and adding them.
- `presets.base_url` defaults to the official repository's `main` branch, and **those files only exist once pushed to GitHub**; local development or an unpushed tag necessarily 404s on sync, so temporarily point the base URL at a reachable mirror. Like the polling parameters, `presets.base_url` / `presets.timeout` require a **process restart** after saving.
- Remote preset SSRF protection judges by "network attributes" rather than a domain allowlist (admins must be able to switch mirrors): it rejects private / loopback / link-local / cloud-metadata IPs, but cannot defend against **DNS rebinding that happens on the proxy side**; when self-hosting a proxy or mirror, make sure the upstream is trustworthy.
- One sync writes the whole batch of presets in a single transaction, briefly holding the connection when there are many files (especially noticeable with SQLite's single connection); network IO and document validation are done before `Begin()` to shorten that hold. MySQL DDL commits implicitly, so only the initial table-creation migration runs outside a transaction; all other writes are still single transactions.
- If an admin changes a catalog entry's URL to match a remote preset, or two remote presets collide on a URL, the whole sync returns 409 `preset_conflict` and **rolls back entirely** (including entries that already succeeded); resolve the conflict and retry.
- A remote preset's `version` may only move forward: if upstream accidentally lowers it, the entry is silently skipped (counted under `skipped`) and local state is never rolled back; presets an admin deleted leave a tombstone, sync never resurrects them, and there is no UI to clear tombstones (edit `catalog_preset_state.deleted` directly if needed).
- Preset JSON and the `feedwatch-catalog` v1 format used by "Import" are two different formats; downloaded category files cannot be pasted into the import box. For an offline fallback, reshape them into the import format or point `presets.base_url` at a reachable mirror.

---

[← Back to main README](../../README_EN.md) · [简体中文](../limitations.md)
