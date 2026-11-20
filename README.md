# FeedWatch

**简体中文** ｜ [English](README_EN.md)

一个 Go 常驻程序：按配置轮询 `releases.atom`，发现**新的稳定版本**后推送通知。通知渠道支持 Resend 邮件，以及企业微信、飞书、钉钉、Telegram、Slack、Discord、Teams、Bark、ntfy 和通用 Webhook，**可同时推送到多个渠道**。数据源、通知渠道、状态存储都是可插拔接口（支持 Atom/RSS 与 GitHub Releases 两种数据源）。

**两种镜像变体**：不带后缀的 base 镜像保持纯 `config.yaml` 手动配置（无前端资源、无 HTTP 服务、无 SQLite）；`-webui` 变体额外提供多用户 Web 管理界面、登录鉴权与数据库存储（SQLite / MySQL / PostgreSQL），**空库启动、不需要任何配置文件**，首个管理员密码在容器日志里打印一次。两者由同一个 `//go:build webui` 开关隔离，base 二进制不链接任何 WebUI / SQLite 代码，详见 [WebUI 变体](docs/webui.md)。

当前默认监控三个仓库（均已验证可用）：

| 项目 | Atom 源 |
| --- | --- |
| golang/go | `https://github.com/golang/go/releases.atom` |
| rust-lang/rust | `https://github.com/rust-lang/rust/releases.atom` |
| python/cpython | `https://github.com/python/cpython/releases.atom` |

<a id="toc"></a>

## 文档导航

主文档只保留「项目概览 + 核心行为 + 快速上手」；更细的配置、部署、WebUI、发布与开发内容按主题拆到 [`docs/` 子目录](docs/README.md)（[English 目录](docs/en/README.md)），两套件数一一对应。下表中的链接既指向**主文档内的章节**，也指向**子文件及其内部章节**。

**主文档内**

| 章节 | 内容 |
| --- | --- |
| [行为约定](#behavior) | 首轮基线 / 重启静默 / 304 / 失败重试 / 限速等行为语义 |
| [快速开始](#quick-start) | 本地 Windows exe 构建、`config.yaml` 生成、dry-run 调试路径 |
| [CLI 参数与环境变量](#cli) | flag / 环境变量优先级、敏感信息只从环境变量读 |
| [配置速览](#config-overview) | 配置文件分层、新增 feed / 渠道的最小改动 |
| [开源协议](#license) | MIT，可自由使用、修改与商用 |

**详细文档（`docs/`）**

| 文档 | 内容 / 可直达章节 |
| --- | --- |
| [配置详解](docs/configuration.md#configuration) | feeds / filter / GitHub Releases 数据源、版本号提取与基线版本 |
| [通知渠道](docs/notifications.md#notifications) | [内置渠道表](docs/notifications.md#built-in-channels) · [通用 Webhook 模板](docs/notifications.md#webhook-template) · [部分失败策略](docs/notifications.md#partial-failure) |
| [并发与频率控制](docs/rate-limiting.md#rate-limiting) | 三层限速、抓取并发、按渠道类型的投递限速门 |
| [Docker 部署](docs/docker.md#docker) | compose / 命名卷、[bind mount 属主](docs/docker.md#bind-mount) · [rootless uid 映射](docs/docker.md#rootless-docker) |
| [WebUI 变体](docs/webui.md#webui) | [启动](docs/webui.md#webui-start) · [数据库后端](docs/webui.md#database-backends) · [身份与权限](docs/webui.md#roles) |
| [账号、凭据与数据](docs/webui-accounts.md#accounts) | [注册与第三方登录](docs/webui-accounts.md#registration) · [数据、主密钥与备份](docs/webui-accounts.md#secrets-and-backup) |
| [订阅中心与远程预设](docs/webui-catalog.md#catalog) | [远程预设目录](docs/webui-catalog.md#remote-presets) 的同步规则、版本 / 保护 / 墓碑、SSRF |
| [WebUI 开发与升级](docs/webui-development.md#webui-dev) | [通知语义](docs/webui-development.md#notification-semantics) · [页面与本地开发](docs/webui-development.md#pages-and-dev) · [从 1.0 升级](docs/webui-development.md#upgrade) |
| [发布镜像](docs/release.md#release) | [发版](docs/release.md#new-release) · [rc 流程](docs/release.md#rc-flow) · [预构建镜像](docs/release.md#prebuilt-images) · [双架构](docs/release.md#multi-arch) |
| [架构与扩展](docs/architecture.md#architecture) | [目录结构](docs/architecture.md#project-layout) · [如何扩展](docs/architecture.md#extending) |
| [测试](docs/testing.md#testing) | 双 build tag 测试、base / WebUI / 目录 / OAuth / 预设专项用例 |
| [已知限制](docs/limitations.md#limitations) | 单实例、限流、SSRF 残留、WebUI 只读项等边界 |

> 阅读另一语言：[English](README_EN.md)（[英文子文档目录](docs/en/README.md)）。

<a id="behavior"></a>

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
| 目录属主看着没问题却仍 not writable | 多半是 **rootless Docker**：容器 uid 映射到宿主的 `100000+`，与 `id -u` 无关；用命名卷或 `user: "0:0"`，见 [rootless Docker 下的 uid 映射](docs/docker.md#rootless-docker) |
| 订阅很多 feed / 一轮很多通知 | 抓取受 `poll.concurrency` 有界并发 + `poll.request_spacing` 限速；渠道投递受 `notify.send_spacing` 限速（**按渠道类型**，同类型共用一个门），见[并发与频率控制](docs/rate-limiting.md) |

> 去重只认条目的稳定 ID（Atom `<id>`，GitHub release 的数字 id），不是版本号。GitHub 的 feed 只保留最近约 10 条，长时间停机漏掉的中间版本不会补发——这是预期行为；需要更早的记录请用 `type: github`（REST API，可翻页）。

<a id="quick-start"></a>

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

默认模板在 `notify.channels` 里只配了一条 `email`（Resend 邮件）：把该条目下 `email.from`（必须是 Resend 已验证域名）和 `email.to` 改成你自己的值。想推送到企业微信 / 飞书 / Telegram 等，见[通知渠道](docs/notifications.md)——在 `channels` 里替换或追加条目，并把对应的 webhook URL / token 填进环境变量即可。

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

<a id="cli"></a>

## CLI 参数与环境变量

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

<a id="config-overview"></a>

## 配置速览

- `config.example.yaml`：**唯一提交进仓库**的配置模板，带全部字段与注释。
- `config.yaml`：从模板复制出的本地运行配置，含你的域名、收件人与 feed 列表，已被 `.gitignore` 忽略——随便改都不会产生 git diff。`.env`、`config.local.yaml` 同样不提交。
- 密钥永远不写进 YAML，只从环境变量读。
- 新增一个监控项目 = 复制一段 `feeds`、改 `name` / `url`，**零代码改动**（Atom 用完整 URL，GitHub Releases 用 `owner/repo`）。
- 新增一个通知渠道 = 在 `notify.channels` 加一段、配好环境变量，**零代码改动**；可多个并存。

完整字段、过滤规则与 `type: github` 用法见 [配置详解](docs/configuration.md)；渠道参数见 [通知渠道](docs/notifications.md)；限速见 [并发与频率控制](docs/rate-limiting.md)；部署见 [Docker 部署](docs/docker.md)。

<a id="license"></a>

## 开源协议

本项目采用 [MIT 协议](LICENSE)，可自由使用、修改与商用，只需保留版权声明。

---

[回到文档导航](#toc) · [English](README_EN.md)
