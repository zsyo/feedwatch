# 已知限制

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/limitations.md)

<a id="limitations"></a>

- 单实例设计：多个实例共写同一状态文件会竞态；WebUI 变体同理（单个 orchestrator；SQLite 额外固定 `SetMaxOpenConns(1)`，MySQL / PostgreSQL 只是连接池可调，并发实例仍会重复投递）。
- GitHub 未认证访问有限流（REST 60 次/小时、GraphQL 必须 token）；默认 30 分钟 × 3 个源约 6 次/小时，并启用 ETag/304、`poll.concurrency` / `request_spacing` 削峰与指数退避。带 token 时 REST 提升到 5000 次/小时；多个 GitHub 订阅可打开 `github.graphql` 用一次 GraphQL 请求批量查询（每轮全量、无 304，失败自动降级回 REST）。
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
- 一次同步在单个事务里写完整批预设，文件较多时会短暂占住连接（SQLite 只有一条连接，尤其明显）；网络 IO 与文档校验都放在 `Begin()` 之前以缩短占用时间。MySQL 的 DDL 会隐式提交，所以只有首次建表的迁移不走事务，其余写入仍是单事务。
- 管理员把某条订阅条目的 URL 改成与某个远程预设相同、或远程预设之间出现 URL 冲突时，整批同步返回 409 `preset_conflict` 并**整体回滚**（包括本已成功的条目），需先解决冲突再重试。
- 远程预设的 `version` 只允许单调前进：上游误把版本调小会被静默跳过（计入 `skipped`），不会把本地回退；管理员删除过的预设会留下墓碑，同步永不复活，也没有页面入口清除墓碑（需要时直接改 `catalog_preset_state.deleted`）。
- 预设 JSON 与「导入」使用的 `feedwatch-catalog` v1 是两种格式，下载下来的分类文件不能直接丢进导入框；需要离线兜底时请按导入格式整理，或把 `presets.base_url` 指向可访问的镜像。

---

[← 返回主文档](../README.md) · [English](en/limitations.md)
