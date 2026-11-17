# Publishing Images (GitHub Actions / GHCR)

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../release.md)

<a id="release"></a>

`.github/workflows/docker-publish.yml` uses `matrix.variant: [base, webui]` on a pushed `v*` tag to **produce two groups of images in parallel**, each a **linux/amd64 + linux/arm64** multi-arch manifest, published to GitHub Container Registry as `ghcr.io/zsyo/feedwatch`. The base group keeps the usual version tags and `latest`; the webui group uses "version + `-webui` suffix" and has its own moving pointer, the **`webui`** tag (not `latest-webui`), so the two groups' pointers never overwrite each other. Docker picks the manifest matching the host architecture automatically, so the same `pull` command works on an x86 server and an ARM board. The image name is derived from `${{ github.repository }}`, so a fork becomes `ghcr.io/<your-username>/feedwatch` with no workflow edit needed.

<a id="new-release"></a>

## Publishing a New Version

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

The tag must be `vMAJOR.MINOR.PATCH` (suffixes like `-rc.1` are allowed); a malformed tag makes the workflow fail immediately without pushing any image. One tag triggers both matrix jobs, and `fail-fast: false` keeps one group's failure from cancelling the other.

| Git tag | base group image tags | webui group image tags |
| --- | --- | --- |
| `v1.2.3` | `1.2.3`, `1.2`, `latest` | `1.2.3-webui`, `1.2-webui`, `webui` |
| `v1.2.3-rc.1` | `1.2.3-rc.1` | `1.2.3-rc.1-webui` (a prerelease only adds its own tag and moves no pointers) |

> `v1.2.3` / `1.2.3` here are illustrative, not an actually released version. General rule: the base group generates `{{version}}` / `{{major}}.{{minor}}` / `latest`, and the webui group adds `-webui` to the first two, with the moving pointer fixed as `webui`. Every formal release adds full version tags to both groups and moves each group's own pointer (base's `latest`, webui's `webui`); `<major>.<minor>` follows its own group's version only. Prereleases add only their own tag and move neither pointer. For the concrete published versions, see the tag list in the repo's Releases / GHCR package; to pin one, use `docker pull ghcr.io/zsyo/feedwatch:<version>` or `docker pull ghcr.io/zsyo/feedwatch:<version>-webui`.

CI switches `file:` with `matrix.variant` (`Dockerfile` / `Dockerfile.webui`). The webui group's `-webui` suffix is written directly into every semver pattern (`{{version}}-webui`), while its moving pointer is given separately by `type=raw,value=webui`: metadata-action's `flavor.suffix` applies to **all** generated tags, so using it for the suffix would produce a `webui-webui` pointer; the webui group also sets `latest=false` so it does not fight the base group over the `latest` tag. The two groups' layer caches are isolated by variant and do not evict each other. The tag is injected into the binary with `-ldflags "-X main.version=..."`, so `./feedwatch -version` inside the container shows it. **No extra secrets are needed**: the built-in `GITHUB_TOKEN` with `packages: write` logs into GHCR, and the build cache uses the GitHub Actions cache.

<a id="rc-flow"></a>

## Release a Prerelease (rc) First, Then Promote

For breaking changes, or changes to files baked into the image (Dockerfile / config template / frontend assets), publish an rc first so the pointers stay on the previous stable version; promote only after verification.

**① Tag the rc** (on the commit to be released):

```bash
git tag -a v1.2.3-rc.1 -m "release v1.2.3-rc.1"
git push origin v1.2.3-rc.1
```

Only `1.2.3-rc.1` and `1.2.3-rc.1-webui` are added; **`latest` and `webui` do not move** — anyone pulling a pointer still gets the previous formal release, unaffected by the work in progress.

**② Verify with the fixed tag** (not the pointer, so another release session cannot change it mid-verification):

```bash
docker pull ghcr.io/zsyo/feedwatch:1.2.3-rc.1-webui
docker run -d --name fw-rc -p 18080:8080 ghcr.io/zsyo/feedwatch:1.2.3-rc.1-webui
docker logs fw-rc     # first start on an empty DB: master key, admin password, listening
# clean up afterwards (no volume in the container, deleting it leaves no trace)
docker rm -f fw-rc
```

