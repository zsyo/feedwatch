# 架构与扩展

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/architecture.md)

<a id="architecture"></a>

<a id="project-layout"></a>

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
internal/source/github/       GitHub Releases REST 抓取 + 可选 GraphQL 批量预取
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
internal/webui/store/         连接与方言层（SQLite / MySQL / PostgreSQL）、三份 DDL、迁移版本表、WAL 与目录可写性预检（schema v8）
internal/webui/auth/          bcrypt 密码、session、CSRF
internal/webui/oauth/         GitHub / linux.do 的授权码流程（authorize URL、换 token、取身份、state 与 PKCE 生成；不碰数据库）
internal/webui/presets/       远程预设的 JSON 校验、加载器与 SSRF 防护（纯函数 + 网络，不碰数据库）
internal/webui/api/           HTTP server、中间件链、/api/* 端点与 SPA 静态服务；catalog.go / catalog_admin.go / catalog_presets.go 是订阅中心与远程同步，auth_register.go / auth_oauth.go 是自助注册与三方登录
internal/webui/orchestrator/  多租户轮询：URL 去重池、每用户策略分流、独立投递与按渠道类型限速
internal/webui/embedded/      只提交 stub.html / .gitkeep，真实产物由构建生成且 gitignore
web/                         Vue 3 + Vite + TS 源码；CatalogView / CatalogPendingView、5 个 AdminCatalog* 子页 View、RegisterView / AdminAuthView、catalogAdmin.ts / presets.ts / authMeta.ts 与 build.sh / build.ps1
presets/                     远程预设数据：manifest.json + 4 个分类 JSON（运行时远程读取，不进镜像）
testdata/                     测试用 feed 夹具
```

base 二进制只链接两个外部模块：`go.yaml.in/yaml/v4`、`github.com/resend/resend-go/v4`；`-tags webui` 变体额外链接 `modernc.org/sqlite`、`github.com/go-sql-driver/mysql`、`github.com/jackc/pgx/v5`（三者都是纯 Go、无需 cgo）与 `golang.org/x/crypto/bcrypt`。`go.mod` 会同时列出这些依赖，但链接器把 WebUI 依赖从 base 二进制中剔除。Atom 解析用标准库 `encoding/xml`。

> YAML 依赖说明：原 `gopkg.in/yaml.v3`（`go-yaml/yaml`）已于 2025 年 4 月被作者标记为 unmaintained；官方 YAML 组织接管的正统继任者是 `go.yaml.in/yaml/*`，其中 v3 为冻结 API（仅安全修复），**v4 是当前活跃维护的稳定版**，故本项目直接使用 v4。`github.com/goccy/go-yaml` 是另一个从零重写的独立实现，与官方继任者无关，本项目未采用。配置解析仍用经典 `Unmarshal` API 以保持行为稳定；两个自定义 `UnmarshalYAML(*yaml.Node)` 钩子的 `Node` 必须来自同一模块，否则 v4 会报迁移错误。

<a id="extending"></a>

## 如何扩展

**新增数据源**：新建 `internal/source/<name>/`，实现 `source.Source`（`Type()` + `Fetch(ctx, Cursor) (Result, error)`），导出匹配 `source.Factory` 的 `New`；在 `main.go` 与 `webui_hook_webui.go` 各加一行 `srcReg.Register(...)`；配置 / 页面里加对应的 `type`。新参数走 `feeds[].options` 透传。`domain` / `config` / `filter` / `store` / `notifier` / `watcher` / 模板 / 两个 Dockerfile **全部不用改**（两种构建复用同一批叶子包）。现成的 `internal/source/github/` 是一个完整样例：REST 主路径 + 可选 GraphQL 批量（通过 context 注入每轮缓存，失败逐仓库降级回 REST）。

**新增通知渠道**（例如 Matrix）：在 `internal/notifier/webhook/` 新增一个文件实现 `webhook.Adapter`（`Type()` + `Build(Message, Options) (Request, error)` + `Check(status, body) error`），在 `webhook/adapters.go` 的 `Adapters()` 里加一行即可。HTTP 客户端、重试/退避、渲染、日志、密钥展开全在共享引擎里，`main.go` / `config` / `watcher` / 模板 / 两个 Dockerfile（base / webui）**全部不用改**（两种构建复用同一批叶子包）；配置里写 `type: matrix` + `options` 即可启用，也能与现有渠道并存。

若新渠道完全不走 HTTP（例如写数据库、发 MQTT），则新建独立包实现 `notifier.Notifier`，并在 `main.go` 加一行 `notifReg.Register("xxx", xxx.New)`——`notify.channels` 会自动把它纳入扇出。

**新增状态存储**（例如 SQLite）：实现 `store.StateStore`（`Load` 用 `store.ErrNotFound` 表示首次运行），注册 + 改 `store.type` 即可。注意 WebUI 变体的 SQLite 是独立的多租户存储（`internal/webui/store/`），不走这个接口。

---

[← 返回主文档](../README.md) · [English](en/architecture.md)
