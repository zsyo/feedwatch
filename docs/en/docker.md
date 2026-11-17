# Docker Deployment (run in WSL)

> [← Back to main README](../../README_EN.md) · [Documentation index](../../README_EN.md#toc) · [简体中文](../docker.md)

<a id="docker"></a>

```bash
cp config.example.yaml config.yaml   # local private config, bind-mounted read-only into the container
cp .env.example .env                 # fill in real secrets: email / the channels you enabled
docker compose build
docker compose up -d
docker compose logs -f    # first run should show the baseline notification pushed to every channel
docker compose restart    # should be completely silent when there are no new versions
```

`docker-compose.yml` pins the container name to `feedwatch` with `container_name`, so `docker logs -f feedwatch` and `docker exec -it feedwatch sh` also work; the `docker compose ...` commands above use the service name and are unaffected. Setting `container_name` means the service cannot be scaled horizontally, but this project shares a single state file and should run only one instance anyway, so there is no impact. The volume is still `feedwatch-data`; renaming and recreating the container will not lose `state.json` or resend the baseline notification.

The image is a multi-stage build (`golang:1.27-alpine` → `alpine:3.21`): static binary, non-root user, CA certificates and timezone data included; the build stage cross-compiles with BuildKit's `TARGETOS` / `TARGETARCH`, so one Dockerfile produces both amd64 and arm64 images. Config is passed in via a read-only mount, channel credentials are injected from `.env` through `docker-compose.yml`'s `environment` (add a line there when you introduce a custom variable), and state is persisted in the named volume `feedwatch-data:/app/data`. The `user: "${FW_UID:-app}:${FW_GID:-app}"` in compose defaults to the image's `app` user (matching the named volume's owner); host uid/gid overrides are only needed for the bind mount described below.

> Baked into the image are the **placeholder defaults** from `config.example.yaml` (no real domain or recipients), so a clean clone can run `docker compose build` directly; `.dockerignore` blocks the local `config.yaml` from entering image layers. A plain `docker run` without mounts uses that placeholder config — mount the real one with `-v`.

> **Losing the volume resends the baseline email** (the program thinks it is the first run). This is expected behavior. For a manual `docker run`, remember `-e RESEND_API_KEY=... -v feedwatch-data:/app/data`.

<a id="bind-mount"></a>

## Replacing the State Volume with a Host Directory

`- feedwatch-data:/app/data` in `docker-compose.yml` is a **named volume**: the left-hand side is a volume name, not a path. Docker keeps the data itself (on Linux at `/var/lib/docker/volumes/<project>_feedwatch-data/_data`; inside the VM disk for Docker Desktop / WSL), so there is no ordinary folder visible on the host; the `volumes: feedwatch-data:` at the end of the file merely declares that the volume exists. `docker compose down` does not delete volumes by default — only `down -v` does.

To put state in `data/` next to the compose file, replace that line with a bind mount; **the container path `/app/data` and `FEEDWATCH_STATE_PATH` stay unchanged**:

```yaml
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./data:/app/data          # replaces - feedwatch-data:/app/data
# the top-level volumes: block can be deleted entirely
```

**A bind mount preserves the host directory's owner**: `chown -R app:app /app` in the `Dockerfile` only affects the image, not a mounted directory. State writes use `CreateTemp` + `rename`, which requires **creating files** in the directory, so it must be writable by the container user. If the directory is `drwxr-xr-x docker:docker`, the container's `app` falls into "others" with just `r-x`, giving `permission denied` → state can never be saved → every round is treated as the first and **the baseline notification is resent** (reproducible on Linux servers; WSL + Docker Desktop file sharing is usually more permissive).

The recommended approach is to **leave host permissions alone** and run the container as the directory's owner. `docker-compose.yml` already sets `user: "${FW_UID:-app}:${FW_GID:-app}"`; just pass the host uid/gid in:

First confirm the directory owner is the current login user: `stat -c '%u:%g' data` should match `id -u`:`id -g`. If the owner is another user (e.g. `docker:docker`), put **that pair of numbers** into `.env` as `FW_UID` / `FW_GID`, or `chown` the directory to the current user first — `$(id -u)` is the person running the command, not the directory owner, and a mismatch still yields `permission denied`.

