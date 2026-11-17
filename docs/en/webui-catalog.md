# Catalog & Remote Presets

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../webui-catalog.md)

<a id="catalog"></a>

<a id="remote-presets"></a>

## Remote Presets ("Fetch Remote Presets")

`presets/` in the repository is a set of **pure data files that can be updated with the project**: `manifest.json` lists the category files, and `language-runtime.json` / `cli-tools.json` / `editors.json` / `devops.json` each hold one category of common subscriptions. Every preset carries a stable `preset_key` and a monotonically increasing integer `version` (not to be confused with the document format's `format_version`). The default source is `https://raw.githubusercontent.com/zsyo/feedwatch/main/presets`; these JSON files **are not baked into the runtime image** — they are read remotely at run time, so when upstream adds a subscription, deployments do not need to rebuild the image.

An admin clicks once under "Subscription Management → Remote Presets → Fetch Remote Presets": one request downloads the manifest and all category files, and **writes them in a single transaction only after all of them validate**. If any file 404s, times out, has invalid JSON or conflicting fields, the whole batch is rolled back without writing a single row, and the error response includes each file's status for diagnosis.

| Local situation | Sync behavior |
| --- | --- |
| No local entry with this `preset_key` | Add a catalog entry (a template only, **creates no user subscriptions**) |
| Remote `version` is higher | Overwrite that entry's name / URL / category / description / homepage / enabled state |
| Remote `version` is equal or older | Skip; never overwrite newer local config with older config |
| Manually edited by an admin, or a user's update request was approved | Mark "protected" and skip; click "Unprotect" on the row to return it to "unlocked" |
| Deleted by an admin | Write a tombstone; never resurrect it |
| Existing entries in an old DB with the same URL (examples / manual / imported) | **Claim** by `url_key`: unedited ones upgrade by version normally, edited ones stay protected after being claimed |

The "Entries" sub-page has a "Preset Status" column: `vN` + "unlocked / protected", "manual" for entries without a `preset_key`, and protected entries can be restored with "Unprotect" in the actions column. **These two badges reflect only local locking**: `vN` is the locally applied preset version and "unlocked" means it has not been manually edited and may be overwritten by a higher version — **it does not mean a newer version exists remotely**. The system does not check for updates in the background; versions are compared only when you click sync. A sync returns counts for added / updated / skipped / protected / claimed plus per-file results; the "Remote Presets" sub-page also lists these version / protection / tombstone / atomicity rules nearby.

Two settings live under "Global Settings" (saving **requires a process restart**, like the polling parameters):

- `presets.base_url`: switch to a GitHub raw mirror, CDN or an internally reachable address. Only `https` on port 443 is allowed; userinfo, query strings and fragments are forbidden; domains resolving to private, loopback, link-local or cloud-metadata addresses are rejected; redirects are re-checked hop by hop, at most 3 hops. Implicit `HTTPS_PROXY` / `NO_PROXY` proxy support is preserved.
- `presets.timeout`: the total time limit for **all files** in one sync (default `15s`, range 1s–2m), independent of `http.timeout` which governs a single feed.

Fallback when the official raw host is unreachable from mainland China: point `presets.base_url` at a reachable mirror (recommended); or reshape the category JSON into a `feedwatch-catalog` v1 document (fields `name` / `url` / `category` / `description` / `homepage` / `enabled`) and load it manually via "Subscription Management → Import / Export". **Preset JSON and import documents are two different formats — you cannot paste a downloaded category file into the import box**; import remains the version-less `skip` / `overwrite` / `error` three-strategy path, entirely separate from remote sync.

**Note**: the default address reads `presets/` on the repository's `main` branch, so those files only exist once pushed to GitHub. Clicking the button while developing locally on an unpushed branch / old tag returns 404 — expected behavior, not a fault; for verification, point `presets.base_url` at a reachable mirror.

---

[← Back to main README](../../README_EN.md) · [简体中文](../webui-catalog.md)
