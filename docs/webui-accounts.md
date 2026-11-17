# 账号、凭据与数据

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/webui-accounts.md)

<a id="accounts"></a>

<a id="registration"></a>

## 注册与第三方登录

两条入口，都默认关闭，由管理员在「登录与注册」页打开：

| 入口 | 打开后的行为 |
| --- | --- |
| 自助注册 | 登录页出现「注册一个」；访客自填用户名 + 密码（≥8 位）即建普通账号并**直接登录**，不经过管理员审批 |
| GitHub / linux.do 登录 | 登录页出现「使用 X 登录」。首次用某个第三方账号登录时**自动建号并绑定**，之后再点就是登录；已登录的用户在「账号设置 → 第三方登录」里可以绑定或解绑 |

- **关闭自助注册会同时关掉第三方自动建号**：两个入口都会写 `users`，共用 `webui.allow_registration` 一个开关。关闭后，第三方账号若从未绑定过本站账号，会回到登录页并提示「未开放自助注册」。
- **凭据存库、加密封存**：`auth_providers` 一行一个平台，`client_secret` 用主密钥做 `fwsec:v1:` 信封（AAD 绑定平台名），任何接口都不回显，保存后只显示「已保存，留空表示不修改」。换主密钥只会让这两个 Secret 失效，重新填一次即可，不影响已绑定的用户。
- **回调地址由请求推导，必须与平台后台登记的一致**：`{当前访问地址}/api/auth/oauth/{github|linuxdo}/callback`，管理页直接显示并可一键复制。经反向代理时要求代理传递 `X-Forwarded-Proto` 与 `X-Forwarded-Host`，否则会算成 `http://` 或内网地址。GitHub 走 OAuth App（`Settings → Developer settings → OAuth Apps`），scope 只要 `read:user user:email`；linux.do 在 `connect.linux.do` 的「我的应用接入」申请，scope 是 `user`。
- **PKCE 只在支持它的平台上用**：GitHub 发 `code_challenge`（S256）与 `code_verifier`；linux.do Connect 的文档没有 PKCE，因此只用 `state` 防伪。
- **`state` 是库里的行**（`oauth_flows`，只存 sha256），一次性、10 分钟过期、回调时先删后校验，重放直接失败；「绑定」流程的目标用户在**发起时**就写进这一行，回调 URL 里没有任何用户标识，改参数不能把别人的第三方账号绑到自己身上。
- **管理员账号完全不参与**：`GET /api/me/identities` 与绑定入口都是 `accessMember`，管理员拿到 403 `role_not_allowed`；即使库里有指向管理员的绑定，回调也会以 `admin_not_allowed` 拒绝登录。管理员只能用用户名 + 密码。
- 第三方建号拿不到密码，所以「修改密码」页对它显示「设置登录密码」且**不要求旧密码**（没有可验证的东西，凭据是当前会话）。账号设置里也**不允许解绑最后一个登录方式**：没有密码时解绑唯一的第三方账号会被拒绝（400 `last_login_method`）。
- 登录名从第三方用户名派生（只保留 `[a-zA-Z0-9._-]`，最多 24 字符，最少 2 个），被占用就依次加 `-2`…`-10`；中文用户名会退化成 `github-user` / `linuxdo-user` 这类可用名。第三方邮箱仅作展示（linux.do 不返回邮箱）。
- 两个公开写入端点按 IP 限流：登录 10 次/分、注册 10 次/分、OAuth start/callback 20 次/分。

<a id="secrets-and-backup"></a>

## 数据、主密钥与备份

