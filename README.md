# FeedWatch

一个 Go 常驻程序：按配置轮询 `releases.atom`，发现**新的稳定版本**后推送通知。通知渠道支持 Resend 邮件，以及企业微信、飞书、钉钉、Telegram、Slack、Discord、Teams、Bark、ntfy 和通用 Webhook，**可同时推送到多个渠道**。数据源、通知渠道、状态存储都是可插拔接口（当前数据源仍只有 Atom）。

**两种镜像变体**：不带后缀的 base 镜像保持纯 `config.yaml` 手动配置（无前端资源、无 HTTP 服务、无 SQLite）；`-webui` 变体额外提供多用户 Web 管理界面、登录鉴权与 SQLite 存储。两者由同一个 `//go:build webui` 开关隔离，base 二进制不链接任何 WebUI / SQLite 代码，详见 [WebUI 变体](#webui-变体)。

当前默认监控三个仓库（均已验证可用）：

| 项目 | Atom 源 |
| --- | --- |
| golang/go | `https://github.com/golang/go/releases.atom` |
| rust-lang/rust | `https://github.com/rust-lang/rust/releases.atom` |
| python/cpython | `https://github.com/python/cpython/releases.atom` |

## 行为约定

| 场景 | 行为 |
| --- | --- |
| **首次启动**（没有状态文件） | 抓取全部 feed，发 **1 条「当前版本基线」汇总通知**，列出每个项目当前最新稳定版；发送成功后才把抓到的所有条目标记为已见 |
| **之后每轮** | 只对新增的稳定版发通知；`digest`（默认）一轮合并成 1 条，`per_feed` 按项目拆成多条（标题带项目名） |
| **重启容器 / 重启进程但无新版本** | **完全静默**，不调用任何通知渠道 |
| **首轮基线** | **固定 1 条汇总**，不受 `per_feed` 影响 |
| 某个批次发送失败 | **只把该批次涉及的 feed 留作重试**：已成功送达的批次照常提交已见 ID 和 ETag，下一轮不会重复推送（at-least-once） |
| 多通道部分失败 | 默认 `best_effort`：只要有一个渠道送达就提交状态（失败渠道只记日志，不重试、不产生重复推送）；`strict` 则整批报错重试 |
| 某个 feed 抓取失败 | 跳过该 feed，其它 feed 正常通知；首轮若有任一 feed 失败则整封基线通知推迟 |
| HTTP 304 | 不做 diff、不改写已见集合、不发通知 |
| 预发布 / 非版本标签 | 默认不通知（`go1.27rc3`、`v3.16.0a1`、CPython 的 `3.10` 分支 tag 等），但 ID 仍记为已见 |
| 状态目录不可写（如 bind mount 权限不对） | **启动即失败**：`jsonfile.New` 在启动期用 `CreateTemp` 探测一次并立刻删除，不会先发通知再发现存不下 |
| 订阅很多 feed / 一轮很多通知 | 抓取受 `poll.concurrency` 有界并发 + `poll.request_spacing` 限速；渠道投递受 `notify.send_spacing` 限速（**按渠道类型**，同类型共用一个门），见[并发与频率控制](#并发与频率控制) |

> 去重只认 Atom `<id>`，不是版本号。GitHub 的 feed 只保留最近约 10 条，长时间停机漏掉的中间版本不会补发——这是预期行为。

## 快速开始（本地 Windows exe，不需要 Docker）

```bash
go mod tidy
go build -o feedwatch.exe ./cmd/feedwatch
```

`config.yaml` 是**本地私有文件，已加入 `.gitignore`**。首次使用先从模板生成：

```bash
cp config.example.yaml config.yaml   # PowerShell: Copy-Item config.example.yaml config.yaml
```

> 装了 GNU make 的话也可用 `make init`（幂等，不会覆盖已有配置）；Windows 自带的 Git Bash 默认没有 make。

默认模板在 `notify.channels` 里只配了一条 `email`（Resend 邮件）：把该条目下 `email.from`（必须是 Resend 已验证域名）和 `email.to` 改成你自己的值。想推送到企业微信 / 飞书 / Telegram 等，见下面的[通知渠道](#通知渠道)章节——在 `channels` 里替换或追加条目，并把对应的 webhook URL / token 填进环境变量即可。

```powershell
# ① 最安全：预览基线邮件，零网络发送、不碰正式状态文件
.\feedwatch.exe -config config.yaml -once -dry-run
.\feedwatch.exe -config config.yaml -once -dry-run   # 第二次：应完全静默（验证重启语义）

# ② 全链路本地调试：channels 里只留 - type: log，也不发真实邮件
#    （复制 config.yaml 为 config.local.yaml，改 channels 和 store.path: data/state.local.json）
.\feedwatch.exe -config config.local.yaml -once

# ③ 真实发信
$env:RESEND_API_KEY="re_xxx"
.\feedwatch.exe -config config.yaml -once    # 发一封基线邮件
.\feedwatch.exe -config config.yaml          # 常驻轮询
```

`-dry-run` 会把**整个通知扇出**换成一个日志通知器（因此多通道配置下也不会误发、且无需准备任何密钥），并把状态文件改成 `data/state.dryrun.json`（独立文件），所以既能反复预览基线 / 静默 / 新版本三条路径，又不会污染正式状态、不会误抑制真实基线通知。

### CLI 参数与环境变量

优先级：**flag > 环境变量 > config.yaml > 内置默认值**。

| flag | 对应环境变量 | 说明 |
| --- | --- | --- |
| `-config` | `FEEDWATCH_CONFIG` | 配置文件路径，默认 `config.yaml` |
| `-once` | — | 只跑一轮后退出 |
| `-dry-run` | `FEEDWATCH_DRY_RUN` | 只打印邮件预览，不发信、隔离状态 |
| `-state-path` | `FEEDWATCH_STATE_PATH` | 覆盖 `store.path`（Docker 必须指向卷内） |
| `-poll-interval` | `FEEDWATCH_POLL_INTERVAL` | 覆盖 `poll.interval`，如 `30m` |
| `-log-level` | `FEEDWATCH_LOG_LEVEL` | `debug/info/warn/error` |
| `-log-format` | `FEEDWATCH_LOG_FORMAT` | `text/json`（容器建议 json） |
| `-version` | — | 打印版本 |

敏感信息只从环境变量读：`RESEND_API_KEY`（变量名可由 email 渠道条目的 `email.api_key_env` 改），以及各渠道的 `*_WEBHOOK_URL` / `*_TOKEN` / `*_SECRET`。配置里写 `${NAME}` 占位符，启动时展开；缺失会直接报出变量名。见 `.env.example`。**不要把真实密钥写进 YAML**。

## 配置

- `config.example.yaml`：**唯一提交进仓库**的配置模板，带全部字段与注释。
- `config.yaml`：从模板复制出的本地运行配置，含你的域名、收件人与 feed 列表，已被 `.gitignore` 忽略——随便改都不会产生 git diff。`.env`、`config.local.yaml` 同样不提交。
- 密钥永远不写进 YAML，只从环境变量读。
- 新增一个监控项目 = 复制一段 `feeds`、改 `name` / `url`，**零代码改动**。
- 新增一个通知渠道 = 在 `notify.channels` 加一段、配好环境变量，**零代码改动**；可多个并存。完整字段见 `config.example.yaml` 与[通知渠道](#通知渠道)。

```yaml
feeds:
  - name: golang/go
    type: atom
    url: https://github.com/golang/go/releases.atom
    enabled: true
    filter:
      stable_only: true
      # include: ['^go\d']
      # exclude: ['(?i)nightly']
      # version_pattern: '^v?(\d+\.\d+\.\d+)$'
    options:
      content_policy: text   # none | text | html
```

`filter.stable_only: true` 时会从标题里提取 3~4 段数字版本号，因此能处理 GitHub 的人类可读标题（`[release-branch.go1.27] go1.27.3`、`Rust 1.99.1`、`v3.15.2`），并排除 `rc/beta/alpha/pre/dev/nightly/snapshot/aN/bN` 后缀以及 `Python 3.10 end of life` 这类非版本标签。邮件里显示的是提取出的版本词元（如 `go1.27.3`）。若某项目真实版本只有两段，用该 feed 的 `version_pattern` 覆盖即可。`stable_only: false` 会放行所有条目（预发布仍会在数据里被标注为 unstable）。

基线邮件取的是**语义上最高的版本**，不是 `updated` 时间最新的一条——同一天发布多个维护分支时，时间戳会把旧分支的补丁版排到最前面。

## 通知渠道

`notify.channels` 是唯一的通知配置写法：列表里每一条是一个渠道，同一条通知会依次推送给所有 `enabled` 的渠道，互不干扰。只推一个渠道就写一条（单元素列表），不需要另外一套配置。**`notify` 外层只放全局项**（`mode` / `send_spacing` / `subject_prefix` / `options` / `on_partial_failure`）；每个渠道的参数都写在自己的条目里——webhook 类放 `options`，邮件放 `email` 子块。旧版的 `notify.type` 与顶层 `notify.email` 都已移除，升级时把它们整体挪进对应的渠道条目即可。

`notify.mode` 是全局项、与通道无关，所以写在 `notify` 第一项：`digest`（默认）每轮把新版本汇总成 1 条，`per_feed` 按项目拆成多条；每条都会复制给所有 enabled 渠道。`per_feed` 下标题会带上项目名，例如 `[FeedWatch] golang/go 发现 1 个新版本：go1.27.4`（正文标题也带），方便在 IM 列表里直接分辨。**首轮基线通知固定是 1 条汇总**，不受 `per_feed` 影响。

`per_feed` 的多个批次是**独立提交**的：某一批发送失败时，只有该批涉及的 feed 不写状态，已成功送达的 feed 照常提交，下一轮不会重复推送它们。注意 `per_feed` 是「按项目分组」而不是「按版本」——同项目一轮出 3 个新版本仍合成 1 条。

`subject_prefix` 是所有渠道共用的标题前缀（可在渠道的 `options.subject_prefix` 覆盖），`notify.options` 是所有渠道共享的默认值，会被各渠道自己的 `options` 覆盖。`notify.send_spacing` 控制两次渠道投递之间的最小间隔，**每个渠道类型各一个限速门**（email / telegram / wecom …互不影响），见[并发与频率控制](#并发与频率控制)。

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

### 内置渠道

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

### 通用 Webhook 模板

`type: webhook` 用 `text/template` 渲染请求体，数据字段有 `.Subject` `.Text` `.Markdown` `.HTML` `.Kind` `.GeneratedAt` `.Count` `.Items`；`{{json .Text}}` 会做 JSON 转义，渲染结果默认会校验为合法 JSON。

```yaml
- type: webhook
  options:
    webhook_url: ${GENERIC_WEBHOOK_URL}
    header.Authorization: Bearer ${GENERIC_TOKEN}
    body: '{"title":{{json .Subject}},"message":{{json .Text}},"count":{{.Count}}}'
```

Gotify / Pushover / Server酱 / Mattermost / 自研告警网关都可以这样接，不需要写代码。设 `content_type: application/x-www-form-urlencoded` 时不校验 JSON。

### 部分失败怎么处理

watcher 的语义是「某条通知返回错误 → 该批次涉及的 feed 不提交已见集合与 ETag → 下轮只重试这些 feed」。其它批次（以及没有新版本的 feed）照常提交，因此 `per_feed` 下不会出现「第 k 批失败，前 k-1 批全部重发」的情况。多通道下，**同一批次内部**的失败仍会造成已成功的渠道被重复推送，因此：

- `best_effort`（默认）：至少一个渠道成功即提交状态；失败渠道打 `ERROR` 日志并放弃该次推送，不产生重复。适合 IM 群通知。
- `strict`：任一渠道失败就整体报错重试，保证每个渠道都不漏，代价是可能重复（例如某个群机器人长期失效时，其它群会每轮都收到同一批）。

只配置一个渠道时两者行为一致：失败就报错重试。

## 并发与频率控制

订阅链接多起来之后有两个风险：一轮开始瞬间对同一域名（最常见是 `github.com`）发出大量请求；以及 `per_feed` + 多渠道在一轮里连续打满 IM / 邮件 API。程序里做了三层约束：

| 层 | 配置（默认值） | 作用 |
| --- | --- | --- |
| 轮询周期 | `poll.interval: 30m` | 一个 feed 每轮最多请求 1 次；`Run()` 串行调用 `RunOnce`，两个周期绝不重叠。配合 ETag/304，未变化的 feed 只有一个 304 往返 |
| 抓取并发与限速 | `poll.concurrency: 4`、`poll.request_spacing: 100ms` | 有界 worker pool：最多 4 个 feed 同时在抓；两次请求**发起**至少隔 100ms，即全局约 10 req/s。单个慢 feed（含重试退避）不再阻塞排在后面的 feed |
| 通知限速 | `notify.send_spacing: 250ms` | **按渠道类型**各一个限速门：同一类型的多次投递至少隔 250ms（约 4 次/秒），不同服务互不阻塞。`Multi` 扇出本身就是串行的，`per_feed` 的多批次、多个同类型渠道都共用该类型的门 |

```yaml
poll:
  interval: 30m
  timeout: 2m
  concurrency: 4         # 1 = 完全串行
  request_spacing: 100ms # 0s 关闭
notify:
  send_spacing: 250ms    # 0s 关闭
```

几点说明：

- **原先是隐式全串行**（并发上限就是 1），既没有可配置项，慢 feed 也会拖住后面的 feed；现在改成显式、可调、有上界的。`poll.concurrency: 1` + 两个 spacing 设 `0s` 即可回到旧行为。
- 抓取限速是**全局**的，不按 host 分组；feed 分散在不同域名时偏保守，但上限明确、实现简单。抓取结果按 `feeds[]` 顺序写入各自下标，所以 diff、通知顺序、状态提交都与并发度无关。
- 通知限速按**渠道类型**分组：`email`、`telegram`、`wecom` …各有独立的一个门，某个服务被限流不会拖慢另一个。同一类型的所有渠道（例如两个邮件渠道）仍然共用该类型的门，所以一个 `per_feed` 批次同时投递给它们时不会连发。类型之间不互相等待，一条通知扇出到 N 种渠道也不会被叠加成 `N × spacing`。
- `N` 个 feed 的一轮最短耗时约 `N × request_spacing`。feed 极多时要保证它明显小于 `poll.timeout`（默认 2m），否则调大 `timeout` / 调小或关闭 spacing：超时后未抓完的 feed 本轮跳过、状态不提交，**不会误发通知**，但下一轮仍按 `feeds[]` 顺序从头开始，末尾的 feed 会长期轮不到。
- 状态写入使用独立的 10s 超时，不受 `poll.timeout` 影响：已经送达的批次不会因为本轮超时而丢掉提交、进而在下一轮重复推送。
- HTTP 层原有的保护仍在：`429` / `5xx`、网络错误按指数退避重试，并尊重 `Retry-After`（含 Telegram `parameters.retry_after`、`X-RateLimit-Reset`）。限速门负责**主动削峰**，退避负责**被动应对**。
- 各渠道自身还有频率上限（见[已知限制](#已知限制)）。`per_feed` + 几十个 feed + 多渠道时，必要时增大 `notify.send_spacing`。

## Docker 部署（WSL 里执行）

```bash
cp config.example.yaml config.yaml   # 本地私有配置，会被只读挂载进容器
cp .env.example .env                 # 填真实密钥：邮件 / 你启用的推送渠道
docker compose build
docker compose up -d
docker compose logs -f    # 首次应看到基线通知已推送到每个渠道
docker compose restart    # 无新版本时应完全静默
```

`docker-compose.yml` 用 `container_name` 把容器名固定为 `feedwatch`，所以也可以直接 `docker logs -f feedwatch`、`docker exec -it feedwatch sh`；上面那些 `docker compose ...` 命令用的是 service 名，不受影响。设置 `container_name` 后该服务不能横向扩容，但本项目共用一份状态文件，本来就只应跑单个实例，没有影响。卷仍是原来的 `feedwatch-data`，改名后重建容器不会丢 `state.json`、不会重发基线通知。

镜像为多阶段构建（`golang:1.27-alpine` → `alpine:3.21`），静态二进制、非 root 用户、含 CA 证书与时区；编译阶段用 BuildKit 的 `TARGETOS` / `TARGETARCH` 交叉编译，因此同一份 Dockerfile 可产出 amd64 / arm64 镜像。配置通过只读挂载传入，各渠道凭证通过 `docker-compose.yml` 的 `environment` 从 `.env` 注入（新增自定义变量时记得在那里补一行），状态通过命名卷 `feedwatch-data:/app/data` 持久化。compose 里的 `user: "${FW_UID:-app}:${FW_GID:-app}"` 默认沿用镜像内的 `app` 用户（与命名卷属主一致）；改成下面的 bind mount 时才需要用宿主机 uid/gid 覆盖。

> 镜像内烘焙的是 `config.example.yaml` 的**占位默认值**（不含任何真实域名或收件人），因此干净 clone 也能直接 `docker compose build`；`.dockerignore` 会挡住本地 `config.yaml`，防止私有配置进入镜像层。不带挂载直接 `docker run` 时用的就是这份占位配置，需自行 `-v` 挂真实配置。

> **卷丢了会重发基线邮件**（因为程序会认为这是首次运行）。这是预期行为。若手工 `docker run`，记得 `-e RESEND_API_KEY=... -v feedwatch-data:/app/data`。

### 把状态卷换成宿主机目录

`docker-compose.yml` 里 `- feedwatch-data:/app/data` 用的是**命名卷**：左边是卷名而不是路径，数据由 Docker 自己保管（Linux 上在 `/var/lib/docker/volumes/<项目名>_feedwatch-data/_data`，Docker Desktop / WSL 则在虚拟机磁盘内），宿主机上看不到普通文件夹；文件末尾的 `volumes: feedwatch-data:` 只是声明这个卷存在。`docker compose down` 默认不删卷，`down -v` 才会。

想让状态落在 compose 同目录的 `data/` 下，把这一行换成 bind mount 即可，**容器内路径 `/app/data` 与 `FEEDWATCH_STATE_PATH` 都不用改**：

```yaml
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./data:/app/data          # 替换 - feedwatch-data:/app/data
# 顶层 volumes: 段可整段删掉
```

**bind mount 会保留宿主机目录的属主**：`Dockerfile` 里的 `chown -R app:app /app` 只作用于镜像内，盖不到挂载进来的目录。而状态写入是 `CreateTemp` + `rename`，需要在目录里**创建文件**，所以目录必须对容器用户可写。若目录是 `drwxr-xr-x docker:docker`，容器内的 `app` 落在 others 上只有 `r-x`，就会 `permission denied` → 状态永远存不下 → 每轮都判定首轮并**重发基线通知**（Linux 服务器必现，WSL + Docker Desktop 的文件共享通常较宽松）。

推荐做法是**不动宿主机权限**，让容器以该目录属主的身份运行。`docker-compose.yml` 已经设好 `user: "${FW_UID:-app}:${FW_GID:-app}"`，把宿主机 uid/gid 传进去即可：

先确认目录属主就是当前登录用户：`stat -c '%u:%g' data` 应与 `id -u`:`id -g` 一致。若目录属主是别的用户（例如 `docker:docker`），就把**那一对数字**写进 `.env` 的 `FW_UID` / `FW_GID`，或先 `chown` 给当前用户——`$(id -u)` 取的是执行命令的人，不是目录属主，两者不一致时照样会 `permission denied`。

```bash
mkdir -p data

# 方式一（推荐）：写进 .env，之后任意终端执行 compose 都一致
printf 'FW_UID=%s\nFW_GID=%s\n' "$(id -u)" "$(id -g)" >> .env
docker compose up -d

# 方式二：只对本次命令生效（shell 先展开 $(...)，再作为环境变量传给 compose）
FW_UID=$(id -u) FW_GID=$(id -g) docker compose up -d
```

> **Compose 不会执行 `$(...)`**：它只做 `${VAR}` 插值，把 `user: "$(id -u):$(id -g)"` 写进 YAML 会被当成字面字符串、导致容器启动失败。所以 `.env` 里必须是展开后的**数字**，上面的 `printf` 就是干这个的。另外别用变量名 `UID`——bash/zsh 里 `UID` 是只读 shell 变量，`UID=$(id -u)` 会报 `readonly variable`，因此这里用 `FW_UID` / `FW_GID`。两者只在宿主机侧用于 Compose 插值，**不会**传入容器。
>
> 两者都不设置时 `user` 为 `app:app`，等价于镜像的 `USER app`，正好匹配默认命名卷的属主，所以默认的命名卷部署方式完全不受影响。

两条备选：保持默认用户而给目录授权（`sudo chown <容器内 app 的 uid>:<gid> data`，或 `chmod 777 data`——所有用户可写，不推荐）；或在 compose 里把 `user` 写死成具体数字。自定义 compose 文件时，同样只需在 service 下加 `user: "1000:1000"`（换成宿主机实际值）。

**配错会在启动期就被拦住**：`jsonfile.New` 在建 store 时先用 `CreateTemp` 在状态目录探测一次并立即删除，不可写就直接启动失败（`state directory ... is not writable`），不会先发出基线邮件才发现存不下。所以现在看到的是启动报错 + 容器反复重启，而不是每半小时一封重复通知。

`data/` 已在 `.gitignore` 中，不会进仓库。

**切换等于从空状态开始**：新目录没有 `state.json`，程序会当成首次运行并重发一封基线通知。想保留已见集合，先把命名卷里的状态拷出来（真实卷名一般带项目名前缀，先用 `docker volume ls | grep feedwatch` 确认）：

```bash
docker run --rm -v feedwatch_feedwatch-data:/src -v "$PWD/data:/dst" alpine:3.21 \
  cp /src/state.json /dst/state.json
```

想保留命名卷、只指定宿主机位置，也可以给顶层卷加 `driver_opts: {type: none, o: bind, device: /绝对路径/data}`，但日常直接用 `./data` 更简单。

## WebUI 变体

| | base | webui |
| --- | --- | --- |
| 镜像 tag | 无后缀 | 同版本号加 `-webui`（如 `latest-webui`） |
| 配置来源 | 每次启动读 `config.yaml` | 首次把 `config.yaml` 当种子导入 SQLite，之后数据库是唯一来源 |
| 界面 / 接口 | 无 | Vue 3 SPA + `/api/*`，默认监听 `:8080` |
| 状态 | `data/state.json`（JSON 原子写） | SQLite WAL（`/app/data/feedwatch.db` 及 `-wal` / `-shm`） |
| 用户 | 单用户（配置即全部订阅） | 多用户 + 管理员，订阅与渠道按用户隔离 |

> **二选一运行**：不要同时启动 `docker-compose.yml` 与 `docker-compose.webui.yml`。两套各有独立的轮询器与状态，会针对同一批 release 各发一遍通知。

### 启动

```bash
cp config.example.yaml config.yaml     # 首次种子来源，之后可不再修改
cp .env.example .env                   # 填渠道密钥；WebUI 段可按需调整
docker compose -f docker-compose.webui.yml up -d --build
docker logs -f feedwatch-webui         # 首启随机管理员密码只在这里打印一次
```

浏览器打开 `http://<宿主机>:8080`（端口由 `.env` 的 `WEBUI_HTTP_PORT` 控制）。用日志里的 `admin` 密码登录，**首次登录会被强制改密**；改密后所有旧会话立即失效。管理员可在「用户管理」里新建账号或重置密码（新密码只在页面上显示一次）。

若经 HTTPS 反向代理访问，把 `.env` 的 `FEEDWATCH_WEBUI_COOKIE_SECURE` 设为 `true`；纯 HTTP 直连时务必保持 `false`，否则会话 cookie 带 `Secure` 标志、浏览器不会回传，导致无法登录。会话有效期由 `FEEDWATCH_WEBUI_SESSION_TTL` 控制（默认 `168h`）。

### 数据与配置的关系

- 空库（仅 `users` 表为空）时把 `config.yaml` 的 `feeds` / `channels` / 全局设置导入 SQLite，并生成一条基线通知；**之后改 `config.yaml` 不会影响已有数据**，订阅、渠道、设置都在页面里管理。
- 数据库在命名卷 `feedwatch-webui-data:/app/data`。SQLite WAL 需要**目录**可写，启动期有和 base 一样的 `CreateTemp` 预检；把卷换成 bind mount 时同样用 `.env` 里的 `FW_UID` / `FW_GID` 指定宿主机属主。
- 新用户新增订阅从当前全局 seen 集合建立基线，**不会补发历史版本**；删除后重新订阅也一样。
- 「全局设置」页可改轮询间隔、超时、并发、抓取/发送间隔、HTTP 参数等；服务端逐项校验后写库，**重启进程后生效**（不热更新）。
- `webui.cookie_secure` / `webui.session_ttl` 是环境变量控制的只读项；`notify.mode` 在多租户模式下是每条订阅自己的 `mode`（`digest` / `per_feed`），不再是全局开关。

### 通知语义

- 同一 URL 全局每轮只抓一次（`url_key = sha256(规范化 URL)`），抓完按每个用户各自的过滤条件、`always` / `on_update` 与 `digest` / `per_feed` 分流。
- **不做跨用户合并**：每个用户 × 每个渠道一次独立请求，使用各自的 `api_key`，这样每个用户都能在 Resend 等后台看到自己的发送记录。限速按**渠道类型**分别计算（email / telegram / wecom …各一个独立门），不同服务互不阻塞。
- 渠道密钥不落库：页面只保存 `${ENV_VAR}` 占位符或环境变量名（邮件填 `api_key_env` 的变量名，不加 `${}`），发送时才在内存里展开；明文 token 会被拒绝，列表回显统一脱敏。Compose 已把 `.env` 里全部渠道变量透传进容器。
- 通知内容在抓取成功后就物化入库，投递失败只重试已渲染的投递组，不重新抓 feed、也不阻塞全局已见集合。

### 页面与本地开发

页面：`/login`、概览、我的订阅、通知方式、账号设置；管理员另有用户管理、轮询池、限速状态、全局设置、审计日志。普通用户看不到管理端入口，后端对他人资源一律返回 404。

```bash
make build-webui                 # 或 sh web/build.sh，产物写入 internal/webui/embedded/（已 gitignore）
go build -tags webui -o feedwatch-webui.exe ./cmd/feedwatch
FEEDWATCH_DB_PATH=data/feedwatch.db FEEDWATCH_HTTP_ADDR=127.0.0.1:8080 ./feedwatch-webui.exe -config config.yaml
./feedwatch-webui.exe -once      # 离线冒烟：抓一轮 → 生成投递计划 → 投递一轮后退出
```

干净 clone 下 `-tags webui` 也能编译，页面只显示占位提示（仓库只提交 `embedded/stub.html`），真实前端由 `make build-webui` 或 `Dockerfile.webui` 的 Node stage 生成。`-dry-run` 在 WebUI 变体里无意义，会被忽略并记一条日志。

> WebUI 仍是**单实例**：SQLite `SetMaxOpenConns(1)` + 单个后台 orchestrator，多实例写同一个库会产生写竞态与重复投递。

## 发布镜像（GitHub Actions / GHCR）

`.github/workflows/docker-publish.yml` 在推送 `v*` tag 时用 `matrix.variant: [base, webui]` **并行产出两组**镜像，每组各自是 **linux/amd64 + linux/arm64** 双架构 manifest，发布到 GitHub Container Registry：`ghcr.io/zsyo/feedwatch`。不带后缀的 tag 是纯配置版，`-webui` 后缀的是带管理界面的变体；两组的 `latest` 是不同 digest。Docker 会按宿主机架构自动挑选 manifest，同一条 `pull` 命令在 x86 服务器和 ARM 板子上都能用。镜像名由 `${{ github.repository }}` 自动生成，fork 后会变成 `ghcr.io/<你的用户名>/feedwatch`，无需改 workflow。

### 发布一个新版本

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

tag 必须是 `vMAJOR.MINOR.PATCH`（允许 `-rc.1` 一类后缀）；格式不对时 workflow 立即报错退出，不会推镜像。一个 tag 同时触发两个 matrix job，`fail-fast: false` 保证其中一组失败不会取消另一组。

| Git tag | base 组镜像 tag | webui 组镜像 tag |
| --- | --- | --- |
| `v1.2.3` | `1.2.3`、`1.2`、`latest` | `1.2.3-webui`、`1.2-webui`、`latest-webui` |
| `v1.2.3-rc.1` | `1.2.3-rc.1` | `1.2.3-rc.1-webui`（预发布不移动任何 `latest`） |

> 本节里的 `v1.2.3` / `1.2.3` 都只是示意，不代表某个已发布版本。通用规则：base 组按 `{{version}}` / `{{major}}.{{minor}}` / `latest` 生成，webui 组用同样的规则再加 `-webui` 后缀。每次正式发版两组都会新增完整版本号 tag，并各自移动自己的 `<主版本>.<次版本>` 与 `latest`；预发布只新增自己的 tag，两组的 `latest` 都不动。已发布的具体版本以仓库 Releases / GHCR package 的 tag 列表为准，固定版本时用 `docker pull ghcr.io/zsyo/feedwatch:<版本>` 或 `docker pull ghcr.io/zsyo/feedwatch:<版本>-webui`。

CI 用 `matrix.variant` 切换 `file:`（`Dockerfile` / `Dockerfile.webui`），webui 组的 metadata-action 追加 `suffix=-webui,onlatest=true`；两组 layer cache 按 variant 隔离，互不挤占。tag 通过 `-ldflags "-X main.version=..."` 注入二进制，容器内 `./feedwatch -version` 能直接看到版本。**无需配置任何额外 secret**：用内置 `GITHUB_TOKEN` + `packages: write` 登录 GHCR，构建缓存走 GitHub Actions cache。

### 直接使用预构建镜像（服务器无需源码、无需本地编译）

```bash
cp config.example.yaml config.yaml    # 填好 channels 里 email 条目的 email.from / to
docker pull ghcr.io/zsyo/feedwatch:latest
docker run -d --name feedwatch \
  -e RESEND_API_KEY="re_xxx" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v feedwatch-data:/app/data \
  ghcr.io/zsyo/feedwatch:latest
```

WebUI 变体换成带 `-webui` 的 tag，多映射一个端口并用自己的数据卷；首次启动的管理员密码只在日志里打印一次：

```bash
docker pull ghcr.io/zsyo/feedwatch:latest-webui
docker run -d --name feedwatch-webui \
  -p 8080:8080 \
  -e RESEND_API_KEY="re_xxx" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v feedwatch-webui-data:/app/data \
  ghcr.io/zsyo/feedwatch:latest-webui
docker logs -f feedwatch-webui
```

> 用命名卷时**不要**加 `--user`：Docker 会用镜像内 `/app/data` 的属主初始化空卷，正好是 `app` 用户；强行指定宿主机 uid 反而会写不进去。WebUI 的 SQLite 卷同理。

想把状态放在宿主机目录，改用 bind mount 并把容器用户设成该目录的属主。`docker run` 由 shell 展开 `$(...)`，可以直接写：

```bash
mkdir -p data
docker run -d --name feedwatch \
  --user "$(id -u):$(id -g)" \
  -e RESEND_API_KEY="re_xxx" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  ghcr.io/zsyo/feedwatch:latest
```

`--user "$(id -u):$(id -g)"` 与 compose 里的 `FW_UID` / `FW_GID` 覆盖等价；同样要求当前用户就是 `data/` 的属主，原理见上一节。

> 用多通道时，把对应的 `-e WECOM_WEBHOOK_URL=...`、`-e TELEGRAM_BOT_TOKEN=...` 等一并加进 `docker run`（或写进 compose 的 `environment`）；漏掉的变量会在启动日志里直接报出名字。

改用 compose：删掉 `docker-compose.yml` 里的 `build: .`，把 `image` 换成远程镜像，`environment` / `volumes` / `restart` / `user` 全部原样保留：

```yaml
services:
  feedwatch:
    image: ghcr.io/zsyo/feedwatch:latest   # 也可固定到某个已发布的具体版本 tag
    # build: .                             # 删除这一行
```

然后 `docker compose pull && docker compose up -d`。WebUI 变体对应独立的 `docker-compose.webui.yml`：把 `image` 换成 `ghcr.io/zsyo/feedwatch:latest-webui` 并删掉 `build:` 段，`ports` / 独立数据卷 / `environment` 原样保留。

### 双架构是怎么构建的

`Dockerfile` 编译阶段固定 `FROM --platform=$BUILDPLATFORM`，用 BuildKit 自动注入的 `TARGETOS` / `TARGETARCH` 做 `CGO_ENABLED=0` 交叉编译，Go 工具链始终跑在 runner 原生 amd64 上；QEMU 只负责运行阶段的 `apk add` / `adduser`（arm64 变体），比在 QEMU 里编译整个 Go 工具链快得多。`golang:1.27-alpine` 与 `alpine:3.21` 的官方镜像均含 `arm64v8` 变体。

两个 Dockerfile 都是这个结构：`Dockerfile.webui` 多一个 `node:22-alpine` 前端 stage（同样跑在 `$BUILDPLATFORM`，产物是纯 JS/CSS），Go stage 用 `-tags webui` 把 `internal/webui/embedded/` 一起 `go:embed` 进二进制，运行阶段不需要 Node。base 的 `Dockerfile` 保持逐字节不变，因此 base 镜像里没有 npm、前端资源、HTTP server 或 SQLite；`go list -deps ./cmd/feedwatch` 也不会出现 `modernc.org/sqlite`、`internal/webui`、`database/sql`。

本地 `docker compose build` 不带 `--platform`，只产出**本机架构**镜像；要复现双架构需 `docker buildx build --platform linux/amd64,linux/arm64 .`（多架构 manifest 只能推到 registry，`--load` 只支持单平台）。Dockerfile 依赖 BuildKit 自动注入的平台变量（Docker 23+ 默认启用）。

> 生产环境建议把 workflow 里的 action 固定到 commit SHA（GitHub 官方安全建议）；当前用的是 major tag：`actions/checkout@v7`、`docker/setup-qemu-action@v4`、`docker/setup-buildx-action@v4`、`docker/login-action@v4`、`docker/metadata-action@v6`、`docker/build-push-action@v7`。

## 目录结构

```
.github/workflows/docker-publish.yml  tag 触发的 GHCR 双组双架构发布（matrix: base / webui）
Dockerfile / Dockerfile.webui  base 两阶段构建 / 多一个 Node 前端 stage 的 webui 构建
docker-compose.yml / docker-compose.webui.yml  base / webui 各自部署（二选一运行）
cmd/feedwatch/main.go          唯一组合根；-version 后通过极薄 hook 分叉到 webui 变体
internal/domain/              领域模型（零内部依赖）
internal/config/              YAML + env 覆盖 + 校验
internal/source/              Source 接口 / Registry
internal/source/atom/         Atom/RSS 解析、条件请求、重试
internal/filter/              稳定版过滤规则
internal/notifier/            Notifier 接口 / Registry / Multi 扇出与失败策略 / 投递限速装饰器
internal/notifier/template/   go:embed 邮件与 IM 消息模板（基线 / 新版本共用）
internal/notifier/email/      Resend 实现
internal/notifier/log/        日志通知器（本地调试 / dry-run）
internal/notifier/webhook/    共享 HTTP 引擎 + 10 个渠道 Adapter（每个渠道一个文件）
internal/pace/                有界并发 + 最小间隔的限速门（抓取全局一个，投递按渠道类型各一个）
internal/store/               StateStore 接口 / Registry
internal/store/jsonfile/      JSON 原子写状态 + 启动期可写性预检
internal/watcher/             base 核心编排：有界并发抓取 → 过滤 → diff → 分批通知 → 按批次提交（webui 不改动它）
internal/webui/               WebUI 变体（全部 //go:build webui）：首启种子、URL 规范化、embed 资源
internal/webui/store/         SQLite 建表/迁移/WAL/目录可写性预检（schema.sql 一次建全）
internal/webui/auth/          bcrypt 密码、session、CSRF
internal/webui/api/           HTTP server、中间件链、/api/* 端点与 SPA 静态服务
internal/webui/orchestrator/  多租户轮询：URL 去重池、每用户策略分流、独立投递与按渠道类型限速
internal/webui/embedded/      只提交 stub.html / .gitkeep，真实产物由构建生成且 gitignore
web/                         Vue 3 + Vite + TS 源码与 build.sh
testdata/                     测试用 feed 夹具
```

base 二进制只链接两个外部模块：`go.yaml.in/yaml/v4`、`github.com/resend/resend-go/v4`；`-tags webui` 变体额外链接 `modernc.org/sqlite`（纯 Go、无需 cgo）与 `golang.org/x/crypto/bcrypt`。`go.mod` 会同时列出这些依赖，但链接器把 WebUI 依赖从 base 二进制中剔除。Atom 解析用标准库 `encoding/xml`。

> YAML 依赖说明：原 `gopkg.in/yaml.v3`（`go-yaml/yaml`）已于 2025 年 4 月被作者标记为 unmaintained；官方 YAML 组织接管的正统继任者是 `go.yaml.in/yaml/*`，其中 v3 为冻结 API（仅安全修复），**v4 是当前活跃维护的稳定版**，故本项目直接使用 v4。`github.com/goccy/go-yaml` 是另一个从零重写的独立实现，与官方继任者无关，本项目未采用。配置解析仍用经典 `Unmarshal` API 以保持行为稳定；两个自定义 `UnmarshalYAML(*yaml.Node)` 钩子的 `Node` 必须来自同一模块，否则 v4 会报迁移错误。

## 如何扩展

**新增数据源**（例如 GitHub REST API）：新建 `internal/source/githubapi/`，实现 `source.Source`（`Type()` + `Fetch(ctx, Cursor) (Result, error)`），导出匹配 `source.Factory` 的 `New`；在 `main.go` 加一行 `srcReg.Register("github_api", githubapi.New)`；配置里加 `type: github_api` 的 feed。新参数走 `feeds[].options` 透传。`domain` / `config` / `filter` / `store` / `notifier` / `watcher` / 模板 / 两个 Dockerfile（base / webui）**全部不用改**（两种构建复用同一批叶子包）。

**新增通知渠道**（例如 Matrix）：在 `internal/notifier/webhook/` 新增一个文件实现 `webhook.Adapter`（`Type()` + `Build(Message, Options) (Request, error)` + `Check(status, body) error`），在 `webhook/adapters.go` 的 `Adapters()` 里加一行即可。HTTP 客户端、重试/退避、渲染、日志、密钥展开全在共享引擎里，`main.go` / `config` / `watcher` / 模板 / 两个 Dockerfile（base / webui）**全部不用改**（两种构建复用同一批叶子包）；配置里写 `type: matrix` + `options` 即可启用，也能与现有渠道并存。

若新渠道完全不走 HTTP（例如写数据库、发 MQTT），则新建独立包实现 `notifier.Notifier`，并在 `main.go` 加一行 `notifReg.Register("xxx", xxx.New)`——`notify.channels` 会自动把它纳入扇出。

**新增状态存储**（例如 SQLite）：实现 `store.StateStore`（`Load` 用 `store.ErrNotFound` 表示首次运行），注册 + 改 `store.type` 即可。注意 WebUI 变体的 SQLite 是独立的多租户存储（`internal/webui/store/`），不走这个接口。

## 测试

```bash
go test -race -count=1 ./...
go vet ./...
gofmt -l ./cmd ./internal

# WebUI 变体：同一套代码换 build tag
go test -race -count=1 -tags webui ./...
go vet -tags webui ./...
go build ./... && go build -tags webui ./...
go list -deps ./cmd/feedwatch | grep -E 'modernc|/webui|database/sql'   # 应无输出
sh web/build.sh                 # 生成前端资源后才能看到真实页面
```

单元测试用 `httptest` 内存网络模拟 feed 与各渠道 webhook（200/204/202/304/429/5xx/畸形 XML/厂商错误码）、临时文件测状态原子写、fake sender 测 Resend 请求（含多收件人合并成 1 次调用）、可编程 fake source 覆盖 watcher 的首轮基线 / 重启静默 / 失败重试 / 单 feed 隔离 / 304 语义 / `per_feed` 部分批次失败只重试失败项目 / 基线固定汇总 / 抓取并发上限；另有 `pace` 限速门的确定性时钟用例、飞书/钉钉 HMAC 签名向量、环境变量展开、扇出失败策略的回归用例。`go test -race` 可覆盖新的并发路径。**测试不会发出真实邮件或真实群消息**。

WebUI 专项用例另覆盖：bcrypt / session / CSRF 与 cookie 属性、越权与跨用户数据隔离、渠道密钥脱敏、URL 去重只抓一次、`always` / `on_update` 分流、每用户独立投递与按渠道类型限速、投递失败重试与放弃、管理端设置校验与审计、静态资源头与未知 `/api/*` 的 JSON 404。两种构建都跑同一套 base 回归，保证 WebUI 改动不污染 base 行为。

改 workflow 后建议跑一次静态检查（需自行安装 `go install github.com/rhysd/actionlint/cmd/actionlint@latest`）：

```bash
actionlint .github/workflows/docker-publish.yml
```

## 已知限制

- 单实例设计：多个实例共写同一状态文件会竞态；WebUI 变体同理（SQLite `SetMaxOpenConns(1)` + 单个 orchestrator）。
- GitHub feed 未认证访问有限流；默认 30 分钟 × 3 个源约 6 次/小时，并启用 ETag/304、`poll.concurrency` / `request_spacing` 削峰与指数退避。
- `From` 必须是 Resend 已验证域名，否则发送会 403。
- `best_effort` 策略下，某个渠道持续失败会丢弃该渠道的后续通知（只留 ERROR 日志），以保证其它渠道不被重复轰炸；要求不漏推送请改用 `strict`。
- 各渠道都有自己的频率上限（企业微信 / 钉钉 20 条/分钟，飞书 100 条/分钟且 5 条/秒，Telegram 同一会话约 1 条/秒，Teams 4 次/秒；Resend 默认 10 次/秒/团队）。默认 30 分钟轮询 + 一轮一条 digest 远低于上限；`notify.send_spacing` 默认 250ms 只是每种渠道类型的通用保护，把 `mode` 改成 `per_feed`、同一类型加很多渠道或缩短间隔时仍需按该类型最严格的限制调整。
- 抓取限速是全局的、不区分域名；通知限速按渠道类型；两者都不做跨轮次的配额预算（每个周期独立计算）。
- 状态文件损坏时程序会报错并跳过该轮，**不会**静默当作首轮重发基线邮件；需人工检查 `data/state.json`。
- WebUI 的「全局设置」写入数据库后需**重启进程**才生效；`FEEDWATCH_WEBUI_COOKIE_SECURE` / `FEEDWATCH_WEBUI_SESSION_TTL` 只在启动时读环境变量，页面里标为只读。
- WebUI 变体的 `-dry-run` 无意义（会被忽略并记一条日志），`-once` 只跑一轮「抓取 → 生成投递计划 → 投递」后退出。
