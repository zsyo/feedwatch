# Configuration

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../configuration.md)

<a id="configuration"></a>

- `config.example.yaml`: the **only config template committed to the repo**, with every field and comments.
- `config.yaml`: the local runtime config copied from the template, containing your domain, recipients and feed list; ignored by `.gitignore`, so editing it produces no git diff. `.env` and `config.local.yaml` are likewise uncommitted.
- Secrets are never written into YAML; they are read only from environment variables.
- Adding a monitored project = copy a `feeds` block and change `name` / `url`, **zero code changes** (full URL for Atom, `owner/repo` for GitHub Releases).
- Adding a notification channel = append a block in `notify.channels` and set its environment variables, **zero code changes**; several channels can coexist. See `config.example.yaml` and [Notification Channels](notifications.md) for the full field list.

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

  # GitHub Releases: for repos without releases.atom; url is owner/repo
  - name: acme/app
    type: github
    url: acme/app
    enabled: true
    options:
      release_scope: stable    # stable | prerelease | all
      # token: ${GITHUB_TOKEN} # optional; anonymous 60 req/h, 5000 req/h with a token
```

With `filter.stable_only: true`, a 3–4 segment numeric version is extracted from the title, so GitHub's human-readable titles work (`[release-branch.go1.27] go1.27.3`, `Rust 1.99.1`, `v3.15.2`) while `rc/beta/alpha/pre/dev/nightly/snapshot/aN/bN` suffixes and non-version tags such as `Python 3.10 end of life` are excluded. The email shows the extracted version token (e.g. `go1.27.3`). If a project's real versions have only two segments, override that feed's `version_pattern`. `stable_only: false` lets every entry through (prereleases are still flagged as unstable in the data). `type: github` does **not** use title heuristics: the version range comes from `options.release_scope` based on GitHub's `is_prerelease` / `is_draft` flags (`stable` = final releases only, `prerelease` = prereleases only, `all` = everything); drafts are always skipped, and its filter keeps only `include` / `exclude` / `version_pattern`.

The baseline email takes the **semantically highest version**, not the entry with the newest `updated` timestamp — when several maintenance branches are released on the same day, timestamps put an old branch's patch release first.

---

[← Back to main README](../../README_EN.md) · [简体中文](../configuration.md)
