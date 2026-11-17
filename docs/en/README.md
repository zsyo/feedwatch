# FeedWatch Documentation Index (English)

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../README.md)

The main [README_EN.md](../../README_EN.md) keeps only the overview, behavior and quick start; everything below is split by topic into this directory, with a one-to-one Chinese counterpart in [`../`](../README.md).

| English document | Topic | 简体中文 |
| --- | --- | --- |
| [Configuration](configuration.md) | feeds / filters / GitHub Releases source, version token extraction, baseline version | [配置详解](../configuration.md) |
| [Notification Channels](notifications.md) | channel config, built-in channel table, generic Webhook template, partial failure policy | [通知渠道](../notifications.md) |
| [Concurrency & Rate Limiting](rate-limiting.md) | polling period, bounded fetch concurrency, per-channel-type delivery pacing | [并发与频率控制](../rate-limiting.md) |
| [Docker Deployment](docker.md) | compose, named volume / bind mount ownership, rootless uid mapping | [Docker 部署](../docker.md) |
| [WebUI Variant](webui.md) | variant comparison, startup, database backends, roles & permissions | [WebUI 变体](../webui.md) |
| [Accounts, Secrets & Data](webui-accounts.md) | registration & third-party login, master key / backup / catalog data | [账号、凭据与数据](../webui-accounts.md) |
| [Catalog & Remote Presets](webui-catalog.md) | remote preset sync rules, version / protection / tombstone, SSRF | [订阅中心与远程预设](../webui-catalog.md) |
| [WebUI Development & Upgrade](webui-development.md) | notification semantics, pages & local development, schema upgrades | [WebUI 开发与升级](../webui-development.md) |
| [Publishing Images](release.md) | GHCR dual-group dual-arch, release / rc flow, prebuilt images | [发布镜像](../release.md) |
| [Architecture & Extending](architecture.md) | project layout, external dependencies, extending sources / channels / stores | [架构与扩展](../architecture.md) |
| [Testing](testing.md) | dual build-tag tests and topic-specific suites | [测试](../testing.md) |
| [Known Limitations](limitations.md) | single instance, rate limits, residual SSRF risk, other boundaries | [已知限制](../limitations.md) |

---

[← Back to main README](../../README_EN.md) · [简体中文](../README.md)