Checklist: first start yields the one-time admin password and the forced first-login password change → create a regular user → confirm "Catalog" sits above "My Subscriptions" in the sidebar with no sibling "Pending" item → the example catalog is visible → search once with a category name as keyword and confirm nothing matches (categories only go through the adjacent dropdown), switch to "Added time" and toggle ascending/descending to confirm order changes → entering the Pending sub-page from the header keeps "Catalog" highlighted and going back works → after one-click subscribe, "My Subscriptions" shows it with no historical backfill → submit new / update requests → the Pending page shows the risk notice and before/after comparison and allows copying → in "Subscription Management", confirm the sidebar expands to 5 sub-items, collapses when leaving the group (e.g. to "Global Settings"), and parent and child are never both highlighted → open all 5 sub-pages to confirm titles and data, and confirm the old `/admin/catalog` link redirects to "Entries" → on "Entries", filter by category, switch between ID / subscriber count and toggle ascending / descending, confirm the default is ascending ID and the title shows the match count after filtering → as admin, approve / reject on "Review Queue", add / remove categories on "Categories", round-trip once on "Import / Export" → in "Global Settings", confirm the `presets.base_url` / `presets.timeout` items and their descriptions → temporarily point `presets.base_url` at a reachable mirror and restart, click "Fetch Remote Presets" on the "Remote Presets" sub-page to see per-file results and counts, and confirm a second click only skips without duplicate additions → manually edit a preset on "Entries" and confirm it becomes "protected" and is not overwritten by a higher version, then click "Unprotect" to return to "unlocked" → delete a preset on "Entries" and confirm a later sync does not resurrect it → user-side status and rejection reasons are correct → create a channel (with secrets) in the UI → trigger one fetch + delivery round → delivery records and `secret_state` match expectations. For the base variant, run `-once -dry-run` once to exercise config parsing and rendering. **Verify upgraded old data separately**: mount a copy of an old volume, confirm it starts, v5 adds the three catalog tables, v6 adds the three preset columns and `catalog_preset_state`, channel credentials can send after being re-entered per the docs, and the first remote sync claims existing entries instead of inserting duplicates.

**③ Found a problem? Publish the next rc**: fix the code → new commit → `v1.2.3-rc.2`. **Never re-push an rc tag of the same name**: GHCR overwrites the same tag, so anyone who already pulled `rc.1` can no longer reproduce that image.

**④ Once it passes, tag the formal release** — only then do the pointers move:

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

This produces `1.2.3` / `1.2` / `latest` and `1.2.3-webui` / `1.2-webui` / `webui`.

> The formal tag must point at **the exact commit that was verified** (or at a new HEAD re-verified from there). Committing more code to `main` after the rc and then tagging HEAD is publishing untested content.
>
> rc tags and images stay in GHCR as history and need not be deleted; but be clear that `docker pull …:1.2.3-rc.1` fetches a prerelease — do not write it into deployment docs as an equivalent of the formal release.

<a id="prebuilt-images"></a>

## Using Prebuilt Images (no source or local build needed on the server)

```bash
cp config.example.yaml config.yaml    # fill in email.from / to on the email item in channels
docker pull ghcr.io/zsyo/feedwatch:latest
docker run -d --name feedwatch \
  -e RESEND_API_KEY="re_xxx" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v feedwatch-data:/app/data \
  ghcr.io/zsyo/feedwatch:latest
```

The WebUI variant uses the `webui` pointer tag (or a fixed `1.2.3-webui`), maps one more port and uses its own data volume; **no `config.yaml` and no channel secret variables are needed**, and the initial admin password is printed in the logs only once:

```bash
docker pull ghcr.io/zsyo/feedwatch:webui
docker run -d --name feedwatch-webui \
  -p 8080:8080 \
  -v feedwatch-webui-data:/app/data \
  ghcr.io/zsyo/feedwatch:webui
docker logs -f feedwatch-webui
```

