# WebUI 变体

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/webui.md)

<a id="webui"></a>

| | base | webui |
| --- | --- | --- |
| 镜像 tag | 版本号 / `latest` | 版本号加 `-webui`（如 `1.2.3-webui`），移动指针是 `webui` |
| 配置来源 | 每次启动读 `config.yaml` | **不读 `config.yaml`**：空库启动，订阅 / 通知方式 / 密钥全部在页面里配 |
| 界面 / 接口 | 无 | Vue 3 SPA + `/api/*`，默认监听 `:8080` |
| 状态 | `data/state.json`（JSON 原子写） | 关系库（默认 SQLite WAL `/app/data/feedwatch.db` 及 `-wal` / `-shm`，也可换 MySQL / PostgreSQL）+ `secret.key`；库内含订阅目录（`catalog_entries`）、审核提交（`submissions`）、远程预设状态（`catalog_preset_state`）与三方登录绑定（`user_identities` / `auth_providers`）表（schema v8） |
| 用户 | 单用户（配置即全部订阅） | 多用户 + 管理员，订阅与渠道按用户隔离 |
| 密钥来源 | 环境变量（`${ENV_VAR}` 展开） | 页面填写，AES-256-GCM 加密后存库，不可回读 |

> **二选一运行**：不要同时启动 `docker-compose.yml` 与 `docker-compose.webui.yml`。两套各有独立的轮询器与状态，会针对同一批 release 各发一遍通知。

<a id="webui-start"></a>

## 启动

```bash
cp .env.example .env                   # 可选；渠道密钥不在这里配
docker compose -f docker-compose.webui.yml up -d --build
docker logs -f feedwatch-webui         # 首启随机管理员密码只在这里打印一次
```