```bash
mkdir -p data

# Option 1 (recommended): write into .env so every terminal runs compose consistently
printf 'FW_UID=%s\nFW_GID=%s\n' "$(id -u)" "$(id -g)" >> .env
docker compose up -d

# Option 2: applies to this command only (the shell expands $(...) before passing env vars to compose)
FW_UID=$(id -u) FW_GID=$(id -g) docker compose up -d
```

> **Compose does not execute `$(...)`**: it only interpolates `${VAR}`, so writing `user: "$(id -u):$(id -g)"` into YAML is treated as a literal string and fails container startup. `.env` must therefore contain the **expanded numbers** — that is what the `printf` above does. Also, do not use the variable name `UID`: in bash/zsh `UID` is a read-only shell variable, so `UID=$(id -u)` fails with `readonly variable`; hence `FW_UID` / `FW_GID`. Both are used only on the host side for Compose interpolation and are **not** passed into the container.
>
> With neither set, `user` is `app:app`, equivalent to the image's `USER app` and matching the default named volume's owner, so the default named-volume deployment is entirely unaffected.

Two alternatives: keep the default user and grant the directory permission (`sudo chown <uid>:<gid of app inside the image> data`, or `chmod 777 data` — writable by everyone, not recommended); or hard-code `user` as specific numbers in compose. For a custom compose file, likewise just add `user: "1000:1000"` under the service (replace with the real host values; **not applicable in rootless mode, see below**).

**A misconfiguration is caught at startup**: `jsonfile.New` probes the state directory once with `CreateTemp` and deletes it immediately when creating the store; if it is not writable, startup fails outright (`state directory ... is not writable`) instead of sending the baseline email and only then discovering state cannot be saved. What you see is therefore a startup error plus restart loops, not a duplicate notification every half hour.

`data/` is already in `.gitignore` and will not enter the repo.

**Switching starts from an empty state**: the new directory has no `state.json`, so the program treats it as a first run and resends one baseline notification. To keep the seen set, copy the state out of the named volume first (real volume names usually carry a project prefix — check with `docker volume ls | grep feedwatch`):

```bash
docker run --rm -v feedwatch_feedwatch-data:/src -v "$PWD/data:/dst" alpine:3.21 \
  cp /src/state.json /dst/state.json
```

To keep a named volume but pin its host location, you can also add `driver_opts: {type: none, o: bind, device: /absolute/path/data}` to the top-level volume, but `./data` directly is simpler for everyday use.

<a id="rootless-docker"></a>

## uid Mapping under Rootless Docker

When Docker runs in rootless mode (RootlessKit), the "apply the host uid" approach above stops working — and the failure looks exactly like a case where permissions are perfectly fine. Reason: uid **N** inside the container is not N on the host, but `100000 + (N - 1)` in the subuid range (typically 100999). The host directory really is `zephyr:zephyr 775` and `id -u` really is 1000, yet running the container as `user: "1000:1000"` still gives `permission denied` — for uid 100999 that directory has only others' `r-x`.

```bash
grep "^$(id -un):" /etc/subuid /etc/subgid        # confirm the mapping start, usually 100000
docker info --format '{{json .SecurityOptions}}'  # name=rootless confirms rootless mode
```

Three options, in recommended order:

1. **Use a named volume, no `user:`** (the default shape): the volume lives in rootless storage and is initialized from the owner of `/app/data` inside the image, bypassing host uids entirely.
2. **With a bind mount, write `user: "0:0"`**: under rootless, the container's root **is the host login user**, so it can write `./data` and files it creates on the host belong to you — `ls` / backup / `chown` all behave intuitively. Isolation is still guaranteed by the user namespace; this is not host root.
3. `chown` to the mapped uid (`sudo chown -R 100999:100999 data`): works but is brittle — changing machines, editing `/etc/subuid` or moving the directory breaks it again. Not recommended.

> No conflict with the previous section: **do not mix a named volume with `user: "0:0"` / `1000:1000`**. A named volume is initialized from the owner of `/app/data` inside the image (`app`, allocated from 100 by Alpine's `adduser -S`); applying a mismatched uid fails the same `jsonfile` / SQLite startup probe.

Two more rootless differences: containers cannot bind ports below 1024 (the default 8080 is unaffected); for `restart: unless-stopped` to actually survive host reboots, hand the daemon to the systemd user instance (`systemctl --user enable --now docker`).

---

[← Back to main README](../../README_EN.md) · [简体中文](../docker.md)
