# Testing

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../testing.md)

<a id="testing"></a>

```bash
go test -race -count=1 ./...
go vet ./...
gofmt -l ./cmd ./internal

# WebUI variant: same code, different build tag
go test -race -count=1 -tags webui ./...
go vet -tags webui ./...
go build ./... && go build -tags webui ./...
go list -deps ./cmd/feedwatch | grep -E 'modernc|/webui|database/sql'   # should produce no output
sh web/build.sh                 # frontend assets must be generated to see the real UI (PowerShell: .\web\build.ps1)
```

Unit tests use in-memory `httptest` networks to simulate feeds and every channel webhook (200/204/202/304/429/5xx/malformed XML/vendor error codes), temp files for atomic state writes, a fake sender for Resend requests (including multiple recipients merged into 1 call), and a programmable fake source covering the watcher's first-round baseline / silent restart / retry on failure / single-feed isolation / 304 semantics / `per_feed` partial-batch failure retrying only failed projects / fixed summary baseline / bounded fetch concurrency; plus deterministic-clock cases for the `pace` gate, Feishu/DingTalk HMAC signature vectors, environment variable expansion, and fan-out failure-policy regressions. `go test -race` covers the new concurrent paths. **Tests never send real emails or real group messages.**

WebUI-specific cases additionally cover: bcrypt / session / CSRF and cookie attributes, privilege escalation and cross-user data isolation, URL dedup fetching only once, `always` / `on_update` routing, per-user independent delivery and per-channel-type pacing, delivery failure retry and abandonment, admin settings validation and auditing, static asset headers and JSON 404 for unknown `/api/*`. The secret chain is tested separately: envelope round-trip with random nonce, ciphertext / AAD tampering, unknown prefix, rejection of lengths 31 / 33, `LoadOrCreate` creating a 0600 file and **not overwriting** an existing corrupted one, env-var precedence, bad base64 never written to disk; API-level assertions that created secrets land in the DB as `fwsec:v1:`, that no response contains plaintext, that a blank edit keeps the value, that admin drill-down is read-only (an extra field yields 400), and that seeding creates only admin with all business tables empty. Both builds run the same base regression suite, guaranteeing WebUI changes do not contaminate base behavior.

Catalog-specific cases: v4→v5 migration adds only three tables while preserving user data, FK `ON DELETE SET NULL`, the partial unique index on `pending` (resubmission after rejection / withdrawal), example catalog being a template only (no writes to `subscriptions` / `feed_pool` / `user_seen`), catalog URL protocol / userinfo / sensitive query / length and alias normalization validation, bidirectional admin/member role guards, **one-click subscribe copying the baseline without replaying history**, dedup abuse prevention and pending-request cap, both new and update review flows (including the `before_json` snapshot, `entry_gone` / `conflict_url`), withdrawal, visible rejection reasons, category delete cascade, admin manual edits producing no request, import/export round-trip and all three conflict strategies. Registration & third-party login cases: 403 when the switch is off, creating an account and logging in immediately when on (`must_change_password=0`, `password_set=true`), duplicate name 409, weak password and invalid username 400; the full OAuth flow runs against a local fake provider — with no config, start only returns `not_configured`; with registration off, an unknown account is rejected and **no `users` row is written**; with it on, an account is created and a second login reuses it; state replay is rejected as invalid; after binding, the third-party login reaches the same account; unbinding the last login method is refused; after setting a password, unbinding works; admin calls to `/api/me/identities` and `mode=bind` both get `admin_not_allowed`; admin-side assertions that the Client Secret is not echoed after saving, a blank save does not overwrite, clearing credentials also disables the platform, and incomplete credentials prevent enabling. `internal/webui/oauth` also has authorize URL parameters (including PKCE appearing only for GitHub), token error branches and three failure responses, and a missing-id identity case.

After frontend changes, also run `npm run typecheck` in `web/`, then rerun the build script before `go build -tags webui`.

Remote preset cases: `presets/*.json` in the repo are validated with the production validator itself (to keep typo'd keys / duplicate URLs out of release data), plus manifest and category file format/filename/category drift, cross-file `preset_key` and `url_key` dedup, SSRF protocol / port / userinfo / private & cloud-metadata IP / DNS resolution re-check / per-hop redirect re-check / hop limit / 1 MiB size cap / context cancellation, loader per-file status and "no writes after a failed file", and v5→v6 column additions with the partial unique index; API-level coverage for upgrade-only, `preset_edited` still 0 after consecutive multi-version upgrades, repeated same-version sync, distinguishing claims of existing entries, protection set by admin edits and approved reviews, `accept-remote` unlocking, tombstones never resurrecting, fetch failure and `preset_invalid_document` producing zero writes, **rolling back earlier updates too** when a `url_key` conflict occurs inside the transaction, the error when a loader is missing, and the catalog list exposing the three preset status fields. All of it uses local `httptest` servers; no real network requests.

After changing the workflow, a static check is recommended (install with `go install github.com/rhysd/actionlint/cmd/actionlint@latest`):

```bash
actionlint .github/workflows/docker-publish.yml
```

---

[← Back to main README](../../README_EN.md) · [简体中文](../testing.md)