> After logging in, create a regular user in the UI to configure subscriptions and channels (the admin cannot — see [Roles & Permissions](webui.md#roles)); secrets are entered in the UI, encrypted and stored in the DB, never passing through environment variables.

> With a named volume, **do not** add `--user`: Docker initializes an empty volume from the owner of `/app/data` inside the image, which is exactly `app`; forcing a host uid instead makes it unwritable. The WebUI SQLite volume behaves the same way.

To keep state in a host directory, use a bind mount and set the container user to that directory's owner. `docker run` is expanded by the shell, so `$(...)` can be written directly:

```bash
mkdir -p data
docker run -d --name feedwatch \
  --user "$(id -u):$(id -g)" \
  -e RESEND_API_KEY="re_xxx" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  ghcr.io/zsyo/feedwatch:latest
```

`--user "$(id -u):$(id -g)"` is equivalent to overriding `FW_UID` / `FW_GID` in compose, and likewise requires the current user to own `data/`; see [Replacing the State Volume with a Host Directory](docker.md#bind-mount) for the reasoning. **Under rootless Docker change it to `--user 0:0`**: uid 1000 in the container maps to 100999 on the host and cannot write a directory owned by your own 1000, while container root maps exactly to the host login user.

> With multiple channels, add the matching `-e WECOM_WEBHOOK_URL=...`, `-e TELEGRAM_BOT_TOKEN=...`, etc. to `docker run` (or to compose's `environment`); any missing variable is reported by name in the startup log.

To switch to compose: remove `build: .` from `docker-compose.yml`, change `image` to the remote image, and keep `environment` / `volumes` / `restart` / `user` exactly as they are:

```yaml
services:
  feedwatch:
    image: ghcr.io/zsyo/feedwatch:latest   # or pin a specific published version tag
    # build: .                             # delete this line
```

Then run `docker compose pull && docker compose up -d`. The WebUI variant has its own `docker-compose.webui.yml`: change `image` to `ghcr.io/zsyo/feedwatch:webui` and delete the `build:` block, keeping `ports` / the separate data volume / `environment` unchanged.

<a id="multi-arch"></a>

## How the Multi-arch Build Works

The `Dockerfile` build stage is pinned to `FROM --platform=$BUILDPLATFORM` and cross-compiles with `CGO_ENABLED=0` using BuildKit's automatically injected `TARGETOS` / `TARGETARCH`, so the Go toolchain always runs on the runner's native amd64; QEMU is only involved in the runtime stage's `apk add` / `adduser` (for the arm64 variant), far faster than compiling the whole Go toolchain under QEMU. Both `golang:1.27-alpine` and `alpine:3.21` official images have `arm64v8` variants.

Both Dockerfiles share this structure: `Dockerfile.webui` adds a `node:22-alpine` frontend stage (also on `$BUILDPLATFORM`, producing plain JS/CSS), and the Go stage uses `-tags webui` to `go:embed` `internal/webui/embedded/` into the binary, so Node is not needed at run time. The base `Dockerfile` stays byte-for-byte unchanged, so the base image contains no npm, frontend assets, HTTP server or SQLite; `go list -deps ./cmd/feedwatch` shows neither `modernc.org/sqlite`, nor `internal/webui`, nor `database/sql`.

A local `docker compose build` without `--platform` produces only a **native-architecture** image; to reproduce multi-arch use `docker buildx build --platform linux/amd64,linux/arm64 .` (a multi-arch manifest can only be pushed to a registry; `--load` supports a single platform). The Dockerfile relies on BuildKit's injected platform variables (enabled by default since Docker 23).

> For production, GitHub's official security advice is to pin the workflow's actions to commit SHAs; currently major tags are used: `actions/checkout@v7`, `docker/setup-qemu-action@v4`, `docker/setup-buildx-action@v4`, `docker/login-action@v4`, `docker/metadata-action@v6`, `docker/build-push-action@v7`.

---

[← Back to main README](../../README_EN.md) · [简体中文](../release.md)
