# 配置详解

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/configuration.md)

<a id="configuration"></a>

- `config.example.yaml`：**唯一提交进仓库**的配置模板，带全部字段与注释。
- `config.yaml`：从模板复制出的本地运行配置，含你的域名、收件人与 feed 列表，已被 `.gitignore` 忽略——随便改都不会产生 git diff。`.env`、`config.local.yaml` 同样不提交。
- 密钥永远不写进 YAML，只从环境变量读。
- 新增一个监控项目 = 复制一段 `feeds`、改 `name` / `url`，**零代码改动**（Atom 用完整 URL，GitHub Releases 用 `owner/repo`）。
- 新增一个通知渠道 = 在 `notify.channels` 加一段、配好环境变量，**零代码改动**；可多个并存。完整字段见 `config.example.yaml` 与[通知渠道](notifications.md)。

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

  # GitHub Releases：给没有 releases.atom 的仓库用；url 填 owner/repo
  - name: acme/app
    type: github
    url: acme/app
    enabled: true
    options:
      release_scope: stable    # stable | prerelease | all
      # token: ${GITHUB_TOKEN} # 可选，匿名 60 次/小时，带 token 5000 次/小时
```

`filter.stable_only: true` 时会从标题里提取 3~4 段数字版本号，因此能处理 GitHub 的人类可读标题（`[release-branch.go1.27] go1.27.3`、`Rust 1.99.1`、`v3.15.2`），并排除 `rc/beta/alpha/pre/dev/nightly/snapshot/aN/bN` 后缀以及 `Python 3.10 end of life` 这类非版本标签。邮件里显示的是提取出的版本词元（如 `go1.27.3`）。若某项目真实版本只有两段，用该 feed 的 `version_pattern` 覆盖即可。`stable_only: false` 会放行所有条目（预发布仍会在数据里被标注为 unstable）。`type: github` 不走标题启发式：版本范围由 `options.release_scope` 按 GitHub 的 `is_prerelease` / `is_draft` 标记决定（`stable` 仅正式版、`prerelease` 仅预发布、`all` 全部），草稿永远跳过；对应的 filter 只保留 `include` / `exclude` / `version_pattern`。

基线邮件取的是**语义上最高的版本**，不是 `updated` 时间最新的一条——同一天发布多个维护分支时，时间戳会把旧分支的补丁版排到最前面。

---

[← 返回主文档](../README.md) · [English](en/configuration.md)
