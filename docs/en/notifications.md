# Notification Channels

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../notifications.md)

<a id="notifications"></a>

`notify.channels` is the only way to configure notifications: each item in the list is one channel, and the same notification is delivered in turn to every `enabled` channel without interfering with the others. To push to just one channel, write one item (a single-element list); there is no second config style. **The outer `notify` block holds only global settings** (`mode` / `send_spacing` / `subject_prefix` / `options` / `on_partial_failure`); every channel's parameters live inside its own item — webhook types use `options`, email uses an `email` sub-block. The old `notify.type` and top-level `notify.email` were removed; on upgrade just move them wholesale into the corresponding channel item.

`notify.mode` is global and channel-independent, so it is the first item under `notify`: `digest` (default) merges all new versions of a round into 1 notification, `per_feed` splits them per project; every notification is copied to all enabled channels. With `per_feed` the title includes the project name, e.g. `[FeedWatch] golang/go found 1 new version: go1.27.4` (the body heading too), so it is easy to tell them apart in an IM list. **The first-round baseline is always exactly 1 summary**, unaffected by `per_feed`.

`per_feed` batches are **committed independently**: if one batch fails, only the feeds it covers are left out of the state write; feeds already delivered are committed normally and will not be pushed again next round. Note that `per_feed` groups **by project**, not by version — 3 new versions of the same project in one round still produce 1 notification.

