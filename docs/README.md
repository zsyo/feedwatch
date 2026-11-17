# FeedWatch 文档目录（中文）

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/README.md)

主文档 [README.md](../README.md) 只保留概览、行为约定与快速上手；以下内容按主题拆分到本目录，英文版逐篇对应在 [`en/`](en/) 子目录下。

| 中文文档 | 主题 | English |
| --- | --- | --- |
| [配置详解](configuration.md) | feeds / filter / GitHub Releases 数据源、版本号提取、基线版本 | [Configuration](en/configuration.md) |
| [通知渠道](notifications.md) | 渠道配置写法、内置渠道表、通用 Webhook 模板、部分失败策略 | [Notification Channels](en/notifications.md) |
| [并发与频率控制](rate-limiting.md) | 轮询周期、抓取并发、按渠道类型的投递限速 | [Concurrency & Rate Limiting](en/rate-limiting.md) |
| [Docker 部署](docker.md) | compose、命名卷 / bind mount 属主、rootless uid 映射 | [Docker Deployment](en/docker.md) |
| [WebUI 变体](webui.md) | 变体对比、启动、数据库后端、身份与权限 | [WebUI Variant](en/webui.md) |
| [账号、凭据与数据](webui-accounts.md) | 注册与第三方登录、主密钥 / 备份 / 目录数据 | [Accounts, Secrets & Data](en/webui-accounts.md) |
| [订阅中心与远程预设](webui-catalog.md) | 远程预设同步规则、版本 / 保护 / 墓碑、SSRF | [Catalog & Remote Presets](en/webui-catalog.md) |
| [WebUI 开发与升级](webui-development.md) | 通知语义、页面与本地开发、schema 升级 | [WebUI Development & Upgrade](en/webui-development.md) |
| [发布镜像](release.md) | GHCR 双组双架构、发版 / rc 流程、预构建镜像 | [Publishing Images](en/release.md) |
| [架构与扩展](architecture.md) | 目录结构、外部依赖、如何扩展数据源 / 渠道 / 存储 | [Architecture & Extending](en/architecture.md) |
| [测试](testing.md) | 双 build tag 测试与专项用例 | [Testing](en/testing.md) |
| [已知限制](limitations.md) | 单实例、限流、SSRF 残留等边界 | [Known Limitations](en/limitations.md) |

---

[← 返回主文档](../README.md) · [English](en/README.md)
