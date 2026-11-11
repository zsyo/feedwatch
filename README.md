# FeedWatch

一个 Go 常驻程序：按配置轮询 `releases.atom`，发现**新的稳定版本**后推送通知。通知渠道支持 Resend 邮件，以及企业微信、飞书、钉钉、Telegram、Slack、Discord、Teams、Bark、ntfy 和通用 Webhook，**可同时推送到多个渠道**。数据源、通知渠道、状态存储都是可插拔接口（当前数据源仍只有 Atom）。

当前默认监控三个仓库（均已验证可用）：

| 项目 | Atom 源 |
| --- | --- |
| golang/go | `https://github.com/golang/go/releases.atom` |
| rust-lang/rust | `https://github.com/rust-lang/rust/releases.atom` |
| python/cpython | `https://github.com/python/cpython/releases.atom` |

## 行为约定

| 场景 | 行为 |
| --- | --- |
| **首次启动**（没有状态文件） | 抓取全部 feed，发 **1 封「当前版本基线」汇总邮件**，列出每个项目当前最新稳定版；发信成功后才把抓到的所有条目标记为已见 |
| **之后每轮** | 只对新增的稳定版发通知，**一轮合并成 1 封汇总邮件** |
| **重启容器 / 重启进程但无新版本** | **完全静默**，不调用任何通知渠道 |
| 只有一个渠道且发送失败 | **不提交状态**，新 ID 保持未见，下一轮自动重试（at-least-once） |
| 多通道部分失败 | 默认 `best_effort`：只要有一个渠道送达就提交状态（失败渠道只记日志，不重试、不产生重复推送）；`strict` 则整批报错重试 |
| 某个 feed 抓取失败 | 跳过该 feed，其它 feed 正常通知；首轮若有任一 feed 失败则整封基线通知推迟 |
| HTTP 304 | 不做 diff、不改写已见集合、不发通知 |
| 预发布 / 非版本标签 | 默认不通知（`go1.27rc3`、`v3.16.0a1`、CPython 的 `3.10` 分支 tag 等），但 ID 仍记为已见 |

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

`notify.channels` 是唯一的通知配置写法：列表里每一条是一个渠道，同一条通知会依次推送给所有 `enabled` 的渠道，互不干扰。只推一个渠道就写一条（单元素列表），不需要另外一套配置。**`notify` 外层只放全局项**（`mode` / `subject_prefix` / `options` / `on_partial_failure`）；每个渠道的参数都写在自己的条目里——webhook 类放 `options`，邮件放 `email` 子块。旧版的 `notify.type` 与顶层 `notify.email` 都已移除，升级时把它们整体挪进对应的渠道条目即可。

`notify.mode` 是全局项、与通道无关，所以写在 `notify` 第一项：`digest`（默认）每轮把新版本汇总成 1 条，`per_feed` 按项目拆成多条；每条都会复制给所有 enabled 渠道。首轮基线通知固定是 1 条汇总，不受 `per_feed` 影响。`subject_prefix` 是所有渠道共用的标题前缀（可在渠道的 `options.subject_prefix` 覆盖），`notify.options` 是所有渠道共享的默认值，会被各渠道自己的 `options` 覆盖。

```yaml
notify:
  mode: digest                      # 全局，与通道无关：digest | per_feed
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

watcher 的语义是「通知返回错误 → 不提交已见集合 → 下轮整体重试」。多通道下这会造成**已成功的渠道被重复推送**，因此：

- `best_effort`（默认）：至少一个渠道成功即提交状态；失败渠道打 `ERROR` 日志并放弃该次推送，不产生重复。适合 IM 群通知。
- `strict`：任一渠道失败就整体报错重试，保证每个渠道都不漏，代价是可能重复（例如某个群机器人长期失效时，其它群会每轮都收到同一批）。

只配置一个渠道时两者行为一致：失败就报错重试。

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

镜像为多阶段构建（`golang:1.27-alpine` → `alpine:3.21`），静态二进制、非 root 用户、含 CA 证书与时区；编译阶段用 BuildKit 的 `TARGETOS` / `TARGETARCH` 交叉编译，因此同一份 Dockerfile 可产出 amd64 / arm64 镜像。配置通过只读挂载传入，各渠道凭证通过 `docker-compose.yml` 的 `environment` 从 `.env` 注入（新增自定义变量时记得在那里补一行），状态通过命名卷 `feedwatch-data:/app/data` 持久化。

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

容器以非 root 的 `app` 用户运行，宿主机目录得允许它写入，否则状态文件保存会失败（WSL + Docker Desktop 的文件共享通常较宽松，Linux 服务器上必须处理）：

```bash
mkdir -p data
chmod 777 data                        # 或更精确：sudo chown <uid>:<gid> data
docker compose up -d
docker compose exec feedwatch id       # 查容器内实际 uid/gid 后再精确授权
```

`data/` 已在 `.gitignore` 中，不会进仓库。

**切换等于从空状态开始**：新目录没有 `state.json`，程序会当成首次运行并重发一封基线通知。想保留已见集合，先把命名卷里的状态拷出来（真实卷名一般带项目名前缀，先用 `docker volume ls | grep feedwatch` 确认）：

```bash
docker run --rm -v feedwatch_feedwatch-data:/src -v "$PWD/data:/dst" alpine:3.21 \
  cp /src/state.json /dst/state.json