- 数据库与主密钥都在命名卷 `feedwatch-webui-data:/app/data`。SQLite WAL 需要**目录**可写，启动期有和 base 一样的 `CreateTemp` 预检；把卷换成 bind mount 时同样用 `.env` 里的 `FW_UID` / `FW_GID` 指定宿主机属主——**rootless Docker 例外**，此时容器的 uid 不是宿主 uid，要么保留命名卷，要么写 `user: "0:0"`，见 [rootless Docker 下的 uid 映射](docker.md#rootless-docker)。
- **`secret.key` 与数据库必须一起备份、一起丢弃**。首启用 `O_CREATE|O_EXCL` 生成 32 字节（0600），做渠道密钥的信封加密：`fwsec:v1:` + `base64.RawURLEncoding(nonce ‖ ciphertext ‖ tag)`，AAD 绑定渠道 id，把密文复制到别的渠道行也解不开。读失败**绝不重新生成**（那会让已存凭据全部变成无法解密的垃圾），而是启动即报错并给出三条补救路径；长度或 base64 不合法同样直接拒绝。想自己托管就设 `FEEDWATCH_WEBUI_SECRET_KEY`（标准 base64 的 32 字节），它覆盖密钥文件，**设过之后不要再换**。
- 新用户新增订阅从当前全局 seen 集合建立基线，**不会补发历史版本**；删除后重新订阅也一样。
- **订阅中心里的目录只是模板**：`catalog_categories` / `catalog_entries` / `submissions` 三张表，`category_id IS NULL` 表示「未分类」。一键订阅把目录里的 URL 复制成用户自己的一份订阅，之后编辑、停用、删除目录**都不会影响已订阅用户**。停用只是从订阅中心隐藏，仍占用 `url_key`（全局唯一索引含停用行），要释放 URL 必须删除条目。
- 用户提交的新增 / 更新申请先进 `submissions`（`pending`），**只有管理员通过才写 `catalog_entries`**；驳回必须填理由，撤回保留 `withdrawn` 历史。更新申请在提交时用 `before_json` 存下当时的原值快照，审核页与「我的提交」直接展示改前 / 改后对比。`submissions` 行永不物理删除，带 `reviewed_by` / `reviewed_at` / `reject_reason`。
- 目录 URL 会**展示给所有普通用户并允许复制**，因此入口校验比普通订阅更严：只允许 http/https，拒绝 userinfo 与含 `token` / `secret` / `key` / `password` / `auth` / `sig` 等敏感 query 键，长度上限 2048；对外响应还会再脱敏一次。已驳回的 URL 可以重新提交（唯一索引只对 `pending` 生效），每人最多 10 条待审申请。
- 「订阅管理 → 导入导出」只搬运目录元数据（分类按名称匹配，不含 id），冲突策略有 `skip`（默认）/ `overwrite` / `error`（遇重复整体回滚）；导入**只写目录表**，绝不写 `subscriptions` / `feed_pool` / `user_seen`，也不含渠道凭据与投递状态。删除分类时该分类下的条目与申请统一改为「未分类」（外键 `ON DELETE SET NULL`，同一事务完成）。
- 「全局设置」页可改轮询间隔、超时、并发、抓取/发送间隔、HTTP 参数等；服务端逐项校验后写库，**重启进程后生效**（不热更新）。
- `webui.cookie_secure` / `webui.session_ttl` 是环境变量控制的只读项；`notify.mode` 在 WebUI 里不再有全局行，它是**账号级**偏好（`users.notify_mode`），在「通知方式」页顶部设置，对所有订阅与所有渠道一起生效。
- 渠道表单由服务端下发：`GET /api/channel-fields` 返回每种渠道的字段表（键名、必填、类型、选项、是否密钥），前端按 schema 渲染。新增一种渠道只需改 `internal/webui/channelspec.go` 的 `ChannelFields`，页面不用动。
- **显示时区跟着账号走**：默认「跟随服务器」（容器的 `TZ`，compose 默认 `Asia/Shanghai`），也可在「账号设置」里选任意 IANA 时区，存在 `users.timezone`，换设备登录自动同步。它只影响页面渲染：数据库里所有时间戳仍是 RFC3339 UTC，改时区不会重写任何数据。需要自己指定默认值就改 `.env` 的 `TZ`。
- 页面里的时间统一渲染成 `YYYY-MM-DD HH:mm:ss`（不带 `T` / `Z`），按上面选中的时区换算；`GET /api/meta` 同时给出 `server_timezone` 与 `server_utc_offset`，未登录页面也能标出服务器默认值。WebUI 变体额外 `import _ "time/tzdata"`，因此精简镜像里没有 tzdata 也能解析 IANA 名称。

---

[← 返回主文档](../README.md) · [English](en/webui-accounts.md)
