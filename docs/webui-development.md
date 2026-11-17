# WebUI 开发与升级

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/webui-development.md)

<a id="webui-dev"></a>

<a id="notification-semantics"></a>

## 通知语义

- 同一 URL 全局每轮只抓一次（`url_key = sha256(规范化 URL)`），抓完按每个用户各自的过滤条件、`always` / `on_update` 分流。`digest` / `per_feed` 由账号决定而不是订阅：同一个用户的所有订阅共用一个形式，一轮里不会同时产出「汇总一条」和「每个项目一条」。
- 订阅中心的「一键订阅」走和手动新增**完全相同**的三写事务（`subscriptions` + `feed_pool` + 复制当前 `user_seen` 基线），同样从当前基线开始、不会补发历史版本；因此它是「复制模板」，不是引用模板。目录显示「已订阅」按 `url_key` 判定，用户在「我的订阅」里改了 URL 之后，该目录卡片会重新显示为未订阅。
- **不做跨用户合并**：每个用户 × 每个渠道一次独立请求，使用各自的凭据，这样每个用户都能在 Resend 等后台看到自己的发送记录。限速按**渠道类型**分别计算（email / telegram / wecom …各一个独立门），不同服务互不阻塞。
- 渠道密钥**加密落库，运行期不读任何环境变量**。页面提交的是字段值本身，服务端 `Seal` 后写入 `channel_secrets`；列表与下钻接口只返回 `secret_fields`（哪些字段已配）、`secret_hints`（仅 `scheme://host`）和 `secret_state`（`ok` / `missing` / `error` / `none`），任何明文或 `fwsec:` 信封都不会出现在响应与日志里。编辑时**留空 = 保持不变**，要清除就显式传 `clear_secrets`；把密钥塞进普通字段会被 400 `secret_in_options` 拒绝。
- 通知内容在抓取成功后就物化入库，投递失败只重试已渲染的投递组，不重新抓 feed、也不阻塞全局已见集合。

<a id="pages-and-dev"></a>

## 页面与本地开发

页面：`/login`、概览、**订阅中心 `/catalog`**（侧边栏排在「我的订阅」之前）、我的订阅、通知方式、账号设置；**审核中 `/catalog/pending`** 是订阅中心内的子页，从订阅中心页头的「审核中的链接」进入、可返回，不再单列在侧边栏；停留在该子页时侧边栏的「订阅中心」仍保持高亮。管理员另有用户管理（含单个用户下钻 `/admin/users/:id`，只读 + 启停）、**订阅管理**、轮询池、限速状态、全局设置、审计日志。订阅管理在侧边栏是一个**上下文分组**：只有停在 `/admin/catalog*` 时才展开 5 个缩进子项，其它管理页仍是 8 项短侧边栏；父项「订阅管理」只作分组入口（不会被当作当前页高亮），当前子项单独高亮。旧地址 `/admin/catalog` 保留为兼容重定向到「订阅条目」，旧书签和概览页快捷入口都继续可用。普通用户看不到管理端入口，管理员看不到订阅 / 渠道 / 订阅中心的入口，后端角色守卫双向拦截。

订阅中心支持关键字（名称 / URL / 简介）、分类筛选与按订阅人数 / 收录时间 / 名称排序（分类检索交给旁边的分类下拉，关键字里不再匹配分类名），卡片直接显示「已订阅」与取消入口；页面下方是「提交新订阅」和「我的提交」（状态、驳回理由、改前 / 改后对比、撤回）。审核中子页是只读的，只提供「复制信息」并常驻风险提示。订阅管理拆成 5 个专注子页，每页只放一到两张卡片：**订阅条目 `/admin/catalog/entries`**（手动添加 + 条目表，含编辑 / 停用 / 删除 / 预设状态 / 解除保护，以及按分类筛选与按 ID / 订阅人数升 / 降序排序）、**审核队列 `/admin/catalog/reviews`**（待审与全部历史、改前 / 改后对比、通过 / 驳回理由）、**分类管理 `/admin/catalog/categories`**（添加 / 重命名 / 删除）、**导入导出 `/admin/catalog/transfer`**（下载 JSON、冲突策略、导入结果）、**远程预设 `/admin/catalog/presets`**（同步按钮、逐文件结果、就近的同步规则说明）。子页共享 `web/src/catalogAdmin.ts` 里的响应类型、请求封装、提示状态与子菜单定义，但各自持有独立的错误 / 成功提示，不会把上一页的消息带过来。订阅条目页默认按 ID 升序——后端实际返回的是名称序，所以此前 ID 列看起来是跳的；可切到「订阅人数」并切换升 / 降序，过滤后标题会显示「命中 / 总数」。筛选与排序都是**前端本地**计算（一次返回全量目录），与现有列表页范式一致。

