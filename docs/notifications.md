# 通知渠道

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/notifications.md)

<a id="notifications"></a>

`notify.channels` 是唯一的通知配置写法：列表里每一条是一个渠道，同一条通知会依次推送给所有 `enabled` 的渠道，互不干扰。只推一个渠道就写一条（单元素列表），不需要另外一套配置。**`notify` 外层只放全局项**（`mode` / `send_spacing` / `subject_prefix` / `options` / `on_partial_failure`）；每个渠道的参数都写在自己的条目里——webhook 类放 `options`，邮件放 `email` 子块。旧版的 `notify.type` 与顶层 `notify.email` 都已移除，升级时把它们整体挪进对应的渠道条目即可。

`notify.mode` 是全局项、与通道无关，所以写在 `notify` 第一项：`digest`（默认）每轮把新版本汇总成 1 条，`per_feed` 按项目拆成多条；每条都会复制给所有 enabled 渠道。`per_feed` 下标题会带上项目名，例如 `[FeedWatch] golang/go 发现 1 个新版本：go1.27.4`（正文标题也带），方便在 IM 列表里直接分辨。**首轮基线通知固定是 1 条汇总**，不受 `per_feed` 影响。

`per_feed` 的多个批次是**独立提交**的：某一批发送失败时，只有该批涉及的 feed 不写状态，已成功送达的 feed 照常提交，下一轮不会重复推送它们。注意 `per_feed` 是「按项目分组」而不是「按版本」——同项目一轮出 3 个新版本仍合成 1 条。

`subject_prefix` 是所有渠道共用的标题前缀（可在渠道的 `options.subject_prefix` 覆盖），`notify.options` 是所有渠道共享的默认值，会被各渠道自己的 `options` 覆盖。`notify.send_spacing` 控制两次渠道投递之间的最小间隔，**每个渠道类型各一个限速门**（email / telegram / wecom …互不影响），见[并发与频率控制](rate-limiting.md)。

```yaml
notify:
  mode: digest                      # 全局，与通道无关：digest | per_feed
  send_spacing: 250ms               # 一个值，按渠道类型各一个限速门（0s 关闭）
  subject_prefix: "[FeedWatch]"     # 全局：所有渠道共用的标题前缀（可省略）
  on_partial_failure: best_effort   # best_effort(默认) | strict
  options:                         # 所有渠道共享的默认值
    timeout: 10s
    retry_max: 2
  channels:
    - type: email                  # 也可以混搭邮件
      email:                       # 该渠道专属参数，不放在外层
        from: "FeedWatch <releases@your-verified-domain.com>"
        to: [you@example.com]
        api_key_env: RESEND_API_KEY
    - type: wecom
      name: 企业微信群
      options:
        webhook_url: ${WECOM_WEBHOOK_URL}
    - type: telegram
      options:
        token: ${TELEGRAM_BOT_TOKEN}
        chat_id: ${TELEGRAM_CHAT_ID}
```

`${ENV_VAR}` 在启动时由程序展开（**只认 `${NAME}`，`$schema` 这类字面量不受影响**），缺失会在启动阶段直接报错并列出变量名；每个渠道的必填项（`webhook_url` / `token` / `chat_id` / 超时与重试参数）也在启动时校验，不会等到第一次轮询才暴露。webhook URL、token、签名密钥都含凭证，**不要写进 YAML、不要提交**；`.env.example` 列出了全部变量，Docker Compose 已把它们透传进容器。

<a id="built-in-channels"></a>

## 内置渠道

| `type` | 渠道 | 必填 options | 可选 options | 成功判定 |
| --- | --- | --- | --- | --- |
| `email` | Resend 邮件 | 条目内 `email.from` / `email.to`，环境变量 `RESEND_API_KEY` | `email.cc`、`email.api_key_env`、`options.subject_prefix` | SDK 返回 |
| `wecom` | 企业微信群机器人 | `webhook_url` | `msg_type`（`markdown` 默认 / `text`）、`mentioned_list`、`mentioned_mobile_list` | HTTP 200 且 `errcode=0` |
| `feishu` | 飞书自定义机器人 | `webhook_url` | `secret`（签名校验）、`msg_type`（`text` / `card`） | `code=0` |
| `dingtalk` | 钉钉自定义机器人 | `webhook_url` | `secret`（加签）、`msg_type`（`markdown` 默认 / `text`） | `errcode=0` |
| `telegram` | Telegram Bot | `chat_id` + (`token` 或 `webhook_url`) | `parse_mode`（`HTML` 默认 / `MarkdownV2` / `Markdown` / `text`）、`disable_web_page_preview` | `ok=true` |
| `slack` | Slack Incoming Webhook | `webhook_url` | — | 2xx |
| `discord` | Discord Webhook | `webhook_url` | `username`、`avatar_url` | 204/2xx |
| `teams` | Teams Workflows Webhook | `webhook_url` | — | 2xx（Adaptive Card） |
| `bark` | Bark（iOS） | `webhook_url` | `device_key`、`group`、`sound`、`level`、`icon` | `code=200` |
| `ntfy` | ntfy.sh / 自建 | `webhook_url`（topic URL） | `token`、`tags`、`priority` | 2xx |
| `webhook` | 通用自定义 Webhook | `webhook_url` | `method`、`content_type`、`body`、`header.<名称>` | 2xx |
| `log` | 只写日志（本地调试） | — | — | 总是成功 |