```

想保留命名卷、只指定宿主机位置，也可以给顶层卷加 `driver_opts: {type: none, o: bind, device: /绝对路径/data}`，但日常直接用 `./data` 更简单。

## 发布镜像（GitHub Actions / GHCR）

`.github/workflows/docker-publish.yml` 在推送 `v*` tag 时自动构建 **linux/amd64 + linux/arm64** 双架构镜像，发布到 GitHub Container Registry：`ghcr.io/zsyo/feedwatch`。Docker 会按宿主机架构自动挑选 manifest，同一条 `pull` 命令在 x86 服务器和 ARM 板子上都能用。镜像名由 `${{ github.repository }}` 自动生成，fork 后会变成 `ghcr.io/<你的用户名>/feedwatch`，无需改 workflow。

### 发布一个新版本

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

tag 必须是 `vMAJOR.MINOR.PATCH`（允许 `-rc.1` 一类后缀）；格式不对时 workflow 立即报错退出，不会推镜像。

| Git tag | 生成的镜像 tag |
| --- | --- |
| `v1.2.3` | `1.2.3`、`1.2`、`latest` |
| `v1.2.3-rc.1` | `1.2.3-rc.1`（预发布不会移动 `latest`） |

> 表中是假设的版本号，用于说明映射规则；本项目当前实际发布的是 `v1.0.0`，对应镜像 `1.0.0` / `1.0` / `latest`。

CI 会把 tag 通过 `-ldflags "-X main.version=..."` 注入二进制，容器内 `./feedwatch -version` 能直接看到版本。**无需配置任何额外 secret**：用内置 `GITHUB_TOKEN` + `packages: write` 登录 GHCR，构建缓存走 GitHub Actions cache。

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

> 用多通道时，把对应的 `-e WECOM_WEBHOOK_URL=...`、`-e TELEGRAM_BOT_TOKEN=...` 等一并加进 `docker run`（或写进 compose 的 `environment`）；漏掉的变量会在启动日志里直接报出名字。

改用 compose：删掉 `docker-compose.yml` 里的 `build: .`，把 `image` 换成远程镜像，`environment` / `volumes` / `restart` 全部原样保留：

```yaml
services:
  feedwatch:
    image: ghcr.io/zsyo/feedwatch:latest   # 也可指定具体的版本如 1.0.0
    # build: .                             # 删除这一行