```bash
make build-webui                 # 或 sh web/build.sh，产物写入 internal/webui/embedded/（已 gitignore）
go build -tags webui -o feedwatch-webui.exe ./cmd/feedwatch
FEEDWATCH_DB_PATH=data/feedwatch.db FEEDWATCH_HTTP_ADDR=127.0.0.1:8080 ./feedwatch-webui.exe
FEEDWATCH_DB_DRIVER=postgres FEEDWATCH_DB_DSN='postgres://fw:fw@127.0.0.1:5432/fw?sslmode=disable' ./feedwatch-webui.exe
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

<a id="upgrade"></a>

## 从 1.0 及更早的 WebUI 升级

1.1.0 起不再读 `config.yaml`，渠道密钥改为页面填写 + 加密落库。旧库能直接打开（`PRAGMA user_version` 从 1 迁到 2，只新增 `channel_secrets` 等表，不动旧数据），但**旧渠道里存的还是环境变量名 / 占位符，无法自动转成密文**，需要在「通知方式」页把凭据重填一次，否则 `secret_state` 会显示 `missing`、投递报缺少凭据。

迁移是累加式的，每一步只补表 / 补列 / 改列名，不重建已有表、不动业务数据（v7 起版本号记在 `schema_migrations`，SQLite 同时维护 `PRAGMA user_version` 以兼容旧库）：

| schema 版本 | 变化 |
| --- | --- |
| v2 | `channel_secrets` 等渠道密钥表 |
| v3 | `users.timezone`（账号显示时区） |
| v4 | `users.notify_mode`（通知形式改为账号级）；老库残留的 `subscriptions.mode` 列不读不写 |
| v5 | `catalog_categories` / `catalog_entries` / `submissions`（订阅中心） |
| v6 | `catalog_preset_state` 表 + `catalog_entries` 的 `preset_key` / `preset_version` / `preset_edited` 三列与偏唯一索引（远程预设） |
| v7 | 支持 MySQL / PostgreSQL：`settings.key` → `setting_key`、`notifications.text` → `body`（这两个名字在 MySQL 里分别是保留字与类型名），版本号改记在 `schema_migrations` 表 |
| v8 | 三方登录：`user_identities`（每平台一条绑定）、`auth_providers`（凭据，Secret 加密存）、`oauth_flows`（一次性 state，只存哈希） |

v8 **只新增三张表**，不动 `users`（第三方建号的账号 `password_hash` 存空串，密码登录因此天然失败），老库升级后只是多出三张空表，注册与两个平台都还是关闭状态。

v7 只做两处列改名（SQLite 的 `ALTER TABLE ... RENAME COLUMN`，存量数据原样保留），不改任何业务语义；改名原因是 `key` 在 MySQL 是保留字、`text` 是类型名，留着就必须按方言写不同的引号。

v5 **只新增三张表**，老库升级后不会自动获得示例目录（种子只在 `users` 为空时执行）；想预置示例，可在「订阅管理 → 导入导出」加载一份从新库导出的 JSON。v6 通过 `ensureColumn()` 给老库的 `catalog_entries` 补三列（默认 `''` / `0` / `0`），并在补列之后才建偏唯一索引 `WHERE preset_key <> ''`（存量行都是空串，不受影响）；**不会重建表、不改动业务数据**。升级后第一次点「获取远程预设数据」，会按 URL 认领已有的示例 / 手工 / 导入条目并按版本升级，不会生成重复行。其余功能升级后直接可用，无需手工改数据。备份时数据库与 `secret.key` 一起带上即可，目录与预设状态都在同一个库里。

不想保留旧数据就干脆删卷重来（`docker compose -f docker-compose.webui.yml down -v`），首启会重新生成管理员密码与新密钥。**`data/` 卷与 `secret.key` 必须同进同退**，只留其一没有任何意义。

---

[← 返回主文档](../README.md) · [English](en/webui-development.md)