**不需要 `config.yaml`，也不需要任何渠道密钥变量**：容器首启就是一个干净的空库，只有 `users` 表里那条 `admin` 记录、一套默认设置，以及 8 条示例订阅目录（GitHub 仓库的 `releases.atom`）和 3 个分类。示例只是可浏览的模板，**不会给任何人创建订阅**；老库升级不会自动获得，可在「订阅管理 → 导入导出」加载。浏览器打开 `http://<宿主机>:8080`（端口由 `.env` 的 `WEBUI_HTTP_PORT` 控制），用日志里的 `admin` 密码登录，**首次登录会被强制改密**；改密后所有旧会话立即失效。管理员可在「用户管理」里新建账号或重置密码（新密码只在页面上显示一次）；**自助注册默认关闭**，要开放就在「登录与注册」页打开（见[注册与第三方登录](webui-accounts.md#registration)）。

若经 HTTPS 反向代理访问，把 `.env` 的 `FEEDWATCH_WEBUI_COOKIE_SECURE` 设为 `true`；纯 HTTP 直连时务必保持 `false`，否则会话 cookie 带 `Secure` 标志、浏览器不会回传，导致无法登录。会话有效期由 `FEEDWATCH_WEBUI_SESSION_TTL` 控制（默认 `168h`）。

<a id="database-backends"></a>

## 数据库后端

默认是 SQLite（`data/feedwatch.db`），也可以直接跑在 MySQL 或 PostgreSQL 上。切换只由环境变量决定，**没有配置文件里的数据库段**：

| 变量 | 说明 |
| --- | --- |
| `FEEDWATCH_DB_DRIVER` | `sqlite`（默认）/ `mysql` / `postgres`，也接受 `mariadb` / `postgresql` / `pg` |
| `FEEDWATCH_DB_DSN` | 驱动自己的连接串；sqlite 时就是文件路径。设了它就不再读 `FEEDWATCH_DB_PATH` |
| `FEEDWATCH_DB_PATH` | 只用于 sqlite 的路径，默认 `data/feedwatch.db`（向后兼容，等价于把路径写进 DSN） |
| `FEEDWATCH_DB_MAX_OPEN_CONNS` | 网络后端的连接池上限，默认 10；sqlite 忽略（固定单连接） |
| `FEEDWATCH_DATA_DIR` | 只放 `secret.key` 的目录；sqlite 默认取库文件所在目录，网络后端默认 `data` |

```bash
# MySQL 8.0.16+（InnoDB / utf8mb4）
FEEDWATCH_DB_DRIVER=mysql \
FEEDWATCH_DB_DSN='feedwatch:secret@tcp(127.0.0.1:3306)/feedwatch?charset=utf8mb4' \
FEEDWATCH_DATA_DIR=/var/lib/feedwatch \
./feedwatch-webui

# PostgreSQL 12+
FEEDWATCH_DB_DRIVER=postgres \
FEEDWATCH_DB_DSN='postgres://feedwatch:secret@127.0.0.1:5432/feedwatch?sslmode=disable' \
FEEDWATCH_DATA_DIR=/var/lib/feedwatch \
./feedwatch-webui
```

- **只支持空库起步**，不提供从 SQLite 迁移存量数据的工具。换库等于新库：管理员密码会重新打印一次，渠道凭据要在页面上重填。
- **`secret.key` 与数据库必须同进同退**。网络后端没有库文件，`secret.key` 落在 `FEEDWATCH_DATA_DIR`，务必挂卷并与库一起备份；丢了它，库里所有渠道凭据都解不开，而且程序**不会**重新生成。
- 三种后端写的是同一套逻辑 schema（18 张业务/目录表 + `schema_migrations` + 17 个索引）。MySQL 有两处实现差异：它没有部分索引，三个「只对部分行唯一」的索引改用 STORED 生成列 + 普通唯一索引等价实现；`TEXT` 不能带字面量默认值，所以长文本列写成 `DEFAULT ('')`（这是 8.0.13+ 的硬性要求，加上 `CHECK` 约束实际要 8.0.16+）。`feed_pool.url` 在 MySQL 上不加唯一约束——2048 字符在 utf8mb4 下无法建唯一索引，而它的身份本来就由 `url_key`（sha256）承担。
- 字符串比较口径：MySQL 表用 `utf8mb4_bin`，与 SQLite 的字节序对齐（默认的 `utf8mb4_0900_ai_ci` 会把 `News` / `news` 当成同一个分类）；PostgreSQL 的 `ORDER BY` 跟随库的 collation，但列表排序都在前端算，看不到差异。
- 连接串里的密码不会进日志：启动行只打印 `driver` 和脱敏后的 DSN。
- 迁移版本记在 `schema_migrations` 表里；SQLite 同时继续维护 `PRAGMA user_version`，1.1.x 的库直接沿用，不需要手工干预。
- 仍是**单实例**：连接池可以调大，但后台 orchestrator 只有一个，多实例共写同一个库会产生重复投递。
- 方言差异全部收在 `internal/webui/store`：`dialect.go` 负责占位符重写（PG 的 `$1`）与 insert-ignore / upsert / returning-id 生成，三份 DDL 分别是 `schema_sqlite.sql` / `schema_mysql.sql` / `schema_postgres.sql`，`TestSchemaFilesAgree` 钉住三者的表与列一致。**新增查询照常写 `?` 占位符与普通 SQL 即可**，只有 `ON CONFLICT ... SET <字面量>` 这类写法才需要按方言分支。
- 想连真实服务器验证（本机没有实例时）：`FEEDWATCH_TEST_MYSQL_DSN=... FEEDWATCH_TEST_POSTGRES_DSN=... go test -tags webui -run TestNetworkBackend ./internal/webui/store/`。它会建表、跑一遍 upsert / 唯一冲突 / 偏唯一索引 / 中文往返，最后 DROP 所有表，**只能指向一次性测试库**。

<a id="roles"></a>

## 身份与权限

| | 管理员（`admin`） | 普通用户 |
| --- | --- | --- |
| 订阅 / 通知方式 | **看不到入口**，直接调接口返回 403 `role_not_allowed` | 增删改自己的订阅与渠道 |
| 订阅中心 / 审核中 | **看不到入口**，直接调接口返回 403 `role_not_allowed` | 浏览目录、一键订阅 / 取消、提交与撤回申请、查看全部待审链接 |
| 订阅管理（侧边栏子菜单） | 五个子页：订阅条目（增删改停 / 预设状态 / 解除保护）、审核队列、分类管理、导入导出、**远程预设同步** | 403 `forbidden` |
| 渠道密钥 | 只能看到「已配置 / 缺失」与脱敏后的主机名 | 只能看到自己填过哪些字段，值永远不回显 |
| 他人数据 | 只读下钻：订阅、渠道、投递记录 | 无 |
| 他人资源启停 | 可停用 / 恢复某个订阅或渠道，**不能改内容** | 无 |
| 用户管理 | 新建用户、重置密码、停用账号 | 只能改自己的密码 |
| 自助注册 / 三方登录 | 只能用密码登录；无绑定入口（403 `role_not_allowed`） | 可自助注册、用 GitHub / linux.do 登录并绑定 |
| 登录与注册配置 | 「登录与注册」页：注册开关 + 两个平台的 Client ID / Secret | 看不到 |

**想给自己配订阅，就新建一个普通用户**：管理员刻意被挡在业务配置之外，运维账号与订阅账号不会混在一起，管理员也无法借权限读到别人的 webhook / token。下钻页面的写操作只接受 `{"enabled": bool}`，多传一个字段即 400 `admin_readonly`。

订阅中心的「更新信息要审核」只针对**普通用户**：管理员是可信角色，在「订阅管理 → 订阅条目」里添加或编辑条目立即生效、不产生待审申请；用户提交的新增 / 更新申请则必须由管理员在「审核队列」子页通过或驳回后才写入目录。「审核中」页对全体普通用户开放，但**只能查看和手动复制**，不能直接订阅。

---

[← 返回主文档](../README.md) · [English](en/webui.md)