```

然后 `docker compose pull && docker compose up -d`。

### 双架构是怎么构建的

`Dockerfile` 编译阶段固定 `FROM --platform=$BUILDPLATFORM`，用 BuildKit 自动注入的 `TARGETOS` / `TARGETARCH` 做 `CGO_ENABLED=0` 交叉编译，Go 工具链始终跑在 runner 原生 amd64 上；QEMU 只负责运行阶段的 `apk add` / `adduser`（arm64 变体），比在 QEMU 里编译整个 Go 工具链快得多。`golang:1.27-alpine` 与 `alpine:3.21` 的官方镜像均含 `arm64v8` 变体。

本地 `docker compose build` 不带 `--platform`，只产出**本机架构**镜像；要复现双架构需 `docker buildx build --platform linux/amd64,linux/arm64 .`（多架构 manifest 只能推到 registry，`--load` 只支持单平台）。Dockerfile 依赖 BuildKit 自动注入的平台变量（Docker 23+ 默认启用）。

> 生产环境建议把 workflow 里的 action 固定到 commit SHA（GitHub 官方安全建议）；当前用的是 major tag：`actions/checkout@v7`、`docker/setup-qemu-action@v4`、`docker/setup-buildx-action@v4`、`docker/login-action@v4`、`docker/metadata-action@v6`、`docker/build-push-action@v7`。

## 目录结构

```
.github/workflows/docker-publish.yml  tag 触发的 GHCR 双架构发布
cmd/feedwatch/main.go          唯一组合根：注册实现、注入配置、启动调度
internal/domain/              领域模型（零内部依赖）
internal/config/              YAML + env 覆盖 + 校验
internal/source/              Source 接口 / Registry
internal/source/atom/         Atom/RSS 解析、条件请求、重试
internal/filter/              稳定版过滤规则
internal/notifier/            Notifier 接口 / Registry / Multi 扇出与失败策略
internal/notifier/template/   go:embed 邮件与 IM 消息模板（基线 / 新版本共用）
internal/notifier/email/      Resend 实现
internal/notifier/log/        日志通知器（本地调试 / dry-run）
internal/notifier/webhook/    共享 HTTP 引擎 + 10 个渠道 Adapter（每个渠道一个文件）
internal/store/               StateStore 接口 / Registry
internal/store/jsonfile/      JSON 原子写状态
internal/watcher/             核心编排：抓取 → 过滤 → diff → 通知 → 提交
testdata/                     测试用 feed 夹具
```

依赖只有两个外部模块：`go.yaml.in/yaml/v4`、`github.com/resend/resend-go/v4`。Atom 解析用标准库 `encoding/xml`。

> YAML 依赖说明：原 `gopkg.in/yaml.v3`（`go-yaml/yaml`）已于 2025 年 4 月被作者标记为 unmaintained；官方 YAML 组织接管的正统继任者是 `go.yaml.in/yaml/*`，其中 v3 为冻结 API（仅安全修复），**v4 是当前活跃维护的稳定版**，故本项目直接使用 v4。`github.com/goccy/go-yaml` 是另一个从零重写的独立实现，与官方继任者无关，本项目未采用。配置解析仍用经典 `Unmarshal` API 以保持行为稳定；两个自定义 `UnmarshalYAML(*yaml.Node)` 钩子的 `Node` 必须来自同一模块，否则 v4 会报迁移错误。

## 如何扩展

**新增数据源**（例如 GitHub REST API）：新建 `internal/source/githubapi/`，实现 `source.Source`（`Type()` + `Fetch(ctx, Cursor) (Result, error)`），导出匹配 `source.Factory` 的 `New`；在 `main.go` 加一行 `srcReg.Register("github_api", githubapi.New)`；配置里加 `type: github_api` 的 feed。新参数走 `feeds[].options` 透传。`domain` / `config` / `filter` / `store` / `notifier` / `watcher` / 模板 / Dockerfile **全部不用改**。

**新增通知渠道**（例如 Matrix）：在 `internal/notifier/webhook/` 新增一个文件实现 `webhook.Adapter`（`Type()` + `Build(Message, Options) (Request, error)` + `Check(status, body) error`），在 `webhook/adapters.go` 的 `Adapters()` 里加一行即可。HTTP 客户端、重试/退避、渲染、日志、密钥展开全在共享引擎里，`main.go` / `config` / `watcher` / 模板 / Dockerfile **全部不用改**；配置里写 `type: matrix` + `options` 即可启用，也能与现有渠道并存。

若新渠道完全不走 HTTP（例如写数据库、发 MQTT），则新建独立包实现 `notifier.Notifier`，并在 `main.go` 加一行 `notifReg.Register("xxx", xxx.New)`——`notify.channels` 会自动把它纳入扇出。

**新增状态存储**（例如 SQLite）：实现 `store.StateStore`（`Load` 用 `store.ErrNotFound` 表示首次运行），注册 + 改 `store.type` 即可。

## 测试

```bash
go test -race -count=1 ./...
go vet ./...
gofmt -l ./cmd ./internal
```

单元测试用 `httptest` 内存网络模拟 feed 与各渠道 webhook（200/204/202/304/429/5xx/畸形 XML/厂商错误码）、临时文件测状态原子写、fake sender 测 Resend 请求、可编程 fake source 覆盖 watcher 的首轮基线 / 重启静默 / 失败重试 / 单 feed 隔离 / 304 语义；另有飞书/钉钉 HMAC 签名向量、环境变量展开、扇出失败策略的回归用例。**测试不会发出真实邮件或真实群消息**。

改 workflow 后建议跑一次静态检查（需自行安装 `go install github.com/rhysd/actionlint/cmd/actionlint@latest`）：

```bash
actionlint .github/workflows/docker-publish.yml
```

## 已知限制

- 单实例设计：多个实例共写同一状态文件会竞态。
- GitHub feed 未认证访问有限流；默认 30 分钟 × 3 个源约 6 次/小时，并启用 ETag/304 与指数退避。
- `From` 必须是 Resend 已验证域名，否则发送会 403。
- `best_effort` 策略下，某个渠道持续失败会丢弃该渠道的后续通知（只留 ERROR 日志），以保证其它渠道不被重复轰炸；要求不漏推送请改用 `strict`。
- 各渠道都有自己的频率上限（企业微信 / 钉钉 20 条/分钟，飞书 100 条/分钟且 5 条/秒，Telegram 同一会话约 1 条/秒，Teams 4 次/秒）。默认 30 分钟轮询 + 一轮一条 digest 远低于上限；把 `mode` 改成 `per_feed` 或缩短间隔时留意。
- 状态文件损坏时程序会报错并跳过该轮，**不会**静默当作首轮重发基线邮件；需人工检查 `data/state.json`。