`subject_prefix` is a title prefix shared by all channels (overridable via a channel's `options.subject_prefix`); `notify.options` holds defaults shared by all channels and is overridden by each channel's own `options`. `notify.send_spacing` controls the minimum interval between two channel deliveries, with **one rate-limit gate per channel type** (email / telegram / wecom … do not affect each other); see [Concurrency & Rate Limiting](rate-limiting.md).

```yaml
notify:
  mode: digest                      # global, channel-independent: digest | per_feed
  send_spacing: 250ms               # one value; one gate per channel type (0s disables)
  subject_prefix: "[FeedWatch]"     # global: title prefix shared by all channels (optional)
  on_partial_failure: best_effort   # best_effort (default) | strict
  options:                         # defaults shared by all channels
    timeout: 10s
    retry_max: 2
  channels:
    - type: email                  # email can be mixed in too
      email:                       # channel-specific parameters, not on the outer level
        from: "FeedWatch <releases@your-verified-domain.com>"
        to: [you@example.com]
        api_key_env: RESEND_API_KEY
    - type: wecom
      name: WeCom Group
      options:
        webhook_url: ${WECOM_WEBHOOK_URL}
    - type: telegram
      options:
        token: ${TELEGRAM_BOT_TOKEN}
        chat_id: ${TELEGRAM_CHAT_ID}
```

`${ENV_VAR}` is expanded by the program at startup (**only `${NAME}`; literals like `$schema` are untouched**). A missing value fails immediately at startup and lists the variable name; each channel's required fields (`webhook_url` / `token` / `chat_id` / timeout & retry parameters) are likewise validated at startup rather than surfacing on the first polling round. Webhook URLs, tokens and signing secrets are credentials — **do not put them in YAML and do not commit them**; `.env.example` lists every variable and Docker Compose passes them into the container.

<a id="built-in-channels"></a>

## Built-in Channels

| `type` | Channel | Required options | Optional options | Success criterion |
| --- | --- | --- | --- | --- |
| `email` | Resend email | `email.from` / `email.to` inside the item, env var `RESEND_API_KEY` | `email.cc`, `email.api_key_env`, `options.subject_prefix` | SDK returns |
| `wecom` | WeCom group bot | `webhook_url` | `msg_type` (`markdown` default / `text`), `mentioned_list`, `mentioned_mobile_list` | HTTP 200 and `errcode=0` |
| `feishu` | Feishu custom bot | `webhook_url` | `secret` (signature verification), `msg_type` (`text` / `card`) | `code=0` |
| `dingtalk` | DingTalk custom bot | `webhook_url` | `secret` (signing), `msg_type` (`markdown` default / `text`) | `errcode=0` |
| `telegram` | Telegram Bot | `chat_id` + (`token` or `webhook_url`) | `parse_mode` (`HTML` default / `MarkdownV2` / `Markdown` / `text`), `disable_web_page_preview` | `ok=true` |
| `slack` | Slack Incoming Webhook | `webhook_url` | — | 2xx |
| `discord` | Discord Webhook | `webhook_url` | `username`, `avatar_url` | 204/2xx |
| `teams` | Teams Workflows Webhook | `webhook_url` | — | 2xx (Adaptive Card) |
| `bark` | Bark (iOS) | `webhook_url` | `device_key`, `group`, `sound`, `level`, `icon` | `code=200` |
| `ntfy` | ntfy.sh / self-hosted | `webhook_url` (topic URL) | `token`, `tags`, `priority` | 2xx |
| `webhook` | Generic custom Webhook | `webhook_url` | `method`, `content_type`, `body`, `header.<name>` | 2xx |
| `log` | Log only (local debugging) | — | — | Always succeeds |

**Email recipients are sent in one call**: the whole `email.to` / `email.cc` list goes into a single `SendEmailRequest`, so **one notification = 1 Resend API call**, not one call per recipient. `to` allows at most 50 addresses (exceeding it fails startup validation, avoiding a 400 retry every round). The trade-off is that addresses in `To` / `Cc` are visible to each other; the SDK supports `Bcc` but the current config does not expose it. If recipients must not see each other, configure multiple `type: email` channels that send separately (1 call per channel, each with its own sender and API key). Multiple email channels and multiple `per_feed` batches each count as one call, so `notify.send_spacing` applies to them as well.

All channels share: `subject_prefix` (default `[FeedWatch]`, global `notify.subject_prefix`, overridable per channel via `options.subject_prefix`), `timeout` (default 10s), `retry_max` (default 2), `retry_base_delay` (default 500ms). Network errors, `429` and `5xx` are retried with exponential backoff and honor `Retry-After` (including Telegram's `parameters.retry_after`); **vendor business error codes are never retried**.

A few details handled per the vendors' official docs: Feishu signing is base64 of `HMAC-SHA256(key=timestamp+"\n"+secret, msg="")`, placed in the JSON body; DingTalk signing is `HMAC-SHA256(key=secret, msg=millisecond timestamp+"\n"+secret)`, with the result as `timestamp` / `sign` **query parameters** (url-encoded exactly once); WeCom markdown is capped at 4096 bytes and text at 2048 bytes; Discord `content` is capped at 2000 characters with `@` parsing disabled; Telegram bodies are capped at 4096 characters and default to `HTML` (fewer escaping pitfalls than MarkdownV2), falling back to plain text when too long so truncation cannot produce broken tags; Teams uses **Adaptive Cards** (the old Office 365 connectors were retired in May 2026 — generate URLs with Workflows).

<a id="webhook-template"></a>

## Generic Webhook Template

`type: webhook` renders the request body with `text/template`. Available data fields are `.Subject` `.Text` `.Markdown` `.HTML` `.Kind` `.GeneratedAt` `.Count` `.Items`; `{{json .Text}}` performs JSON escaping, and the rendered result is validated as legal JSON by default.

```yaml
- type: webhook
  options:
    webhook_url: ${GENERIC_WEBHOOK_URL}
    header.Authorization: Bearer ${GENERIC_TOKEN}
    body: '{"title":{{json .Subject}},"message":{{json .Text}},"count":{{.Count}}}'
```

Gotify / Pushover / ServerChan / Mattermost / in-house alerting gateways can all be integrated this way with no code. Setting `content_type: application/x-www-form-urlencoded` disables JSON validation.

<a id="partial-failure"></a>

## Handling Partial Failures

The watcher's semantics: "a notification returns an error → the feeds covered by that batch do not commit their seen set or ETag → next round retries only those feeds". Other batches (and feeds with no new versions) commit normally, so with `per_feed` a failure in batch k never causes batches 1..k-1 to be resent. With multiple channels, a failure **inside the same batch** can still cause channels that already succeeded to receive duplicates, hence:

- `best_effort` (default): state is committed as soon as at least one channel succeeds; failed channels log `ERROR` and drop that delivery, producing no duplicates. A good fit for IM group notifications.
- `strict`: any channel failure fails the whole batch for a retry, guaranteeing no channel misses a notification, at the cost of possible duplicates (e.g. if one group bot stays broken, the others receive the same batch every round).

With only one channel configured the two behave identically: a failure means an error and a retry.

---

[← Back to main README](../../README_EN.md) · [简体中文](../notifications.md)
