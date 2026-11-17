# 发布镜像（GitHub Actions / GHCR）

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/release.md)

<a id="release"></a>

`.github/workflows/docker-publish.yml` 在推送 `v*` tag 时用 `matrix.variant: [base, webui]` **并行产出两组**镜像，每组各自是 **linux/amd64 + linux/arm64** 双架构 manifest，发布到 GitHub Container Registry：`ghcr.io/zsyo/feedwatch`。base 组沿用常规的版本号 tag 与 `latest`；webui 组是「版本号 + `-webui` 后缀」，它的移动指针是独立的 **`webui`** tag（而不是 `latest-webui`），两组的指针互不覆盖。Docker 会按宿主机架构自动挑选 manifest，同一条 `pull` 命令在 x86 服务器和 ARM 板子上都能用。镜像名由 `${{ github.repository }}` 自动生成，fork 后会变成 `ghcr.io/<你的用户名>/feedwatch`，无需改 workflow。

<a id="new-release"></a>

## 发布一个新版本

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

tag 必须是 `vMAJOR.MINOR.PATCH`（允许 `-rc.1` 一类后缀）；格式不对时 workflow 立即报错退出，不会推镜像。一个 tag 同时触发两个 matrix job，`fail-fast: false` 保证其中一组失败不会取消另一组。

| Git tag | base 组镜像 tag | webui 组镜像 tag |
| --- | --- | --- |
| `v1.2.3` | `1.2.3`、`1.2`、`latest` | `1.2.3-webui`、`1.2-webui`、`webui` |
| `v1.2.3-rc.1` | `1.2.3-rc.1` | `1.2.3-rc.1-webui`（预发布只新增自己的 tag，不移动任何指针） |

> 本节里的 `v1.2.3` / `1.2.3` 都只是示意，不代表某个已发布版本。通用规则：base 组按 `{{version}}` / `{{major}}.{{minor}}` / `latest` 生成，webui 组把前两者加 `-webui` 后缀，移动指针固定叫 `webui`。每次正式发版两组都会新增完整版本号 tag，并各自移动自己的指针（base 的 `latest`、webui 的 `webui`），`<主版本>.<次版本>` 只跟随各自的版本号；预发布只新增自己的 tag，两个指针都不动。已发布的具体版本以仓库 Releases / GHCR package 的 tag 列表为准，固定版本时用 `docker pull ghcr.io/zsyo/feedwatch:<版本>` 或 `docker pull ghcr.io/zsyo/feedwatch:<版本>-webui`。

CI 用 `matrix.variant` 切换 `file:`（`Dockerfile` / `Dockerfile.webui`）。webui 组的 `-webui` 后缀直接写在每个 semver pattern 里（`{{version}}-webui`），移动指针由 `type=raw,value=webui` 单独给出：metadata-action 的 `flavor.suffix` 会作用到**所有**生成的 tag，用它加后缀会把指针写成 `webui-webui`；同时 webui 组设 `latest=false`，避免它和 base 组抢同一个 `latest` tag。两组 layer cache 按 variant 隔离，互不挤占。tag 通过 `-ldflags "-X main.version=..."` 注入二进制，容器内 `./feedwatch -version` 能直接看到版本。**无需配置任何额外 secret**：用内置 `GITHUB_TOKEN` + `packages: write` 登录 GHCR，构建缓存走 GitHub Actions cache。

<a id="rc-flow"></a>

## 先发预发布（rc），验证通过再转正式版

破坏性改动、或改动了镜像内文件（Dockerfile / 配置模板 / 前端产物）时，建议先发一个 rc，让指针保持在上一个稳定版，验证没问题再转正。

**① 打 rc**（在待发布的提交上）：

```bash
git tag -a v1.2.3-rc.1 -m "release v1.2.3-rc.1"
git push origin v1.2.3-rc.1
```

只新增 `1.2.3-rc.1` 与 `1.2.3-rc.1-webui` 两个 tag，**`latest` 与 `webui` 都不动**——此时别人 `pull` 指针拿到的仍是上一个正式版，不会被半成品波及。

**② 用固定 tag 验证**（不要用指针，避免验证期间指针被别的发布会话改动）：

