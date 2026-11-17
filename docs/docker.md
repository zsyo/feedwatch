# Docker 部署（WSL 里执行）

> [← 返回主文档](../README.md) · [文档导航](../README.md#toc) · [English](en/docker.md)

<a id="docker"></a>

```bash
cp config.example.yaml config.yaml   # 本地私有配置，会被只读挂载进容器
cp .env.example .env                 # 填真实密钥：邮件 / 你启用的推送渠道
docker compose build
docker compose up -d
docker compose logs -f    # 首次应看到基线通知已推送到每个渠道
docker compose restart    # 无新版本时应完全静默
```

`docker-compose.yml` 用 `container_name` 把容器名固定为 `feedwatch`，所以也可以直接 `docker logs -f feedwatch`、`docker exec -it feedwatch sh`；上面那些 `docker compose ...` 命令用的是 service 名，不受影响。设置 `container_name` 后该服务不能横向扩容，但本项目共用一份状态文件，本来就只应跑单个实例，没有影响。卷仍是原来的 `feedwatch-data`，改名后重建容器不会丢 `state.json`、不会重发基线通知。

镜像为多阶段构建（`golang:1.27-alpine` → `alpine:3.21`），静态二进制、非 root 用户、含 CA 证书与时区；编译阶段用 BuildKit 的 `TARGETOS` / `TARGETARCH` 交叉编译，因此同一份 Dockerfile 可产出 amd64 / arm64 镜像。配置通过只读挂载传入，各渠道凭证通过 `docker-compose.yml` 的 `environment` 从 `.env` 注入（新增自定义变量时记得在那里补一行），状态通过命名卷 `feedwatch-data:/app/data` 持久化。compose 里的 `user: "${FW_UID:-app}:${FW_GID:-app}"` 默认沿用镜像内的 `app` 用户（与命名卷属主一致）；改成下面的 bind mount 时才需要用宿主机 uid/gid 覆盖。

> 镜像内烘焙的是 `config.example.yaml` 的**占位默认值**（不含任何真实域名或收件人），因此干净 clone 也能直接 `docker compose build`；`.dockerignore` 会挡住本地 `config.yaml`，防止私有配置进入镜像层。不带挂载直接 `docker run` 时用的就是这份占位配置，需自行 `-v` 挂真实配置。

> **卷丢了会重发基线邮件**（因为程序会认为这是首次运行）。这是预期行为。若手工 `docker run`，记得 `-e RESEND_API_KEY=... -v feedwatch-data:/app/data`。

<a id="bind-mount"></a>

## 把状态卷换成宿主机目录

`docker-compose.yml` 里 `- feedwatch-data:/app/data` 用的是**命名卷**：左边是卷名而不是路径，数据由 Docker 自己保管（Linux 上在 `/var/lib/docker/volumes/<项目名>_feedwatch-data/_data`，Docker Desktop / WSL 则在虚拟机磁盘内），宿主机上看不到普通文件夹；文件末尾的 `volumes: feedwatch-data:` 只是声明这个卷存在。`docker compose down` 默认不删卷，`down -v` 才会。

想让状态落在 compose 同目录的 `data/` 下，把这一行换成 bind mount 即可，**容器内路径 `/app/data` 与 `FEEDWATCH_STATE_PATH` 都不用改**：

```yaml
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./data:/app/data          # 替换 - feedwatch-data:/app/data
# 顶层 volumes: 段可整段删掉
```

**bind mount 会保留宿主机目录的属主**：`Dockerfile` 里的 `chown -R app:app /app` 只作用于镜像内，盖不到挂载进来的目录。而状态写入是 `CreateTemp` + `rename`，需要在目录里**创建文件**，所以目录必须对容器用户可写。若目录是 `drwxr-xr-x docker:docker`，容器内的 `app` 落在 others 上只有 `r-x`，就会 `permission denied` → 状态永远存不下 → 每轮都判定首轮并**重发基线通知**（Linux 服务器必现，WSL + Docker Desktop 的文件共享通常较宽松）。

推荐做法是**不动宿主机权限**，让容器以该目录属主的身份运行。`docker-compose.yml` 已经设好 `user: "${FW_UID:-app}:${FW_GID:-app}"`，把宿主机 uid/gid 传进去即可：

先确认目录属主就是当前登录用户：`stat -c '%u:%g' data` 应与 `id -u`:`id -g` 一致。若目录属主是别的用户（例如 `docker:docker`），就把**那一对数字**写进 `.env` 的 `FW_UID` / `FW_GID`，或先 `chown` 给当前用户——`$(id -u)` 取的是执行命令的人，不是目录属主，两者不一致时照样会 `permission denied`。

```bash
mkdir -p data

# 方式一（推荐）：写进 .env，之后任意终端执行 compose 都一致
printf 'FW_UID=%s\nFW_GID=%s\n' "$(id -u)" "$(id -g)" >> .env
docker compose up -d

# 方式二：只对本次命令生效（shell 先展开 $(...)，再作为环境变量传给 compose）
FW_UID=$(id -u) FW_GID=$(id -g) docker compose up -d
```

> **Compose 不会执行 `$(...)`**：它只做 `${VAR}` 插值，把 `user: "$(id -u):$(id -g)"` 写进 YAML 会被当成字面字符串、导致容器启动失败。所以 `.env` 里必须是展开后的**数字**，上面的 `printf` 就是干这个的。另外别用变量名 `UID`——bash/zsh 里 `UID` 是只读 shell 变量，`UID=$(id -u)` 会报 `readonly variable`，因此这里用 `FW_UID` / `FW_GID`。两者只在宿主机侧用于 Compose 插值，**不会**传入容器。
>
> 两者都不设置时 `user` 为 `app:app`，等价于镜像的 `USER app`，正好匹配默认命名卷的属主，所以默认的命名卷部署方式完全不受影响。

两条备选：保持默认用户而给目录授权（`sudo chown <容器内 app 的 uid>:<gid> data`，或 `chmod 777 data`——所有用户可写，不推荐）；或在 compose 里把 `user` 写死成具体数字。自定义 compose 文件时，同样只需在 service 下加 `user: "1000:1000"`（换成宿主机实际值；**rootless 模式不适用，见下节**）。

**配错会在启动期就被拦住**：`jsonfile.New` 在建 store 时先用 `CreateTemp` 在状态目录探测一次并立即删除，不可写就直接启动失败（`state directory ... is not writable`），不会先发出基线邮件才发现存不下。所以现在看到的是启动报错 + 容器反复重启，而不是每半小时一封重复通知。

`data/` 已在 `.gitignore` 中，不会进仓库。

**切换等于从空状态开始**：新目录没有 `state.json`，程序会当成首次运行并重发一封基线通知。想保留已见集合，先把命名卷里的状态拷出来（真实卷名一般带项目名前缀，先用 `docker volume ls | grep feedwatch` 确认）：

```bash
docker run --rm -v feedwatch_feedwatch-data:/src -v "$PWD/data:/dst" alpine:3.21 \
  cp /src/state.json /dst/state.json
```

想保留命名卷、只指定宿主机位置，也可以给顶层卷加 `driver_opts: {type: none, o: bind, device: /绝对路径/data}`，但日常直接用 `./data` 更简单。

<a id="rootless-docker"></a>

## rootless Docker 下的 uid 映射

以 rootless 模式运行 Docker（RootlessKit）时，上面那套「套用宿主机 uid」的做法会失效，而且失败现象和权限完全正常时一模一样。原因：容器里的 uid **N** 落到宿主机上不是 N，而是 subuid 区间里的 `100000 + (N - 1)`（典型值 100999）。宿主机目录明明是 `zephyr:zephyr 775`、`id -u` 也确实等于 1000，容器以 `user: "1000:1000"` 运行仍会 `permission denied` —— 对 100999 来说那个目录只剩 others 的 `r-x`。

```bash
grep "^$(id -un):" /etc/subuid /etc/subgid        # 确认映射起点，通常是 100000
docker info --format '{{json .SecurityOptions}}'  # 含 name=rootless 即确认是 rootless
```

三种做法，按推荐顺序：

1. **用命名卷、不写 `user:`**（默认形态）：卷在 rootless 的存储里，由镜像内 `/app/data` 的属主初始化，完全绕开宿主 uid。
2. **bind mount 时写 `user: "0:0"`**：rootless 下容器的 root 就是**宿主的登录用户**，既能写 `./data`，容器创建的文件在宿主机上也正好属于自己，`ls` / 备份 / `chown` 都符合直觉；隔离仍由 user namespace 保证，不等于拿到了宿主机 root。
3. `chown` 到映射后的 uid（`sudo chown -R 100999:100999 data`）：能用但脆弱，换机器、改 `/etc/subuid`、迁目录后都会再次失效，不推荐。

> 与上节不冲突：**别把命名卷和 `user: "0:0"` / `1000:1000` 混着用**。命名卷是用镜像内 `/app/data` 的属主（`app`，Alpine 的 `adduser -S` 从 100 起分配）初始化的，套一个不匹配的 uid 同样会在 `jsonfile` / SQLite 的启动预检处失败。

rootless 还有两点差异：容器绑不了 1024 以下端口（默认 8080 不受影响）；`restart: unless-stopped` 要真正跨宿主重启生效，需要把守护进程交给 systemd 用户实例（`systemctl --user enable --now docker`）。

---

[← 返回主文档](../README.md) · [English](en/docker.md)
