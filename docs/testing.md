# 测试

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/testing.md)

<a id="testing"></a>

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

订阅中心专项用例：v4→v5 迁移只补三张表且保留用户数据、外键 `ON DELETE SET NULL`、pending 部分唯一索引（驳回 / 撤回后可重提）、示例目录只是模板（不写 `subscriptions` / `feed_pool` / `user_seen`）、目录 URL 的协议 / userinfo / 敏感 query / 长度与别名归一化校验、admin 与 member 的双向角色守卫、**一键订阅复制基线不重放历史**、去重防刷与待审上限、新增与更新两类审核流（含 `before_json` 快照、`entry_gone` / `conflict_url`）、撤回、驳回理由可见、分类删除级联、管理员手动编辑不产生申请、导入导出往返与三种冲突策略。注册与三方登录专项用例：注册开关关闭时 403、打开后建号即登录（`must_change_password=0`、`password_set=true`）、重名 409、弱密码与非法用户名 400；OAuth 全流程用本地假 provider 跑通——未配置时 start 只回 `not_configured`、注册关闭时陌生账号被拒且**不写 users**、打开后建号且二次登录复用同一账号、state 重放被判失效、绑定后可用第三方登录进入同一账号、解绑最后一个登录方式被拒、设置密码后可以解绑、管理员调 `/api/me/identities` 与 `mode=bind` 都是 `admin_not_allowed`；管理端断言 Client Secret 保存后不回显、空值保存不覆盖、清除凭据同时停用、凭据不全时禁止启用。`internal/webui/oauth` 另有 authorize URL 参数（含 PKCE 只在 GitHub 出现）、token 错误分支与三种失败响应、身份缺 id 的用例。

改前端后还要在 `web/` 目录下跑 `npm run typecheck`，并重新执行构建脚本再 `go build -tags webui`。

远程预设专项用例：仓库内 `presets/*.json` 直接用生产校验器验一遍（防止拼错 key / 重复 URL 混入发版数据）、manifest 与分类文件的格式/文件名/分类漂移、跨文件 `preset_key` 与 `url_key` 去重、SSRF 的协议 / 端口 / userinfo / 私网与云元数据 IP / DNS 解析复检 / 重定向逐跳复检 / 跳数上限 / 1 MiB 体积上限 / 上下文取消、加载器的逐文件状态与「失败文件之后不再写」、v5→v6 补列与偏唯一索引；接口层覆盖只升不降、连续多版本升级后 `preset_edited` 仍为 0、同版本重复同步、存量条目区分认领、管理员编辑与审核通过置保护、`accept-remote` 解锁、墓碑不复活、拉取失败与 `preset_invalid_document` 零写入、事务内 `url_key` 冲突时**把先前的更新一并回滚**、缺少 loader 的报错、目录列表暴露三个预设状态字段。全部用 `httptest` 本地服务器，不发真实网络请求。

改 workflow 后建议跑一次静态检查（需自行安装 `go install github.com/rhysd/actionlint/cmd/actionlint@latest`）：

```bash
actionlint .github/workflows/docker-publish.yml
```

---

[← 返回主文档](../README.md) · [English](en/testing.md)