```bash
docker pull ghcr.io/zsyo/feedwatch:1.2.3-rc.1-webui
docker run -d --name fw-rc -p 18080:8080 ghcr.io/zsyo/feedwatch:1.2.3-rc.1-webui
docker logs fw-rc     # 空库首启：master key、admin 密码、listening
# 验完清理（容器里没有卷，删掉即不留痕迹）
docker rm -f fw-rc
```

检查项：首启能拿到一次性管理员密码并完成首登改密 → 建普通用户 → 确认侧边栏「订阅中心」排在「我的订阅」之上、且没有平级的「审核中」项 → 订阅中心能看到示例目录 → 用分类名当关键字搜一次确认搜不到（分类只走旁边的下拉）、切到「收录时间」并切换升降序确认顺序变化 → 从页头进入审核中子页时「订阅中心」仍高亮、可返回 → 一键订阅后「我的订阅」出现且没有补发历史 → 提交新增 / 更新申请 → 「审核中」页显示风险提示与改前 / 改后对比、可复制 → 进「订阅管理」确认侧边栏展开 5 个子项、离开分组（例如去「全局设置」）自动收起、父项与子项不会同时高亮 → 逐个打开 5 个子页确认标题与数据，并确认旧链接 `/admin/catalog` 会跳到「订阅条目」 → 在「订阅条目」子页按分类筛选、在 ID / 订阅人数之间切换并切换升 / 降序，确认默认是 ID 升序、筛选后标题显示命中数 → 管理员在「审核队列」通过 / 驳回、在「分类管理」增删分类、在「导入导出」往返一次 → 在「全局设置」确认 `presets.base_url` / `presets.timeout` 两项及说明 → 把 `presets.base_url` 临时改成一个可访问的镜像并重启，在「远程预设」子页点「获取远程预设数据」看到逐文件结果与计数，重复点击只跳过、不重复新增 → 在「订阅条目」子页手动编辑某条预设后确认它变「已保护」且不被更高版本覆盖、点「解除保护」后恢复「未锁定」 → 在「订阅条目」子页删除一条预设后再次同步不会复活 → 用户侧状态与驳回理由正确 → 在页面上建渠道（含密钥）→ 触发一轮抓取与投递 → 投递记录与 `secret_state` 符合预期。base 变体则用 `-once -dry-run` 跑一遍配置解析与渲染。**升过级的旧数据要单独验**：拿一份旧卷副本挂上去，确认能启动、v5 补出三张目录表、v6 补出预设三列与 `catalog_preset_state`、渠道凭据按文档重填后能发出去，且首次远程同步能认领存量条目而非重复插入。

**③ 发现问题就发下一个 rc**：改代码 → 新提交 → `v1.2.3-rc.2`。**不要重推同名 rc tag**：GHCR 上的同名 tag 会被覆盖，已经拉过 `rc.1` 的人将无法复现当时的镜像。

**④ 通过后打正式 tag**，指针才会前移：

```bash
git tag -a v1.2.3 -m "release v1.2.3"
git push origin v1.2.3
```

产出 `1.2.3` / `1.2` / `latest` 与 `1.2.3-webui` / `1.2-webui` / `webui`。

> 正式 tag 必须打**实际验证过的那个提交**（或在其之上重新验证新的 HEAD）。rc 之后又在 `main` 上提了代码却直接把正式 tag 打在 HEAD 上，等于发布了没测过的内容。
>
> rc 的 tag 与镜像会一直留在 GHCR 作为历史，不必删除；但要清楚 `docker pull …:1.2.3-rc.1` 拿到的是预发布版本，不要把它当成正式版的等价物写进部署文档。

<a id="prebuilt-images"></a>

## 直接使用预构建镜像（服务器无需源码、无需本地编译）

```bash
cp config.example.yaml config.yaml    # 填好 channels 里 email 条目的 email.from / to
docker pull ghcr.io/zsyo/feedwatch:latest
docker run -d --name feedwatch \
  -e RESEND_API_KEY="re_xxx" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v feedwatch-data:/app/data \
  ghcr.io/zsyo/feedwatch:latest
```

WebUI 变体用 `webui` 指针 tag（或换成固定的 `1.2.3-webui`），多映射一个端口并用自己的数据卷；**不需要 `config.yaml`，也不需要任何渠道密钥变量**，首次启动的管理员密码只在日志里打印一次：

```bash
docker pull ghcr.io/zsyo/feedwatch:webui
docker run -d --name feedwatch-webui \
  -p 8080:8080 \
  -v feedwatch-webui-data:/app/data \
  ghcr.io/zsyo/feedwatch:webui
docker logs -f feedwatch-webui
```