**邮件收件人是合并发送的**：`email.to` / `email.cc` 是整个列表一次性放进同一个 `SendEmailRequest`，**一条通知 = 1 次 Resend API 调用**，不会按收件人循环调用。`to` 最多 50 个地址（超出在启动校验阶段就报错，避免每轮 400 重试）。代价是 `To` / `Cc` 里的地址彼此可见；SDK 虽支持 `Bcc`，当前配置未暴露该字段。需要互相不可见时，配多个 `type: email` 渠道各自发（每个渠道 1 次调用，也可各用不同的发件人与密钥）。另外，多个 email 渠道、`per_feed` 的多个批次各算一次调用，所以 `notify.send_spacing` 对它们同样生效。

所有渠道都共享：`subject_prefix`（默认 `[FeedWatch]`，全局 `notify.subject_prefix`，可用渠道 `options.subject_prefix` 覆盖）、`timeout`（默认 10s）、`retry_max`（默认 2）、`retry_base_delay`（默认 500ms）。网络错误、`429`、`5xx` 按指数退避重试，并尊重 `Retry-After`（含 Telegram 的 `parameters.retry_after`）；**厂商业务错误码不会重试**。

几个按官方文档处理过的细节：飞书签名是 `HMAC-SHA256(key=timestamp+"\n"+secret, msg="")` 的 base64，字段放在 JSON body；钉钉加签是 `HMAC-SHA256(key=secret, msg=毫秒时间戳+"\n"+secret)`，结果作为 `timestamp` / `sign` **查询参数**（只 urlencode 一次）；企业微信 markdown 上限 4096 字节、文本 2048 字节；Discord `content` 上限 2000 字符并禁用 `@` 解析；Telegram 正文上限 4096 字符，默认用 `HTML`（比 MarkdownV2 少踩转义坑），内容过长时自动退回纯文本以免截断出坏标签；Teams 用 **Adaptive Card**（旧 Office 365 连接器已于 2026 年 5 月退役，URL 请用 Workflows 生成）。

<a id="webhook-template"></a>

## 通用 Webhook 模板

`type: webhook` 用 `text/template` 渲染请求体，数据字段有 `.Subject` `.Text` `.Markdown` `.HTML` `.Kind` `.GeneratedAt` `.Count` `.Items`；`{{json .Text}}` 会做 JSON 转义，渲染结果默认会校验为合法 JSON。

```yaml
- type: webhook
  options:
    webhook_url: ${GENERIC_WEBHOOK_URL}
    header.Authorization: Bearer ${GENERIC_TOKEN}
    body: '{"title":{{json .Subject}},"message":{{json .Text}},"count":{{.Count}}}'
```

Gotify / Pushover / Server酱 / Mattermost / 自研告警网关都可以这样接，不需要写代码。设 `content_type: application/x-www-form-urlencoded` 时不校验 JSON。

<a id="partial-failure"></a>

## 部分失败怎么处理

watcher 的语义是「某条通知返回错误 → 该批次涉及的 feed 不提交已见集合与 ETag → 下轮只重试这些 feed」。其它批次（以及没有新版本的 feed）照常提交，因此 `per_feed` 下不会出现「第 k 批失败，前 k-1 批全部重发」的情况。多通道下，**同一批次内部**的失败仍会造成已成功的渠道被重复推送，因此：

- `best_effort`（默认）：至少一个渠道成功即提交状态；失败渠道打 `ERROR` 日志并放弃该次推送，不产生重复。适合 IM 群通知。
- `strict`：任一渠道失败就整体报错重试，保证每个渠道都不漏，代价是可能重复（例如某个群机器人长期失效时，其它群会每轮都收到同一批）。

只配置一个渠道时两者行为一致：失败就报错重试。

---

[← 返回主文档](../README.md) · [English](en/notifications.md)
