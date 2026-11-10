# FeedWatch

一个 Go 常驻程序：按配置轮询 `releases.atom`，发现**新的稳定版本**后通过 Resend 发邮件通知。前期只接 Atom 源、只发邮件，但数据源、通知渠道、状态存储都是可插拔接口。

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
| 邮件发送失败 | **不提交状态**，新 ID 保持未见，下一轮自动重试（at-least-once） |
| 某个 feed 抓取失败 | 跳过该 feed，其它 feed 正常通知；首轮若有任一 feed 失败则整封基线邮件推迟 |
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

再把里面的 `notify.email.from`（必须是 Resend 已验证域名）和 `to` 改成你自己的值。

```powershell
# ① 最安全：预览基线邮件，零网络发送、不碰正式状态文件
.\feedwatch.exe -config config.yaml -once -dry-run
.\feedwatch.exe -config config.yaml -once -dry-run   # 第二次：应完全静默（验证重启语义）

# ② 全链路本地调试：把 notify.type 改成 log，也不发真实邮件
#    （复制 config.yaml 为 config.local.yaml，改 notify.type: log、store.path: data/state.local.json）
.\feedwatch.exe -config config.local.yaml -once

# ③ 真实发信
$env:RESEND_API_KEY="re_xxx"
.\feedwatch.exe -config config.yaml -once    # 发一封基线邮件
.\feedwatch.exe -config config.yaml          # 常驻轮询
```

`-dry-run` 会把通知器换成日志通知器，并把状态文件改成 `data/state.dryrun.json`（独立文件），所以既能反复预览基线 / 静默 / 新版本三条路径，又不会污染正式状态、不会误抑制真实基线邮件。

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

敏感信息只从环境变量读：`RESEND_API_KEY`（变量名可由 `notify.email.api_key_env` 改）。见 `.env.example`。**不要把真实密钥写进 YAML**。

## 配置

- `config.example.yaml`：**唯一提交进仓库**的配置模板，带全部字段与注释。
- `config.yaml`：从模板复制出的本地运行配置，含你的域名、收件人与 feed 列表，已被 `.gitignore` 忽略——随便改都不会产生 git diff。`.env`、`config.local.yaml` 同样不提交。
- 密钥永远不写进 YAML，只从环境变量读。
- 新增一个监控项目 = 复制一段 `feeds`、改 `name` / `url`，**零代码改动**。

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

## Docker 部署（WSL 里执行）

```bash
cp config.example.yaml config.yaml   # 本地私有配置，会被只读挂载进容器
cp .env.example .env                 # 填入真实 RESEND_API_KEY
docker compose build
docker compose up -d
docker compose logs -f    # 首次应看到基线邮件已发送
docker compose restart    # 无新版本时应完全静默
```

镜像为多阶段构建（`golang:1.27-alpine` → `alpine:3.21`），静态二进制、非 root 用户、含 CA 证书与时区。配置通过只读挂载传入，状态通过命名卷 `feedwatch-data:/app/data` 持久化。

> 镜像内烘焙的是 `config.example.yaml` 的**占位默认值**（不含任何真实域名或收件人），因此干净 clone 也能直接 `docker compose build`；`.dockerignore` 会挡住本地 `config.yaml`，防止私有配置进入镜像层。不带挂载直接 `docker run` 时用的就是这份占位配置，需自行 `-v` 挂真实配置。

> **卷丢了会重发基线邮件**（因为程序会认为这是首次运行）。这是预期行为。若手工 `docker run`，记得 `-e RESEND_API_KEY=... -v feedwatch-data:/app/data`。

## 目录结构

```
cmd/feedwatch/main.go          唯一组合根：注册实现、注入配置、启动调度
internal/domain/              领域模型（零内部依赖）
internal/config/              YAML + env 覆盖 + 校验
internal/source/              Source 接口 / Registry
internal/source/atom/         Atom/RSS 解析、条件请求、重试
internal/filter/              稳定版过滤规则
internal/notifier/            Notifier 接口 / Registry
internal/notifier/template/   go:embed 邮件模板（基线 / 新版本共用）
internal/notifier/email/      Resend 实现
internal/notifier/log/        日志通知器（本地调试 / dry-run）
internal/store/               StateStore 接口 / Registry
internal/store/jsonfile/      JSON 原子写状态
internal/watcher/             核心编排：抓取 → 过滤 → diff → 通知 → 提交
testdata/                     测试用 feed 夹具
```

依赖只有两个外部模块：`go.yaml.in/yaml/v4`、`github.com/resend/resend-go/v4`。Atom 解析用标准库 `encoding/xml`。

> YAML 依赖说明：原 `gopkg.in/yaml.v3`（`go-yaml/yaml`）已于 2025 年 4 月被作者标记为 unmaintained；官方 YAML 组织接管的正统继任者是 `go.yaml.in/yaml/*`，其中 v3 为冻结 API（仅安全修复），**v4 是当前活跃维护的稳定版**，故本项目直接使用 v4。`github.com/goccy/go-yaml` 是另一个从零重写的独立实现，与官方继任者无关，本项目未采用。配置解析仍用经典 `Unmarshal` API 以保持行为稳定；两个自定义 `UnmarshalYAML(*yaml.Node)` 钩子的 `Node` 必须来自同一模块，否则 v4 会报迁移错误。

## 如何扩展

**新增数据源**（例如 GitHub REST API）：新建 `internal/source/githubapi/`，实现 `source.Source`（`Type()` + `Fetch(ctx, Cursor) (Result, error)`），导出匹配 `source.Factory` 的 `New`；在 `main.go` 加一行 `srcReg.Register("github_api", githubapi.New)`；配置里加 `type: github_api` 的 feed。新参数走 `feeds[].options` 透传。`domain` / `config` / `filter` / `store` / `notifier` / `watcher` / 模板 / Dockerfile **全部不用改**。

**新增通知渠道**（例如 Slack）：新建 `internal/notifier/slack/`，实现 `notifier.Notifier`（把中性的 `domain.Notification` 渲染成该渠道格式），在 `main.go` 加一行 `Register("slack", slack.New)`，把 `notify.type` 指过去。`source` / `filter` / `store` / `watcher` 与现有邮件实现全部不用动。

**新增状态存储**（例如 SQLite）：实现 `store.StateStore`（`Load` 用 `store.ErrNotFound` 表示首次运行），注册 + 改 `store.type` 即可。

## 测试

```bash
go test -race -count=1 ./...
go vet ./...
gofmt -l ./cmd ./internal
```

单元测试用 `httptest` 模拟 feed（200/304/429/5xx/畸形 XML）、临时文件测状态原子写、fake sender 测 Resend 请求、可编程 fake source 覆盖 watcher 的首轮基线 / 重启静默 / 失败重试 / 单 feed 隔离 / 304 语义。**测试不会发出真实邮件**。

## 已知限制

- 单实例设计：多个实例共写同一状态文件会竞态。
- GitHub feed 未认证访问有限流；默认 30 分钟 × 3 个源约 6 次/小时，并启用 ETag/304 与指数退避。
- `From` 必须是 Resend 已验证域名，否则发送会 403。
- 状态文件损坏时程序会报错并跳过该轮，**不会**静默当作首轮重发基线邮件；需人工检查 `data/state.json`。
