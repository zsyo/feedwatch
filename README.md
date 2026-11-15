# FeedWatch

一个 Go 常驻程序：按配置轮询 `releases.atom`，发现**新的稳定版本**后推送通知。通知渠道支持 Resend 邮件，以及企业微信、飞书、钉钉、Telegram、Slack、Discord、Teams、Bark、ntfy 和通用 Webhook，**可同时推送到多个渠道**。数据源、通知渠道、状态存储都是可插拔接口（当前数据源仍只有 Atom）。

**两种镜像变体**：不带后缀的 base 镜像保持纯 `config.yaml` 手动配置（无前端资源、无 HTTP 服务、无 SQLite）；`-webui` 变体额外提供多用户 Web 管理界面、登录鉴权与 SQLite 存储，**空库启动、不需要任何配置文件**，首个管理员密码在容器日志里打印一次。两者由同一个 `//go:build webui` 开关隔离，base 二进制不链接任何 WebUI / SQLite 代码，详见 [WebUI 变体](#webui-变体)。

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
| 目录属主看着没问题却仍 not writable | 多半是 **rootless Docker**：容器 uid 映射到宿主的 `100000+`，与 `id -u` 无关；用命名卷或 `user: "0:0"`，见 [rootless Docker 下的 uid 映射](#rootless-docker-下的-uid-映射) |
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

两条备选：保持默认用户而给目录授权（`sudo chown <容器内 app 的 uid>:<gid> data`，或 `chmod 777 data`——所有用户可写，不推荐）；或在 compose 里把 `user` 写死成具体数字。自定义 compose 文件时，同样只需在 service 下加 `user: "1000:1000"`（换成宿主机实际值；**rootless 模式不适用，见下节**）。

**配错会在启动期就被拦住**：`jsonfile.New` 在建 store 时先用 `CreateTemp` 在状态目录探测一次并立即删除，不可写就直接启动失败（`state directory ... is not writable`），不会先发出基线邮件才发现存不下。所以现在看到的是启动报错 + 容器反复重启，而不是每半小时一封重复通知。

`data/` 已在 `.gitignore` 中，不会进仓库。

**切换等于从空状态开始**：新目录没有 `state.json`，程序会当成首次运行并重发一封基线通知。想保留已见集合，先把命名卷里的状态拷出来（真实卷名一般带项目名前缀，先用 `docker volume ls | grep feedwatch` 确认）：

```bash
docker run --rm -v feedwatch_feedwatch-data:/src -v "$PWD/data:/dst" alpine:3.21 \
  cp /src/state.json /dst/state.json
```

想保留命名卷、只指定宿主机位置，也可以给顶层卷加 `driver_opts: {type: none, o: bind, device: /绝对路径/data}`，但日常直接用 `./data` 更简单。

### rootless Docker 下的 uid 映射

以 rootless 模式运行 Docker（RootlessKit）时，上面那套「套用宿主机 uid」的做法会失效，而且失败现象和权限完全正常时一模一样。原因：容器里的 uid **N** 落到宿主机上不是 N，而是 subuid 区间里的 `100000 + (N - 1)`（典型值 100999）。宿主机目录明明是 `zephyr:zephyr 775`、`id -u` 也确实等于 1000，容器以 `user: "1000:1000"` 运行仍会 `permission denied` —— 对 100999 来说那个目录只剩 others 的 `r-x`。

```bash
grep "^$(id -un):" /etc/subuid /etc/subgid        # 确认映射起点，通常是 100000
docker info --format '{{json .SecurityOptions}}'  # 含 name=rootless 即确认是 rootless
```

三种做法，按推荐顺序：

1. **用命名卷、不写 `user:`**（默认形态）：卷在 rootless 的存储里，由镜像内 `/app/data` 的属主初始化，完全绕开宿主 uid。
2. **bind mount 时写 `user: "0:0"`**：rootless 下容器的 root 就是**宿主的登录用户**，既能写 `./data`，容器创建的文件在宿主机上也正好属于自己，`ls` / 备份 / `chown` 都符合直觉；隔离仍由 user namespace 保证，不等于拿到了宿主机 root。
3. `chown` 到映射后的 uid（`sudo chown -R 100999:100999 data`）：能用但脆弱，换机器、改 `/etc/subuid`、迁目录后都会再次失效，不推荐。

> 与上节不冲突：**别把命名卷和 `user: "0:0"` / `1000:1000` 混着用**。命名卷是用镜像内 `/app/data` 的属主（`app`，Alpine 的 `adduser -S` 从 100 起分配）初始化的，套一个不匹配的 uid 同样会在 `jsonfile` / SQLite 的启动预检处失败。

rootless 还有两点差异：容器绑不了 1024 以下端口（默认 8080 不受影响）；`restart: unless-stopped` 要真正跨宿主重启生效，需要把守护进程交给 systemd 用户实例（`systemctl --user enable --now docker`）。

## WebUI 变体

| | base | webui |
| --- | --- | --- |
| 镜像 tag | 版本号 / `latest` | 版本号加 `-webui`（如 `1.2.3-webui`），移动指针是 `webui` |
| 配置来源 | 每次启动读 `config.yaml` | **不读 `config.yaml`**：空库启动，订阅 / 通知方式 / 密钥全部在页面里配 |
| 界面 / 接口 | 无 | Vue 3 SPA + `/api/*`，默认监听 `:8080` |
| 状态 | `data/state.json`（JSON 原子写） | SQLite WAL（`/app/data/feedwatch.db` 及 `-wal` / `-shm`）+ `secret.key`；库内含订阅目录（`catalog_entries`）、审核提交（`submissions`）与远程预设状态（`catalog_preset_state`）表（schema v6） |
| 用户 | 单用户（配置即全部订阅） | 多用户 + 管理员，订阅与渠道按用户隔离 |
| 密钥来源 | 环境变量（`${ENV_VAR}` 展开） | 页面填写，AES-256-GCM 加密后存库，不可回读 |

> **二选一运行**：不要同时启动 `docker-compose.yml` 与 `docker-compose.webui.yml`。两套各有独立的轮询器与状态，会针对同一批 release 各发一遍通知。

### 启动

```bash
cp .env.example .env                   # 可选；渠道密钥不在这里配
docker compose -f docker-compose.webui.yml up -d --build
docker logs -f feedwatch-webui         # 首启随机管理员密码只在这里打印一次
```

**不需要 `config.yaml`，也不需要任何渠道密钥变量**：容器首启就是一个干净的空库，只有 `users` 表里那条 `admin` 记录、一套默认设置，以及 8 条示例订阅目录（GitHub 仓库的 `releases.atom`）和 3 个分类。示例只是可浏览的模板，**不会给任何人创建订阅**；老库升级不会自动获得，可在「目录管理 → 导入导出」加载。浏览器打开 `http://<宿主机>:8080`（端口由 `.env` 的 `WEBUI_HTTP_PORT` 控制），用日志里的 `admin` 密码登录，**首次登录会被强制改密**；改密后所有旧会话立即失效。管理员可在「用户管理」里新建账号或重置密码（新密码只在页面上显示一次）。

若经 HTTPS 反向代理访问，把 `.env` 的 `FEEDWATCH_WEBUI_COOKIE_SECURE` 设为 `true`；纯 HTTP 直连时务必保持 `false`，否则会话 cookie 带 `Secure` 标志、浏览器不会回传，导致无法登录。会话有效期由 `FEEDWATCH_WEBUI_SESSION_TTL` 控制（默认 `168h`）。

### 身份与权限

| | 管理员（`admin`） | 普通用户 |
| --- | --- | --- |
| 订阅 / 通知方式 | **看不到入口**，直接调接口返回 403 `role_not_allowed` | 增删改自己的订阅与渠道 |
| 订阅中心 / 审核中 | **看不到入口**，直接调接口返回 403 `role_not_allowed` | 浏览目录、一键订阅 / 取消、提交与撤回申请、查看全部待审链接 |
| 目录管理（侧边栏子菜单） | 五个子页：目录条目（增删改停 / 预设状态 / 接受远程更新）、审核队列、分类管理、导入导出、**远程预设同步** | 403 `forbidden` |
| 渠道密钥 | 只能看到「已配置 / 缺失」与脱敏后的主机名 | 只能看到自己填过哪些字段，值永远不回显 |
| 他人数据 | 只读下钻：订阅、渠道、投递记录 | 无 |
| 他人资源启停 | 可停用 / 恢复某个订阅或渠道，**不能改内容** | 无 |
| 用户管理 | 新建用户、重置密码、停用账号 | 只能改自己的密码 |

**想给自己配订阅，就新建一个普通用户**：管理员刻意被挡在业务配置之外，运维账号与订阅账号不会混在一起，管理员也无法借权限读到别人的 webhook / token。下钻页面的写操作只接受 `{"enabled": bool}`，多传一个字段即 400 `admin_readonly`。

订阅中心的「更新信息要审核」只针对**普通用户**：管理员是可信角色，在「目录管理 → 目录条目」里添加或编辑条目立即生效、不产生待审申请；用户提交的新增 / 更新申请则必须由管理员在「审核队列」子页通过或驳回后才写入目录。「审核中」页对全体普通用户开放，但**只能查看和手动复制**，不能直接订阅。

### 数据、主密钥与备份

- 数据库与主密钥都在命名卷 `feedwatch-webui-data:/app/data`。SQLite WAL 需要**目录**可写，启动期有和 base 一样的 `CreateTemp` 预检；把卷换成 bind mount 时同样用 `.env` 里的 `FW_UID` / `FW_GID` 指定宿主机属主——**rootless Docker 例外**，此时容器的 uid 不是宿主 uid，要么保留命名卷，要么写 `user: "0:0"`，见 [rootless Docker 下的 uid 映射](#rootless-docker-下的-uid-映射)。
- **`secret.key` 与数据库必须一起备份、一起丢弃**。首启用 `O_CREATE|O_EXCL` 生成 32 字节（0600），做渠道密钥的信封加密：`fwsec:v1:` + `base64.RawURLEncoding(nonce ‖ ciphertext ‖ tag)`，AAD 绑定渠道 id，把密文复制到别的渠道行也解不开。读失败**绝不重新生成**（那会让已存凭据全部变成无法解密的垃圾），而是启动即报错并给出三条补救路径；长度或 base64 不合法同样直接拒绝。想自己托管就设 `FEEDWATCH_WEBUI_SECRET_KEY`（标准 base64 的 32 字节），它覆盖密钥文件，**设过之后不要再换**。
- 新用户新增订阅从当前全局 seen 集合建立基线，**不会补发历史版本**；删除后重新订阅也一样。
- **订阅中心里的目录只是模板**：`catalog_categories` / `catalog_entries` / `submissions` 三张表，`category_id IS NULL` 表示「未分类」。一键订阅把目录里的 URL 复制成用户自己的一份订阅，之后编辑、停用、删除目录**都不会影响已订阅用户**。停用只是从订阅中心隐藏，仍占用 `url_key`（全局唯一索引含停用行），要释放 URL 必须删除条目。
- 用户提交的新增 / 更新申请先进 `submissions`（`pending`），**只有管理员通过才写 `catalog_entries`**；驳回必须填理由，撤回保留 `withdrawn` 历史。更新申请在提交时用 `before_json` 存下当时的原值快照，审核页与「我的提交」直接展示改前 / 改后对比。`submissions` 行永不物理删除，带 `reviewed_by` / `reviewed_at` / `reject_reason`。
- 目录 URL 会**展示给所有普通用户并允许复制**，因此入口校验比普通订阅更严：只允许 http/https，拒绝 userinfo 与含 `token` / `secret` / `key` / `password` / `auth` / `sig` 等敏感 query 键，长度上限 2048；对外响应还会再脱敏一次。已驳回的 URL 可以重新提交（唯一索引只对 `pending` 生效），每人最多 10 条待审申请。
- 「目录管理 → 导入导出」只搬运目录元数据（分类按名称匹配，不含 id），冲突策略有 `skip`（默认）/ `overwrite` / `error`（遇重复整体回滚）；导入**只写目录表**，绝不写 `subscriptions` / `feed_pool` / `user_seen`，也不含渠道凭据与投递状态。删除分类时该分类下的条目与申请统一改为「未分类」（外键 `ON DELETE SET NULL`，同一事务完成）。
- 「全局设置」页可改轮询间隔、超时、并发、抓取/发送间隔、HTTP 参数等；服务端逐项校验后写库，**重启进程后生效**（不热更新）。
- `webui.cookie_secure` / `webui.session_ttl` 是环境变量控制的只读项；`notify.mode` 在 WebUI 里不再有全局行，它是**账号级**偏好（`users.notify_mode`），在「通知方式」页顶部设置，对所有订阅与所有渠道一起生效。
- 渠道表单由服务端下发：`GET /api/channel-fields` 返回每种渠道的字段表（键名、必填、类型、选项、是否密钥），前端按 schema 渲染。新增一种渠道只需改 `internal/webui/channelspec.go` 的 `ChannelFields`，页面不用动。
- **显示时区跟着账号走**：默认「跟随服务器」（容器的 `TZ`，compose 默认 `Asia/Shanghai`），也可在「账号设置」里选任意 IANA 时区，存在 `users.timezone`，换设备登录自动同步。它只影响页面渲染：数据库里所有时间戳仍是 RFC3339 UTC，改时区不会重写任何数据。需要自己指定默认值就改 `.env` 的 `TZ`。
- 页面里的时间统一渲染成 `YYYY-MM-DD HH:mm:ss`（不带 `T` / `Z`），按上面选中的时区换算；`GET /api/meta` 同时给出 `server_timezone` 与 `server_utc_offset`，未登录页面也能标出服务器默认值。WebUI 变体额外 `import _ "time/tzdata"`，因此精简镜像里没有 tzdata 也能解析 IANA 名称。

### 远程预设目录（获取远程预设数据）

仓库里的 `presets/` 是一组**可随项目更新的纯数据文件**：`manifest.json` 列出分类文件，`language-runtime.json` / `cli-tools.json` / `editors.json` / `devops.json` 各放一类常用订阅。每条预设带一个稳定 `preset_key` 和一个单调整数 `version`（与文档格式 `format_version` 是两回事）。默认从 `https://raw.githubusercontent.com/zsyo/feedwatch/main/presets` 拉取；这些 JSON **不进运行镜像**，运行时按地址远程读取，所以上游加了订阅、部署方不必重建镜像。

管理员在「目录管理 → 远程预设 → 获取远程预设数据」点一次即可：一次请求拉完 manifest 与全部分类文件，**全部校验通过后用单事务写入**。任何一个文件 404 / 超时 / JSON 非法 / 字段冲突，就整体回滚、一行都不写，错误响应里带上每个文件的状态方便定位。

| 本地情况 | 同步行为 |
| --- | --- |
| 本地没有这个 `preset_key` | 新增目录条目（只是模板，**不创建任何用户订阅**） |
| 远程 `version` 更高 | 覆盖该条目的名称 / URL / 分类 / 简介 / 主页 / 启停状态 |
| 远程 `version` 相等或更旧 | 跳过，绝不把旧配置盖回已更新的配置 |
| 管理员手动编辑过、或用户更新申请被审核通过 | 标记「已保护」，跳过；可点该行的「接受远程更新」解除保护 |
| 管理员删除过该预设 | 写墓碑，之后永不复活 |
| 老库里同 URL 的存量条目（示例 / 手工 / 导入） | 按 `url_key` **认领**：未编辑过的按版本正常升级，编辑过的认领后仍受保护 |

「目录条目」子页的表格有「预设状态」列：`vN` + 「可更新 / 已保护」，没有 `preset_key` 的显示「手动」，已保护的条目可在操作列点「接受远程更新」解锁。同步返回新增 / 更新 / 跳过 / 受保护 / 认领五项计数与每个文件的结果；「远程预设」子页还就近列出了这几条版本 / 保护 / 墓碑 / 原子性规则。

两个设置项在「全局设置」里（保存后**需重启进程**生效，与轮询参数一致）：

- `presets.base_url`：换成 GitHub raw 镜像、CDN 或内网可达的地址。只允许 `https`、443 端口，禁止 userinfo / 查询串 / 片段，域名解析到私网、环回、链路本地或云元数据地址会被拒绝；重定向逐跳复检，最多 3 跳。保留 `HTTPS_PROXY` / `NO_PROXY` 的隐式代理支持。
- `presets.timeout`：一次同步**所有文件**的总时限（默认 `15s`，范围 1s–2m），与管单个 feed 的 `http.timeout` 相互独立。

国内网络拉不到官方 raw 时的兜底：把 `presets.base_url` 改成可访问的镜像（推荐）；或把分类 JSON 整理成 `feedwatch-catalog` v1 文档（字段 `name` / `url` / `category` / `description` / `homepage` / `enabled`）后用「目录管理 → 导入导出」手动加载。**预设 JSON 与导入文档是两种不同格式，不能直接把下载下来的分类文件丢进导入框**；导入仍然是无版本概念的 `skip` / `overwrite` / `error` 三策略，与远程同步是两条独立路径。

**注意**：默认地址读的是仓库 `main` 分支上的 `presets/`，只有这些文件被推送到 GitHub 后才存在。本地改在未推送的分支 / 旧 tag 上开发时点按钮会拿到 404，这是预期行为，不是故障；验证时把 `presets.base_url` 换成可访问的镜像即可。

### 通知语义

- 同一 URL 全局每轮只抓一次（`url_key = sha256(规范化 URL)`），抓完按每个用户各自的过滤条件、`always` / `on_update` 分流。`digest` / `per_feed` 由账号决定而不是订阅：同一个用户的所有订阅共用一个形式，一轮里不会同时产出「汇总一条」和「每个项目一条」。
- 订阅中心的「一键订阅」走和手动新增**完全相同**的三写事务（`subscriptions` + `feed_pool` + 复制当前 `user_seen` 基线），同样从当前基线开始、不会补发历史版本；因此它是「复制模板」，不是引用模板。目录显示「已订阅」按 `url_key` 判定，用户在「我的订阅」里改了 URL 之后，该目录卡片会重新显示为未订阅。
- **不做跨用户合并**：每个用户 × 每个渠道一次独立请求，使用各自的凭据，这样每个用户都能在 Resend 等后台看到自己的发送记录。限速按**渠道类型**分别计算（email / telegram / wecom …各一个独立门），不同服务互不阻塞。
- 渠道密钥**加密落库，运行期不读任何环境变量**。页面提交的是字段值本身，服务端 `Seal` 后写入 `channel_secrets`；列表与下钻接口只返回 `secret_fields`（哪些字段已配）、`secret_hints`（仅 `scheme://host`）和 `secret_state`（`ok` / `missing` / `error` / `none`），任何明文或 `fwsec:` 信封都不会出现在响应与日志里。编辑时**留空 = 保持不变**，要清除就显式传 `clear_secrets`；把密钥塞进普通字段会被 400 `secret_in_options` 拒绝。
- 通知内容在抓取成功后就物化入库，投递失败只重试已渲染的投递组，不重新抓 feed、也不阻塞全局已见集合。

### 页面与本地开发

页面：`/login`、概览、**订阅中心 `/catalog`**（侧边栏排在「我的订阅」之前）、我的订阅、通知方式、账号设置；**审核中 `/catalog/pending`** 是订阅中心内的子页，从订阅中心页头的「审核中的链接」进入、可返回，不再单列在侧边栏；停留在该子页时侧边栏的「订阅中心」仍保持高亮。管理员另有用户管理（含单个用户下钻 `/admin/users/:id`，只读 + 启停）、**目录管理**、轮询池、限速状态、全局设置、审计日志。目录管理在侧边栏是一个**上下文分组**：只有停在 `/admin/catalog*` 时才展开 5 个缩进子项，其它管理页仍是 8 项短侧边栏；父项「目录管理」只作分组入口（不会被当作当前页高亮），当前子项单独高亮。旧地址 `/admin/catalog` 保留为兼容重定向到「目录条目」，旧书签和概览页快捷入口都继续可用。普通用户看不到管理端入口，管理员看不到订阅 / 渠道 / 订阅中心的入口，后端角色守卫双向拦截。

订阅中心支持关键字（名称 / URL / 简介 / 分类名）、分类筛选与按订阅人数 / 名称 / 分类排序，卡片直接显示「已订阅」与取消入口；页面下方是「提交新订阅」和「我的提交」（状态、驳回理由、改前 / 改后对比、撤回）。审核中子页是只读的，只提供「复制信息」并常驻风险提示。目录管理拆成 5 个专注子页，每页只放一到两张卡片：**目录条目 `/admin/catalog/entries`**（手动添加 + 条目表，含编辑 / 停用 / 删除 / 预设状态 / 接受远程更新）、**审核队列 `/admin/catalog/reviews`**（待审与全部历史、改前 / 改后对比、通过 / 驳回理由）、**分类管理 `/admin/catalog/categories`**（添加 / 重命名 / 删除）、**导入导出 `/admin/catalog/transfer`**（下载 JSON、冲突策略、导入结果）、**远程预设 `/admin/catalog/presets`**（同步按钮、逐文件结果、就近的同步规则说明）。子页共享 `web/src/catalogAdmin.ts` 里的响应类型、请求封装、提示状态与子菜单定义，但各自持有独立的错误 / 成功提示，不会把上一页的消息带过来。搜索 / 筛选 / 排序目前是**前端本地**计算（一次返回全量目录），与现有列表页范式一致。

```bash
make build-webui                 # 或 sh web/build.sh，产物写入 internal/webui/embedded/（已 gitignore）
go build -tags webui -o feedwatch-webui.exe ./cmd/feedwatch
FEEDWATCH_DB_PATH=data/feedwatch.db FEEDWATCH_HTTP_ADDR=127.0.0.1:8080 ./feedwatch-webui.exe
./feedwatch-webui.exe -once      # 离线冒烟：抓一轮 → 生成投递计划 → 投递一轮后退出
```

PowerShell 里用等价的 `web/build.ps1`（Windows PowerShell 5.1 与 PowerShell 7 都可），不需要 `sh` / MSYS 环境：

```powershell
.\web\build.ps1                  # 产物同样写入 internal\webui\embedded\
.\web\build.ps1 -SkipDeps        # 完全不碰依赖（等价 FW_SKIP_DEPS=1）
.\web\build.ps1 -ForceDeps       # 强制 npm ci，会重写 node_modules（等价 FW_FORCE_DEPS=1）
.\web\build.ps1 -OutDir .\out    # 换输出目录
```

这两个入口共用同一套指纹与 `BUILD_ID` 规则，可以随意交替使用：在 `sh` 下装好的依赖，用 `ps1` 构建时不会被判定过期，反之亦然。

WebUI 变体**不接受 `-config`**：传了只会记一条 `-config is ignored; the WebUI variant is configured entirely in the browser` 然后继续；`-dry-run` 同样无意义。干净 clone 下 `-tags webui` 也能编译，页面只显示占位提示（仓库只提交 `embedded/stub.html`），真实前端由 `make build-webui` 或 `Dockerfile.webui` 的 Node stage 生成。

前端构建需要 Node **22.12+**（Vite 8 的 `engines` 要求，`node:22-alpine` 已满足）；依赖版本见 `web/package.json`，`npm run typecheck` 可单独跑 `tsc --noEmit`。改完前端依赖后记得重新跑一次构建脚本（`sh web/build.sh` 或 `web/build.ps1`）再跑 `go build -tags webui`，否则嵌进去的还是旧产物。

`sh web/build.sh` 与 `web/build.ps1` **不再每次重建 `node_modules`**：它们把 `package.json` / `package-lock.json` 的内容和 Node 平台（`platform-arch-abi`）一起做指纹存进 `web/node_modules/.cache/feedwatch-deps`，指纹没变就直接跳过安装；变了才 `npm install` 增量补齐（同一目录在 Windows 与 WSL 之间切换时平台指纹不同，会各自补齐对应平台的二进制包）。只有目录不存在或显式 `FW_FORCE_DEPS=1`（ps1 是 `-ForceDeps`）时才走 `npm ci` 全量重装，`FW_SKIP_DEPS=1`（ps1 是 `-SkipDeps`）则完全不碰依赖。

产物里的 `BUILD_ID`（`/api/meta` 的 `build_id`，侧边栏与概览页都显示）形如 `11142242-2f04086`：构建机本地时间的月日时分 + git 短哈希，不含秒，便于一眼比对两次构建（镜像构建跑在 UTC 下，所以 Docker 产物的时间戳是 UTC）。

> WebUI 仍是**单实例**：SQLite `SetMaxOpenConns(1)` + 单个后台 orchestrator，多实例写同一个库会产生写竞态与重复投递。

### 从 1.0 及更早的 WebUI 升级

1.1.0 起不再读 `config.yaml`，渠道密钥改为页面填写 + 加密落库。旧库能直接打开（`PRAGMA user_version` 从 1 迁到 2，只新增 `channel_secrets` 等表，不动旧数据），但**旧渠道里存的还是环境变量名 / 占位符，无法自动转成密文**，需要在「通知方式」页把凭据重填一次，否则 `secret_state` 会显示 `missing`、投递报缺少凭据。

`PRAGMA user_version` 的迁移是累加式的，每一步只补表 / 补列，不重建已有表、不动业务数据：

| schema 版本 | 变化 |
| --- | --- |
| v2 | `channel_secrets` 等渠道密钥表 |
| v3 | `users.timezone`（账号显示时区） |
| v4 | `users.notify_mode`（通知形式改为账号级）；老库残留的 `subscriptions.mode` 列不读不写 |
| v5 | `catalog_categories` / `catalog_entries` / `submissions`（订阅中心） |
| v6 | `catalog_preset_state` 表 + `catalog_entries` 的 `preset_key` / `preset_version` / `preset_edited` 三列与偏唯一索引（远程预设） |

v5 **只新增三张表**，老库升级后不会自动获得示例目录（种子只在 `users` 为空时执行）；想预置示例，可在「目录管理 → 导入导出」加载一份从新库导出的 JSON。v6 通过 `ensureColumn()` 给老库的 `catalog_entries` 补三列（默认 `''` / `0` / `0`），并在补列之后才建偏唯一索引 `WHERE preset_key <> ''`（存量行都是空串，不受影响）；**不会重建表、不改动业务数据**。升级后第一次点「获取远程预设数据」，会按 URL 认领已有的示例 / 手工 / 导入条目并按版本升级，不会生成重复行。其余功能升级后直接可用，无需手工改数据。备份时数据库与 `secret.key` 一起带上即可，目录与预设状态都在同一个库里。

不想保留旧数据就干脆删卷重来（`docker compose -f docker-compose.webui.yml down -v`），首启会重新生成管理员密码与新密钥。**`data/` 卷与 `secret.key` 必须同进同退**，只留其一没有任何意义。

## 发布镜像（GitHub Actions / GHCR）

`.github/workflows/docker-publish.yml` 在推送 `v*` tag 时用 `matrix.variant: [base, webui]` **并行产出两组**镜像，每组各自是 **linux/amd64 + linux/arm64** 双架构 manifest，发布到 GitHub Container Registry：`ghcr.io/zsyo/feedwatch`。base 组沿用常规的版本号 tag 与 `latest`；webui 组是「版本号 + `-webui` 后缀」，它的移动指针是独立的 **`webui`** tag（而不是 `latest-webui`），两组的指针互不覆盖。Docker 会按宿主机架构自动挑选 manifest，同一条 `pull` 命令在 x86 服务器和 ARM 板子上都能用。镜像名由 `${{ github.repository }}` 自动生成，fork 后会变成 `ghcr.io/<你的用户名>/feedwatch`，无需改 workflow。

### 发布一个新版本

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

tag 必须是 `vMAJOR.MINOR.PATCH`（允许 `-rc.1` 一类后缀）；格式不对时 workflow 立即报错退出，不会推镜像。一个 tag 同时触发两个 matrix job，`fail-fast: false` 保证其中一组失败不会取消另一组。

| Git tag | base 组镜像 tag | webui 组镜像 tag |
| --- | --- | --- |
| `v1.2.3` | `1.2.3`、`1.2`、`latest` | `1.2.3-webui`、`1.2-webui`、`webui` |
| `v1.2.3-rc.1` | `1.2.3-rc.1` | `1.2.3-rc.1-webui`（预发布只新增自己的 tag，不移动任何指针） |

> 本节里的 `v1.2.3` / `1.2.3` 都只是示意，不代表某个已发布版本。通用规则：base 组按 `{{version}}` / `{{major}}.{{minor}}` / `latest` 生成，webui 组把前两者加 `-webui` 后缀，移动指针固定叫 `webui`。每次正式发版两组都会新增完整版本号 tag，并各自移动自己的指针（base 的 `latest`、webui 的 `webui`），`<主版本>.<次版本>` 只跟随各自的版本号；预发布只新增自己的 tag，两个指针都不动。已发布的具体版本以仓库 Releases / GHCR package 的 tag 列表为准，固定版本时用 `docker pull ghcr.io/zsyo/feedwatch:<版本>` 或 `docker pull ghcr.io/zsyo/feedwatch:<版本>-webui`。

CI 用 `matrix.variant` 切换 `file:`（`Dockerfile` / `Dockerfile.webui`）。webui 组的 `-webui` 后缀直接写在每个 semver pattern 里（`{{version}}-webui`），移动指针由 `type=raw,value=webui` 单独给出：metadata-action 的 `flavor.suffix` 会作用到**所有**生成的 tag，用它加后缀会把指针写成 `webui-webui`；同时 webui 组设 `latest=false`，避免它和 base 组抢同一个 `latest` tag。两组 layer cache 按 variant 隔离，互不挤占。tag 通过 `-ldflags "-X main.version=..."` 注入二进制，容器内 `./feedwatch -version` 能直接看到版本。**无需配置任何额外 secret**：用内置 `GITHUB_TOKEN` + `packages: write` 登录 GHCR，构建缓存走 GitHub Actions cache。

### 先发预发布（rc），验证通过再转正式版

破坏性改动、或改动了镜像内文件（Dockerfile / 配置模板 / 前端产物）时，建议先发一个 rc，让指针保持在上一个稳定版，验证没问题再转正。

**① 打 rc**（在待发布的提交上）：

```bash
git tag -a v1.2.3-rc.1 -m "release v1.2.3-rc.1"
git push origin v1.2.3-rc.1
```

只新增 `1.2.3-rc.1` 与 `1.2.3-rc.1-webui` 两个 tag，**`latest` 与 `webui` 都不动**——此时别人 `pull` 指针拿到的仍是上一个正式版，不会被半成品波及。

**② 用固定 tag 验证**（不要用指针，避免验证期间指针被别的发布会话改动）：

```bash
docker pull ghcr.io/zsyo/feedwatch:1.2.3-rc.1-webui
docker run -d --name fw-rc -p 18080:8080 ghcr.io/zsyo/feedwatch:1.2.3-rc.1-webui
docker logs fw-rc     # 空库首启：master key、admin 密码、listening
# 验完清理（容器里没有卷，删掉即不留痕迹）
docker rm -f fw-rc
```

检查项：首启能拿到一次性管理员密码并完成首登改密 → 建普通用户 → 确认侧边栏「订阅中心」排在「我的订阅」之上、且没有平级的「审核中」项 → 订阅中心能看到示例目录 → 从页头进入审核中子页时「订阅中心」仍高亮、可返回 → 一键订阅后「我的订阅」出现且没有补发历史 → 提交新增 / 更新申请 → 「审核中」页显示风险提示与改前 / 改后对比、可复制 → 进「目录管理」确认侧边栏展开 5 个子项、离开分组（例如去「全局设置」）自动收起、父项与子项不会同时高亮 → 逐个打开 5 个子页确认标题与数据，并确认旧链接 `/admin/catalog` 会跳到「目录条目」 → 管理员在「审核队列」通过 / 驳回、在「分类管理」增删分类、在「导入导出」往返一次 → 在「全局设置」确认 `presets.base_url` / `presets.timeout` 两项及说明 → 把 `presets.base_url` 临时改成一个可访问的镜像并重启，在「远程预设」子页点「获取远程预设数据」看到逐文件结果与计数，重复点击只跳过、不重复新增 → 在「目录条目」子页手动编辑某条预设后确认它变「已保护」且不被更高版本覆盖、点「接受远程更新」后恢复可更新 → 在「目录条目」子页删除一条预设后再次同步不会复活 → 用户侧状态与驳回理由正确 → 在页面上建渠道（含密钥）→ 触发一轮抓取与投递 → 投递记录与 `secret_state` 符合预期。base 变体则用 `-once -dry-run` 跑一遍配置解析与渲染。**升过级的旧数据要单独验**：拿一份旧卷副本挂上去，确认能启动、v5 补出三张目录表、v6 补出预设三列与 `catalog_preset_state`、渠道凭据按文档重填后能发出去，且首次远程同步能认领存量条目而非重复插入。

**③ 发现问题就发下一个 rc**：改代码 → 新提交 → `v1.2.3-rc.2`。**不要重推同名 rc tag**：GHCR 上的同名 tag 会被覆盖，已经拉过 `rc.1` 的人将无法复现当时的镜像。

**④ 通过后打正式 tag**，指针才会前移：

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

产出 `1.2.3` / `1.2` / `latest` 与 `1.2.3-webui` / `1.2-webui` / `webui`。

> 正式 tag 必须打**实际验证过的那个提交**（或在其之上重新验证新的 HEAD）。rc 之后又在 `main` 上提了代码却直接把正式 tag 打在 HEAD 上，等于发布了没测过的内容。
>
> rc 的 tag 与镜像会一直留在 GHCR 作为历史，不必删除；但要清楚 `docker pull …:1.2.3-rc.1` 拿到的是预发布版本，不要把它当成正式版的等价物写进部署文档。

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

WebUI 变体用 `webui` 指针 tag（或换成固定的 `1.2.3-webui`），多映射一个端口并用自己的数据卷；**不需要 `config.yaml`，也不需要任何渠道密钥变量**，首次启动的管理员密码只在日志里打印一次：

```bash
docker pull ghcr.io/zsyo/feedwatch:webui
docker run -d --name feedwatch-webui \
  -p 8080:8080 \
  -v feedwatch-webui-data:/app/data \
  ghcr.io/zsyo/feedwatch:webui
docker logs -f feedwatch-webui
```

> 登录后在页面里建一个普通用户来配订阅与通知方式（管理员不能配，见[身份与权限](#身份与权限)）；密钥填在页面里，加密后存库，不再经过环境变量。

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

`--user "$(id -u):$(id -g)"` 与 compose 里的 `FW_UID` / `FW_GID` 覆盖等价；同样要求当前用户就是 `data/` 的属主，原理见上一节。**rootless Docker 下要改成 `--user 0:0`**：容器里的 uid 1000 映射到宿主的 100999，写不进您自己 1000 拥有的目录，而容器 root 恰好等于宿主的登录用户。

> 用多通道时，把对应的 `-e WECOM_WEBHOOK_URL=...`、`-e TELEGRAM_BOT_TOKEN=...` 等一并加进 `docker run`（或写进 compose 的 `environment`）；漏掉的变量会在启动日志里直接报出名字。

改用 compose：删掉 `docker-compose.yml` 里的 `build: .`，把 `image` 换成远程镜像，`environment` / `volumes` / `restart` / `user` 全部原样保留：

```yaml
services:
  feedwatch:
    image: ghcr.io/zsyo/feedwatch:latest   # 也可固定到某个已发布的具体版本 tag
    # build: .                             # 删除这一行
```

然后 `docker compose pull && docker compose up -d`。WebUI 变体对应独立的 `docker-compose.webui.yml`：把 `image` 换成 `ghcr.io/zsyo/feedwatch:webui` 并删掉 `build:` 段，`ports` / 独立数据卷 / `environment` 原样保留。

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
internal/webui/               WebUI 变体（全部 //go:build webui）：首启只建 admin、渠道字段表、URL 规范化、embed 资源
internal/webui/catalogspec.go 订阅目录的字段上限、URL 安全校验、内置示例、导入导出结构（Go 侧唯一真源）
internal/webui/secrets/       主密钥加载与 AES-256-GCM 信封加密（唯一知道密钥的包）
internal/webui/store/         SQLite 建表/迁移/WAL/目录可写性预检（schema v6，含三张目录表与预设状态表）
internal/webui/auth/          bcrypt 密码、session、CSRF
internal/webui/presets/       远程预设的 JSON 校验、加载器与 SSRF 防护（纯函数 + 网络，不碰数据库）
internal/webui/api/           HTTP server、中间件链、/api/* 端点与 SPA 静态服务；catalog.go / catalog_admin.go / catalog_presets.go 是订阅中心与远程同步
internal/webui/orchestrator/  多租户轮询：URL 去重池、每用户策略分流、独立投递与按渠道类型限速
internal/webui/embedded/      只提交 stub.html / .gitkeep，真实产物由构建生成且 gitignore
web/                         Vue 3 + Vite + TS 源码；CatalogView / CatalogPendingView、5 个 AdminCatalog* 子页 View、catalogAdmin.ts / presets.ts 与 build.sh / build.ps1
presets/                     远程预设数据：manifest.json + 4 个分类 JSON（运行时远程读取，不进镜像）
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
sh web/build.sh                 # 生成前端资源后才能看到真实页面（PowerShell 用 .\web\build.ps1）
```

单元测试用 `httptest` 内存网络模拟 feed 与各渠道 webhook（200/204/202/304/429/5xx/畸形 XML/厂商错误码）、临时文件测状态原子写、fake sender 测 Resend 请求（含多收件人合并成 1 次调用）、可编程 fake source 覆盖 watcher 的首轮基线 / 重启静默 / 失败重试 / 单 feed 隔离 / 304 语义 / `per_feed` 部分批次失败只重试失败项目 / 基线固定汇总 / 抓取并发上限；另有 `pace` 限速门的确定性时钟用例、飞书/钉钉 HMAC 签名向量、环境变量展开、扇出失败策略的回归用例。`go test -race` 可覆盖新的并发路径。**测试不会发出真实邮件或真实群消息**。

WebUI 专项用例另覆盖：bcrypt / session / CSRF 与 cookie 属性、越权与跨用户数据隔离、URL 去重只抓一次、`always` / `on_update` 分流、每用户独立投递与按渠道类型限速、投递失败重试与放弃、管理端设置校验与审计、静态资源头与未知 `/api/*` 的 JSON 404。密钥链路单独测：信封往返与随机 nonce、密文 / AAD 篡改、未知前缀、长度 31 / 33 拒绝、`LoadOrCreate` 建 0600 文件且**不覆盖**已存在的损坏文件、环境变量优先、坏 base64 不落盘；接口层断言创建的密钥落库为 `fwsec:v1:` 且任何响应里都没有明文、留空编辑保持不变、管理员下钻只读（多传字段即 400）、种子只建 admin 且业务表全 0。两种构建都跑同一套 base 回归，保证 WebUI 改动不污染 base 行为。

订阅中心专项用例：v4→v5 迁移只补三张表且保留用户数据、外键 `ON DELETE SET NULL`、pending 部分唯一索引（驳回 / 撤回后可重提）、示例目录只是模板（不写 `subscriptions` / `feed_pool` / `user_seen`）、目录 URL 的协议 / userinfo / 敏感 query / 长度与别名归一化校验、admin 与 member 的双向角色守卫、**一键订阅复制基线不重放历史**、去重防刷与待审上限、新增与更新两类审核流（含 `before_json` 快照、`entry_gone` / `conflict_url`）、撤回、驳回理由可见、分类删除级联、管理员手动编辑不产生申请、导入导出往返与三种冲突策略。改前端后还要在 `web/` 目录下跑 `npm run typecheck`，并重新执行构建脚本再 `go build -tags webui`。

远程预设专项用例：仓库内 `presets/*.json` 直接用生产校验器验一遍（防止拼错 key / 重复 URL 混入发版数据）、manifest 与分类文件的格式/文件名/分类漂移、跨文件 `preset_key` 与 `url_key` 去重、SSRF 的协议 / 端口 / userinfo / 私网与云元数据 IP / DNS 解析复检 / 重定向逐跳复检 / 跳数上限 / 1 MiB 体积上限 / 上下文取消、加载器的逐文件状态与「失败文件之后不再写」、v5→v6 补列与偏唯一索引；接口层覆盖只升不降、连续多版本升级后 `preset_edited` 仍为 0、同版本重复同步、存量条目区分认领、管理员编辑与审核通过置保护、`accept-remote` 解锁、墓碑不复活、拉取失败与 `preset_invalid_document` 零写入、事务内 `url_key` 冲突时**把先前的更新一并回滚**、缺少 loader 的报错、目录列表暴露三个预设状态字段。全部用 `httptest` 本地服务器，不发真实网络请求。

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
- 订阅中心的搜索 / 筛选 / 排序是浏览器对一次返回的全量目录做本地计算；目录量级显著变大后需改成后端分页，并**同时移除本地过滤**（否则只会过滤当前页）。
- 「审核中」页会把待审 URL 明文展示给全体普通用户并允许复制，所以目录 URL 的 http/https、无 userinfo、无敏感 query 校验**不能放宽**；FeedWatch 不验证这些链接的内容、可用性与安全性，手动复制添加前需自行判断。
- `presets.base_url` 默认指向官方仓库的 `main` 分支，**这些文件必须先推送到 GitHub 才存在**；本地开发或使用未推送的 tag 时同步必然 404，需要临时把基址换成可访问的镜像。`presets.base_url` / `presets.timeout` 与轮询参数一样保存后需**重启进程**生效。
- 远程预设的 SSRF 防护按「网络属性」判定而非域名白名单（管理员要能换任意镜像）：会拒绝私网 / 环回 / 链路本地 / 云元数据 IP，但对**代理侧发生的 DNS rebinding** 无能为力；自建代理或镜像时请自行保证上游可信。
- 一次同步用 `SetMaxOpenConns(1)` 的单个事务写完整批预设，文件较多时会短暂占用唯一的 SQLite 连接；网络 IO 与文档校验都放在 `Begin()` 之前以缩短占用时间。
- 管理员把某条目录条目的 URL 改成与某个远程预设相同、或远程预设之间出现 URL 冲突时，整批同步返回 409 `preset_conflict` 并**整体回滚**（包括本已成功的条目），需先解决冲突再重试。
- 远程预设的 `version` 只允许单调前进：上游误把版本调小会被静默跳过（计入 `skipped`），不会把本地回退；管理员删除过的预设会留下墓碑，同步永不复活，也没有页面入口清除墓碑（需要时直接改 `catalog_preset_state.deleted`）。
- 预设 JSON 与「导入」使用的 `feedwatch-catalog` v1 是两种格式，下载下来的分类文件不能直接丢进导入框；需要离线兜底时请按导入格式整理，或把 `presets.base_url` 指向可访问的镜像。