> 登录后在页面里建一个普通用户来配订阅与通知方式（管理员不能配，见[身份与权限](webui.md#roles)）；密钥填在页面里，加密后存库，不再经过环境变量。

> 用命名卷时**不要**加 `--user`：Docker 会用镜像内 `/app/data` 的属主初始化空卷，正好是 `app` 用户；强行指定宿主机 uid 反而会写不进去。WebUI 的 SQLite 卷同理。

想把状态放在宿主机目录，改用 bind mount 并把容器用户设成该目录的属主。`docker run` 由 shell 展开 `$(...)`，可以直接写：

```bash
mkdir -p data
docker run -d --name feedwatch \
  --user "$(id -u):$(id -g)" \
  -e RESEND_API_KEY="re_xxx" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  ghcr.io/zsyo/feedwatch:latest
```

`--user "$(id -u):$(id -g)"` 与 compose 里的 `FW_UID` / `FW_GID` 覆盖等价；同样要求当前用户就是 `data/` 的属主，原理见[把状态卷换成宿主机目录](docker.md#bind-mount)。**rootless Docker 下要改成 `--user 0:0`**：容器里的 uid 1000 映射到宿主的 100999，写不进您自己 1000 拥有的目录，而容器 root 恰好等于宿主的登录用户。

> 用多通道时，把对应的 `-e WECOM_WEBHOOK_URL=...`、`-e TELEGRAM_BOT_TOKEN=...` 等一并加进 `docker run`（或写进 compose 的 `environment`）；漏掉的变量会在启动日志里直接报出名字。

改用 compose：删掉 `docker-compose.yml` 里的 `build: .`，把 `image` 换成远程镜像，`environment` / `volumes` / `restart` / `user` 全部原样保留：

```yaml
services:
  feedwatch:
    image: ghcr.io/zsyo/feedwatch:latest   # 也可固定到某个已发布的具体版本 tag
    # build: .                             # 删除这一行
```

然后 `docker compose pull && docker compose up -d`。WebUI 变体对应独立的 `docker-compose.webui.yml`：把 `image` 换成 `ghcr.io/zsyo/feedwatch:webui` 并删掉 `build:` 段，`ports` / 独立数据卷 / `environment` 原样保留。

<a id="multi-arch"></a>

## 双架构是怎么构建的

`Dockerfile` 编译阶段固定 `FROM --platform=$BUILDPLATFORM`，用 BuildKit 自动注入的 `TARGETOS` / `TARGETARCH` 做 `CGO_ENABLED=0` 交叉编译，Go 工具链始终跑在 runner 原生 amd64 上；QEMU 只负责运行阶段的 `apk add` / `adduser`（arm64 变体），比在 QEMU 里编译整个 Go 工具链快得多。`golang:1.27-alpine` 与 `alpine:3.21` 的官方镜像均含 `arm64v8` 变体。

两个 Dockerfile 都是这个结构：`Dockerfile.webui` 多一个 `node:22-alpine` 前端 stage（同样跑在 `$BUILDPLATFORM`，产物是纯 JS/CSS），Go stage 用 `-tags webui` 把 `internal/webui/embedded/` 一起 `go:embed` 进二进制，运行阶段不需要 Node。base 的 `Dockerfile` 保持逐字节不变，因此 base 镜像里没有 npm、前端资源、HTTP server 或 SQLite；`go list -deps ./cmd/feedwatch` 也不会出现 `modernc.org/sqlite`、`internal/webui`、`database/sql`。

本地 `docker compose build` 不带 `--platform`，只产出**本机架构**镜像；要复现双架构需 `docker buildx build --platform linux/amd64,linux/arm64 .`（多架构 manifest 只能推到 registry，`--load` 只支持单平台）。Dockerfile 依赖 BuildKit 自动注入的平台变量（Docker 23+ 默认启用）。

> 生产环境建议把 workflow 里的 action 固定到 commit SHA（GitHub 官方安全建议）；当前用的是 major tag：`actions/checkout@v7`、`docker/setup-qemu-action@v4`、`docker/setup-buildx-action@v4`、`docker/login-action@v4`、`docker/metadata-action@v6`、`docker/build-push-action@v7`。

---

[← 返回主文档](../README.md) · [English](en/release.md)
