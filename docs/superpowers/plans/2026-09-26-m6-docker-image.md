# M6 之二：hub 的 Docker 镜像 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 发布 `ghcr.io/xjetry/probe-hub:<version>`（linux/amd64、linux/arm64，`FROM scratch`）：`make docker` 在本地构建、逐条目核对并冒烟；推 tag 时 release 流水线在建 GitHub Release 之前推送并回读镜像；CI 每次构建并冒烟。

**Architecture:** 构建逻辑只在 Makefile。hub 二进制经与 `release` 共用的 `hub_build` 构建到 `build/image/linux/<arch>/probe-hub`，Dockerfile 只按 `TARGETARCH` COPY；CA 证书与账户文件来自固定在构建节点本机平台运行的 alpine 阶段，最终阶段没有 RUN。构建节点是 Makefile 创建的、按 digest 固定的 BuildKit（docker-container 驱动），本地、CI、发布用同一个。两个平台的根文件系统以 tar 导出交给新增的 `scripts/checkimage` 逐条目核对，本机平台装进 docker 后由 `scripts/docker-smoke.sh` 冒烟；`make docker-push` 在这些之后推送，`scripts/docker-readback.sh` 回读通过后才 `gh release create`。预发布判定只在 Makefile 的 `RELEASE_CHANNEL`，GitHub Release 的 prerelease 与镜像的 `latest` 都读它。

**Tech Stack:** Dockerfile（BuildKit 内置前端）、docker buildx（docker-container 驱动）、GNU Make、POSIX sh、Go（archive/tar、debug/elf、encoding/pem）、GitHub Actions、ghcr.io。

**Spec:** docs/superpowers/specs/2026-09-17-probe-architecture-design.md（提交 c381a27）：§14 的"hub Docker 镜像"与"发布"两条、§12 的"Docker 镜像"一条、§5.4、§5.3。

**工作树:** `/Users/xjetry/work/vibe/probe-docker`，分支 `m5m6-docker`，基点 main `c381a27`。本机 docker 由 OrbStack 提供（Docker 29.4.0、buildx v0.33.0、arm64），能拉 Docker Hub 与 ghcr。

## 实验依据

2026-09-26 在本机（OrbStack，Docker 29.4.0，经典 overlay2 镜像存储，buildx v0.33.0，arm64）用最小程序与当时 main 的 hub 实测。代码与注释只引用 spec 章节，不引用本段；需要写进注释的因果都已在这里实测过，写法见各任务。

1. docker 驱动（OrbStack 默认构建器）多平台构建报 `Multi-platform build is not supported for the docker driver`；docker-container 驱动（`moby/buildkit:v0.33.0`）下多平台 `--load` 报 `docker exporter does not currently support exporting manifest lists`。多平台 `--output type=tar,dest=…` 可用：tar 里每个平台一个 `linux_<arch>/` 目录，是该平台镜像的完整文件树（不含 `/dev`、`/proc` 等运行时注入项），保留属主与 1777 权限；带 `-t` 也被接受。
2. `FROM --platform=$BUILDPLATFORM alpine…` 阶段在构建日志里只以 `linux/arm64`（构建节点本机）执行一次 RUN；`linux/amd64` 只有 COPY。最终阶段没有 RUN 时，多平台构建不在目标架构上执行任何程序。
3. `COPY --from=<阶段>` 保留源阶段的属主（`/data` 为 65532:65532）；来自构建上下文的文件归 0:0。
4. 空的命名卷挂到镜像的 `/data` 时，卷根的属主变为镜像里 `/data` 的 65532——此前被 alpine 容器 `chown 0:0` 过、但仍为空的卷也一样；卷里已有文件时 Docker 不改属主。匿名卷（`VOLUME /data`、不给 `-v`）以 65532 可写。
5. scratch 里没有 `/tmp` 时，`modernc.org/sqlite v1.59.0` 在 30 万行、`cache_size=16` 下的 `CREATE INDEX`、`CREATE TEMP TABLE … AS SELECT`、带 `ORDER BY` 的子查询都报 `disk I/O error (6410)`（SQLITE_IOERR_GETTEMPPATH）；小数据量的临时表与 `VACUUM` 不触发（页缓存装得下）。镜像里加上 1777 的 `/tmp` 后全部成功。
6. `docker exec` 不带 `-i`：容器里进程的 stdin 是字符设备（`/dev/null`），立即 EOF，宿主一侧的管道不会接进去；带 `-i` 时是管道。现有 hub 在容器里 `passwd` 不带 `-i` 报 `error: password must be at least 12 characters`。
7. 现有 hub 在 `/data` 不可写（卷里有 root 文件、卷根 root 0755）时以 1 退出，只报 `error: unable to open database file (14)`，不含路径。
8. scratch 镜像没有 `PATH` 时容器得到默认 `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`；二进制在 `/usr/local/bin` 时 `docker exec <容器> probe-hub …` 按名字可执行，放在 `/` 下则不在 PATH 里。
9. `docker run <镜像> --timezone X` 替换整组默认 CMD，hub 打印 usage 以 2 退出；`-e TZ=Asia/Shanghai` 生效（启动行 `timezone=Asia/Shanghai`，无回退告警）；不给时启动日志有 `host time zone could not be resolved; using UTC; set --timezone explicitly` 与 `listening on a non-loopback address…`。
10. 容器里的 hub：`/admin` 307 到 `/admin/`；`/admin/` 200 且含 `id="root"`；面板没有构建进二进制时 `/admin/` 是 503 说明页（`internal/hub/web/web.go` 的 `notBuilt`）。`Login` 错误密码 401，正确密码 200 并下发 `probe_session` cookie。`probe-hub stats` 每行 `<表>: <行数>`，空库有 `admin: 0`。
11. 空 `DOCKER_CONFIG` 的匿名 `docker manifest inspect` 对公开的 ghcr 镜像返回 0，对不存在的 tag 返回 1（`manifest unknown`）。`docker buildx imagetools inspect --format '{{.Manifest.Digest}}'` 打印索引 digest（末尾无换行），tag 不存在时返回 1。
12. 冷缓存下 `-trimpath` 构建 hub：linux/amd64 8 s、linux/arm64 6 s（16 核 Mac）。建 docker-container 构建器（拉 BuildKit）7 s；两个平台导出 tar 8 s；本机平台 `--load` 6 s。
13. `alpine:3.21` 基础镜像自带 `ca-certificates-bundle-20260909-r0`（`/etc/ssl/certs/ca-certificates.crt` 属于它），`apk add --no-cache --upgrade ca-certificates-bundle` 可用；证书包 121 张。
14. 固定值（2026-09-26 查得）：`alpine:3.21` 索引 digest `sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507`；`moby/buildkit:v0.33.0` 索引 digest `sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3`；`tonistiigi/binfmt:qemu-v10.2.3` digest `sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0`。`docker/setup-qemu-action` v4 的输入为 `image`、`platforms`、`reset`、`cache-image`；`docker/login-action` v4 为 `registry`、`username`、`password`、`logout` 等。
15. 本计划的代码块在写计划时实跑过（仓库外的临时目录，未改工作树）：Task 2 的 checkimage 与其测试 gofmt、vet 干净，测试通过；Task 4 的 Dockerfile 与 `.dockerignore` 以当时 main 的 hub 二进制做两平台 tar 导出，checkimage 无问题行；Task 5 的冒烟脚本在 Task 1 尚未落地时红在 `passwd without -i did not report the missing input`（打印 `password must be at least 12 characters`），把两处报错文字临时换成现状的写法后全段通过——本机 arm64 一次、`SMOKE_PLATFORM=linux/amd64` 一次（10 s）；Task 6 的回读脚本 `latest` 子命令在包不存在时打印 `absent`，报错留在 stderr；两个脚本 shellcheck 0.11.0 无告警。

## 对任务说明的取舍

任务说明没有写死、或与 spec 口径有出入的地方，按下列理由定：

1. **二进制放 `/usr/local/bin/probe-hub`，不放 `/probe-hub`。** §14 写的是 `docker exec … probe-hub passwd --db /data/probe.db`，按名字执行；`/` 不在容器默认 PATH 里（实验 8）。`ENTRYPOINT ["/usr/local/bin/probe-hub"]`。
2. **不用 `docker/setup-buildx-action` 与 `docker/build-push-action`。** §14："构建逻辑只在 Makefile 一处，本地验收与线上发布用的是同一套产物"。镜像的构建参数（构建器、Dockerfile、上下文、平台）写在 Makefile 的 `docker_build`，本地 `make docker` 与发布的 `make docker-push` 共用；构建器由 Makefile 按 digest 固定创建，发布与本地是同一版本的 BuildKit。`setup-buildx-action` 默认用浮动的 `buildx-stable-1`，`build-push-action` 会在 workflow 里另写一份平台与 tag。保留 `docker/setup-qemu-action`（仅供回读时运行 arm64 镜像）与 `docker/login-action`，按提交 SHA 固定。
3. **`/admin/` 只接受 200 且页面含 `id="root"`，不接受 503。** 503 正是 `internal/hub/web` 的"面板没有构建进二进制"说明页（实验 10）；接受它，漏了 `make web` 的镜像也会冒烟通过。"没起来"单独区分：容器已退出、到上限仍无 HTTP 应答、有应答但不是面板，三种分开报。
4. **镜像构建上下文放 `build/image/`，不放 `dist/` 下。** release.yml 以 `gh release create … dist/*` 整体上传 `dist/` 顶层，而镜像在 `make release` 之后、`gh release create` 之前构建；目录混进 `dist/` 就会被 `dist/*` 展开进上传参数。放在 `dist/` 之外，两个目标之间就没有"谁先清理"的顺序依赖。
5. **`make docker` 用两个输出：多平台 `--output type=tar`（导出根文件系统交给 checkimage），加本机平台 `--load`（供冒烟运行）；不用 `--output type=oci`。** 多平台 `--load` 在经典镜像存储上不可用（实验 1），而是否启用 containerd 存储是宿主配置；OCI 归档冒烟无法直接运行，tar 却能让 checkimage 同时核对两个平台。
6. **镜像先于 GitHub Release 发布。** `gh release create` 是最后一步：之前任何一步失败，原样重跑 job 即可（同一 tag 重推即覆盖）；反过来，Release 已建而镜像失败时，重跑会停在"release 已存在"。推送之前先跑完 `make release` 与 `make docker`（核对、冒烟），构建失败时什么都没有发布。
7. **带构建元数据（`+`）的版本不能发布镜像，`make docker`/`docker-push` 在构建之前拒绝。** Docker tag 只允许 `[A-Za-z0-9_.-]`；镜像 tag 与版本号逐字相同（`docker run … version` 打印的就是 tag）。这使 `v1.2.3+build` 这类 tag 在发布流水线里整体失败（推送之前，什么都不发布）。见文末"spec 待同步"。
8. **预发布判定下沉到 Makefile 的 `RELEASE_CHANNEL`。** release.yml 里原有的 `case "${GITHUB_REF_NAME%%+*}"` 移走，GitHub Release 的 `--prerelease` 与镜像是否推 `latest` 都经 `make -s release-channel` 读同一个判定。
9. **新增、任务说明未列的：** hub 侧两条报错（Task 1，Review Focus 第 1、2 条的期望行为需要它）；`scripts/checkimage`（Task 2，§14 "只含"的逐条目核对，并照到 `/tmp`、`/data` 属主与二进制架构）；推送后的回读（Task 6，版本号、匿名可取、`latest` 指向）。

## Global Constraints

- hub Docker 镜像（§14 原文）：`ghcr.io/xjetry/probe-hub:<version>`，预发布不打 `latest`；`FROM scratch`，只含静态二进制、CA 证书（通知出站 HTTPS 要用）与非 root 用户；时区数据已嵌入二进制。数据卷 `/data`，默认参数 `serve --db /data/probe.db --listen 0.0.0.0:8080`；容器里监听非 loopback 是预期的，启动告警照旧，反代与 `--trusted-proxies` 由部署者配。管理员密码经 `docker exec … probe-hub passwd --db /data/probe.db` 设置。release 流水线用 buildx 发布 amd64 与 arm64；`make docker` 在本地构建并冒烟（起容器、`/admin` 有应答、`passwd` 可执行）。
- 构建逻辑只在 Makefile 一处，本地验收与线上发布用的是同一套产物；推送 `v*` tag 时 CI 调用同一目标（§14）。镜像里不编译 Go。
- 全部 `CGO_ENABLED=0`；静态门禁 `scripts/checkstatic`，发布流水线必须把全部 Linux 产物交给它——镜像里的 hub 二进制同样在产出时过门禁（§14）。
- tag 去掉构建元数据（`+` 及之后）后仍含 `-` 即预发布：GitHub Release 建为 prerelease、不成为 latest（§14）；镜像不推 `latest`。
- hub 只监听明文 HTTP；`--listen` 非 loopback 时启动告警；`--timezone` 取不到本地时区名时退回 UTC 并告警；`--trusted-proxies` 空列表 = 不信任任何转发头（§5.4）。管理员密码只在 hub 主机上经 `probe-hub passwd` 设置，没有经网络的首次设置页（§5.3）。
- §12：Docker 镜像构建后起容器，`/admin` 有应答，`docker exec` 能执行 `passwd`；每条新断言做一次缺陷注入，确认它红且红在正确的原因上。
- Actions 按提交 SHA 固定，行尾注释主版本（沿用 ci.yml 的 `@<40 位 SHA> # v4` 写法）。镜像与构建器按 digest 固定（实验 14 的三个值）。Dockerfile 不写 `# syntax=` 行：用所固定 BuildKit 内置的 Dockerfile 前端，不在构建时另拉浮动的前端镜像。
- Docker 资源只用本计划的前缀：容器与卷 `probe-smoke-*`、`probe-exp-*`、`probe-readme-*`，网络 `probe-readme-net`，镜像 tag `ghcr.io/xjetry/probe-hub:v0.0.0-*` 与 `probe-hub-notmp:exp`，构建器 `probe-hub-buildkit-*`。已有的 `weir-multi`、`orbstack`、`default` 构建器与任何 OrbStack 机器都不动。不推送任何镜像、不创建 GitHub 仓库、不推 tag。
- 判成败的命令不接管道：`cd /Users/xjetry/work/vibe/probe-docker && cmd > /tmp/<独特名>.log 2>&1; echo $?`，再看日志。Go 测试一律 `go test -count=1`。不用 `sed -i ''`；注入用编辑工具改文件，改前 `git add` 建基准，改后 `git diff --stat` 必须列出该文件，验完 `git checkout -- <文件>` 还原并复跑。
- 项目规则（代码注释与提交信息同样适用）：注释写 WHY 与不变式并指明由谁保证，不复述代码；不写过程信息（任务号、轮次、方案代号、审阅引用、"按上一轮"）；不打补丁、不加兼容层；同形状的问题一次改齐；不变式靠显式检查承载。实现者不改 `docs/`，不派子代理。

## Review Focus

1. **把属主为 root 的宿主目录绑定到 `/data`**（`-v /srv/probe:/data`，最常见的部署写法）：hub 以 uid 65532 运行，写不了库。期望：容器立即以 1 退出，报错写出 `/data/probe.db`；新建的命名卷与匿名卷则开箱可写。测试：Task 1 `TestOpenErrorNamesTheDatabasePath`；Task 5 冒烟的"库目录不可写"段（预置 root 属主的非空卷）与"卷属主"段（新卷里的库文件属 65532:65532）；注入见 Task 5 Step 6 的 (c)(d)。
2. **照 §14 的写法 `docker exec … probe-hub passwd` 却没带 `-i`**：容器里 stdin 是 `/dev/null`，现状报"密码至少 12 个字符"（实验 6）。期望：非零退出，报 `no password on stdin` 并写明 `-i`/`-it`，管理员表不变；`probe-hub` 按名字可执行。测试：Task 1 的两个单测；Task 5 冒烟的 exec 段；注入见 Task 5 Step 6 的 (b)(e)。
3. **scratch 里没有 `/tmp`**：SQLite 的排序溢出、临时表、建索引报 `disk I/O error (6410)`（实验 5），平时小查询不触发，数据量上来或迁移重建索引时才炸。期望：镜像里的 hub 与宿主上一样能跑大查询与迁移。测试：Task 2 checkimage 的 `tmp` 条目（1777 且带 sticky）；Task 4 Step 7 的一次性 SQLite 实验与注入（删掉 `/tmp` 那一行 → checkimage 红）。
4. **`docker run -p 8080:8080` 把监听 0.0.0.0 的 hub 暴露在所有网卡上**：期望：启动日志照旧告警；README 只示范发布到 127.0.0.1 或与反代同网络，并讲清 `--trusted-proxies`。测试：Task 5 冒烟断言告警，冒烟自己只发布到 127.0.0.1；注入见 Task 5 Step 6 的 (f)；README 的同网络写法在 Task 7 Step 4 实跑。
5. **镜像里没有 shell，出问题无从下手**：期望：README 给出不依赖镜像内 shell 的办法（日志、经 exec 的 `probe-hub version`/`stats`、挂同一卷的工具容器、`--network container:`）。测试：Task 5 冒烟逐条执行这些办法；注入见 Task 5 Step 6 的 (g)；README 原文在 Task 7 Step 4 实跑。

---

### Task 1: hub：passwd 没收到输入时单独报错，打不开库时报出库路径

**Files:**
- Modify: `cmd/hub/passwd.go`（`readPassword` 的非终端分支；新增 `errNoPasswordInput`）
- Modify: `cmd/hub/passwd_test.go`
- Modify: `internal/hub/store/store.go`（`Open` 里 `migrate` 失败时的返回）
- Modify: `internal/hub/store/store_test.go`

**Interfaces:**
- Produces：`errNoPasswordInput`（`cmd/hub` 包内的 `error` 变量），文本以 `no password on stdin` 开头——Task 5 的冒烟按这段文字断言。`store.Open` 在库打不开时返回的错误以 `open database <path>: ` 开头，`%w` 包住原错误——Task 5 的冒烟按 `open database /data/probe.db` 断言。

- [ ] **Step 1: 写失败测试**

`cmd/hub/passwd_test.go` 末尾追加（该文件已导入 `errors`、`io`、`os`、`path/filepath`，不需要新导入）：

```go
// docker exec 不带 -i 时容器里的 stdin 就是 /dev/null：读到立即 EOF，这不是"密码太短"。
func TestReadPasswordFromDevNullIsMissingInput(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	if _, err := readPassword(devnull, io.Discard); !errors.Is(err, errNoPasswordInput) {
		t.Fatalf("stdin /dev/null: err = %v, want errNoPasswordInput", err)
	}
}

// 空行是输入了一个空密码，由 SetPassword 按长度拒绝，不归入"没有输入"。
func TestReadPasswordEmptyLineIsAnEmptyPassword(t *testing.T) {
	got, err := readPassword(pipeWith(t, "\n"), io.Discard)
	if err != nil || got != "" {
		t.Fatalf("empty line: password=%q err=%v, want an empty password and no error", got, err)
	}
}

func TestPasswdWithoutInputLeavesNoDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "t.db")
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	if err := runPasswdWith([]string{"--db", db}, devnull, io.Discard); !errors.Is(err, errNoPasswordInput) {
		t.Fatalf("err = %v, want errNoPasswordInput", err)
	}
	if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passwd without input touched %s (stat err = %v)", db, err)
	}
}
```

`internal/hub/store/store_test.go`：import 块补 `"os"` 与 `"strings"`，末尾追加：

```go
// SQLite 打开失败的报错不带文件名；容器里 /data 不可写时，路径是报错里唯一能指向原因的线索。
func TestOpenErrorNamesTheDatabasePath(t *testing.T) {
	// 父路径是普通文件：谁来运行测试都打不开，失败不依赖权限位。
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "t.db")
	s, err := Open(path, clock.NewFake(time.Unix(0, 0)), slog.Default())
	if err == nil {
		s.Close()
		t.Fatalf("Open(%s) succeeded", path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("Open(%s) = %q, want the path in the error", path, err)
	}
}
```

- [ ] **Step 2: 跑红**

```sh
cd /Users/xjetry/work/vibe/probe-docker && go test -count=1 -run 'TestReadPassword|TestPasswd' ./cmd/hub > /tmp/pd-t1-passwd-red.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && go test -count=1 -run TestOpenErrorNamesTheDatabasePath ./internal/hub/store > /tmp/pd-t1-store-red.log 2>&1; echo $?
```

Expected：两条都是 1。第一份日志含 `undefined: errNoPasswordInput`；第二份含 `want the path in the error` 与 `unable to open database file`。

- [ ] **Step 3: 实现**

`cmd/hub/passwd.go`：在 `readPassword` 之前加

```go
// errNoPasswordInput：stdin 不是终端，且在读到任何字节之前就结束了。docker exec 不带 -i 时，
// 容器里的 stdin 是 /dev/null，宿主上的管道根本没有接进来；这时报"密码太短"会让人去查一个
// 从来没输入过的密码，所以单独报，并写明该加的参数。
var errNoPasswordInput = errors.New("no password on stdin: it is not a terminal and ended before any input (with docker exec, add -i to pipe the password in, or -it to type it)")
```

`readPassword` 的非终端分支改为：

```go
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		// ReadString 只在 EOF 之前一个字节都没读到时返回空串；空行至少带着 '\n'，
		// 那是输入了一个空密码，留给 SetPassword 按长度拒绝。
		if line == "" {
			return "", errNoPasswordInput
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
```

`internal/hub/store/store.go` 的 `Open`：

```go
	if err := migrate(w); err != nil {
		w.Close()
		// SQLite 打开失败的报错不带文件名（如 unable to open database file (14)）；serve 与离线子命令
		// 都经这里打开库，在这一层补上路径，报错才指得出是哪个文件、该查哪个目录的权限。
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
```

- [ ] **Step 4: 跑绿**

```sh
cd /Users/xjetry/work/vibe/probe-docker && go test -count=1 ./cmd/hub ./internal/hub/store > /tmp/pd-t1-green.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 5: 缺陷注入**

先 `cd /Users/xjetry/work/vibe/probe-docker && git add cmd/hub/passwd.go cmd/hub/passwd_test.go internal/hub/store/store.go internal/hub/store/store_test.go` 建基准。

(a) 删掉 `readPassword` 里 `if line == "" { return "", errNoPasswordInput }` 连同其上两行注释。`git diff --stat` 列出 `cmd/hub/passwd.go`。

```sh
cd /Users/xjetry/work/vibe/probe-docker && go test -count=1 -run 'TestReadPassword|TestPasswd' ./cmd/hub > /tmp/pd-t1-inject-a.log 2>&1; echo $?
```

Expected：1。`TestReadPasswordFromDevNullIsMissingInput` 失败于 `err = <nil>, want errNoPasswordInput`；`TestPasswdWithoutInputLeavesNoDatabase` 失败于 `password must be at least 12 characters`；`TestReadPasswordEmptyLineIsAnEmptyPassword` 仍通过。`git checkout -- cmd/hub/passwd.go`，复跑期望 0。

(b) 把 `Open` 里的 `return nil, fmt.Errorf("open database %s: %w", path, err)` 改回 `return nil, err`。`git diff --stat` 列出 `internal/hub/store/store.go`。

```sh
cd /Users/xjetry/work/vibe/probe-docker && go test -count=1 -run 'TestOpenErrorNamesTheDatabasePath|TestOpenRefusesNewerSchema' ./internal/hub/store > /tmp/pd-t1-inject-b.log 2>&1; echo $?
```

Expected：1，只有 `TestOpenErrorNamesTheDatabasePath` 失败，日志含 `want the path in the error`。`git checkout -- internal/hub/store/store.go`，复跑期望 0。

- [ ] **Step 6: 全量测试**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make test > /tmp/pd-t1-all.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 7: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-docker && git add cmd/hub/passwd.go cmd/hub/passwd_test.go internal/hub/store/store.go internal/hub/store/store_test.go && git commit -m "hub: passwd 没收到输入时单独报错，打不开库时报出库路径" -m "docker exec 不带 -i 时容器里的 stdin 是 /dev/null，passwd 读到立即 EOF，原先报成密码不足 12 个字符。库目录不可写时 SQLite 只报 unable to open database file (14)，不带路径。两处都在读入与打开的那一层补齐，serve 与离线子命令同一口径。"
```

---

### Task 2: scripts/checkimage：逐条目核对镜像的根文件系统

**Files:**
- Create: `scripts/checkimage/main.go`
- Create: `scripts/checkimage/main_test.go`

**Interfaces:**
- Produces：`go run ./scripts/checkimage <rootfs.tar> <arch>...`。输入是 buildx 多平台 `--output type=tar` 的结果（每个平台一个 `linux_<arch>/`）。全部符合时退出 0、无输出；否则每条问题一行 `<平台>/<路径>: <原因>`，退出 1；参数少于两个时退出 2。架构只认 `amd64`、`arm64`（`machines` 表）。Task 4 的 `make docker` 调用它。镜像布局的约定（Task 4 的 Dockerfile 必须产出）：

| 路径 | 类型与权限 | 属主 |
|---|---|---|
| `data` | 目录 0755 | 65532:65532 |
| `etc`、`etc/ssl`、`etc/ssl/certs`、`usr`、`usr/local`、`usr/local/bin` | 目录 0755 | 0:0 |
| `etc/passwd`、`etc/group`、`etc/ssl/certs/ca-certificates.crt` | 文件 0644 | 0:0 |
| `tmp` | 目录 1777（sticky） | 0:0 |
| `usr/local/bin/probe-hub` | 文件 0755，ELF 机器类型与平台一致 | 0:0 |

`etc/passwd` 恰一行、七列，uid 与 gid 为 65532；`etc/group` 恰一行、四列，gid 为 65532；证书包至少 100 个 `CERTIFICATE` 块。

- [ ] **Step 1: 写失败测试**

`scripts/checkimage/main_test.go`：

```go
package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type file struct {
	name     string
	typ      byte
	mode     int64
	uid, gid int
	body     []byte
}

// fakeELF 只有 64 字节的 ELF 文件头：checkimage 只看机器类型，合成夹具不依赖交叉工具链。
func fakeELF(m elf.Machine) []byte {
	var buf bytes.Buffer
	h := elf.Header64{
		Ident:     [elf.EI_NIDENT]byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)},
		Type:      uint16(elf.ET_EXEC),
		Machine:   uint16(m),
		Version:   uint32(elf.EV_CURRENT),
		Ehsize:    64,
		Phentsize: 56,
		Shentsize: 64,
	}
	if err := binary.Write(&buf, binary.LittleEndian, h); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// caBundle 由 n 个 CERTIFICATE 块组成；checkimage 只数块，不解析证书内容。
func caBundle(n int) []byte {
	return bytes.Repeat(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not parsed")}), n)
}

func dir(name string, mode int64, owner int) file {
	return file{name: name, typ: tar.TypeDir, mode: mode, uid: owner, gid: owner}
}

func reg(name string, mode int64, body []byte) file {
	return file{name: name, typ: tar.TypeReg, mode: mode, body: body}
}

// expectedLayout 与 Dockerfile 产出的根文件系统一致，按 buildx 的 tar 输出分平台目录。
func expectedLayout(arches ...string) []file {
	var files []file
	for _, a := range arches {
		p := "linux_" + a + "/"
		files = append(files,
			dir(p, 0o755, 0),
			dir(p+"data/", 0o755, runUID),
			dir(p+"etc/", 0o755, 0),
			reg(p+"etc/group", 0o644, []byte("probe-hub:x:65532:\n")),
			reg(p+"etc/passwd", 0o644, []byte("probe-hub:x:65532:65532:probe-hub:/nonexistent:/sbin/nologin\n")),
			dir(p+"etc/ssl/", 0o755, 0),
			dir(p+"etc/ssl/certs/", 0o755, 0),
			reg(p+"etc/ssl/certs/ca-certificates.crt", 0o644, caBundle(minCACerts)),
			dir(p+"tmp/", 0o1777, 0),
			dir(p+"usr/", 0o755, 0),
			dir(p+"usr/local/", 0o755, 0),
			dir(p+"usr/local/bin/", 0o755, 0),
			reg(p+"usr/local/bin/probe-hub", 0o755, fakeELF(machines[a])),
		)
	}
	return files
}

func tarOf(t *testing.T, files []file) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Typeflag: f.typ, Mode: f.mode, Uid: f.uid, Gid: f.gid, Size: int64(len(f.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func edit(name string, change func(*file)) func([]file) []file {
	return func(files []file) []file {
		for i := range files {
			if files[i].name == name {
				change(&files[i])
			}
		}
		return files
	}
}

func without(prefix string) func([]file) []file {
	return func(files []file) []file {
		var kept []file
		for _, f := range files {
			if !strings.HasPrefix(f.name, prefix) {
				kept = append(kept, f)
			}
		}
		return kept
	}
}

func with(extra file) func([]file) []file {
	return func(files []file) []file { return append(files, extra) }
}

func TestAcceptsTheExpectedLayout(t *testing.T) {
	if problems := check(tarOf(t, expectedLayout("amd64", "arm64")), []string{"amd64", "arm64"}); len(problems) != 0 {
		t.Fatalf("problems on the expected layout: %q", problems)
	}
}

func TestRejectsEachDeviation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]file) []file
		want   string
	}{
		{"tmp without the sticky bit", edit("linux_amd64/tmp/", func(f *file) { f.mode = 0o777 }), "linux_amd64/tmp: mode drwxrwxrwx, want dtrwxrwxrwx"},
		{"no tmp", without("linux_arm64/tmp/"), "linux_arm64/tmp: missing"},
		{"data owned by root", edit("linux_amd64/data/", func(f *file) { f.uid, f.gid = 0, 0 }), "linux_amd64/data: owner 0:0, want 65532:65532"},
		{"binary not executable", edit("linux_amd64/usr/local/bin/probe-hub", func(f *file) { f.mode = 0o644 }), "linux_amd64/usr/local/bin/probe-hub: mode -rw-r--r--, want -rwxr-xr-x"},
		{"a shell", with(reg("linux_amd64/bin/sh", 0o755, nil)), "linux_amd64/bin/sh: unexpected entry"},
		{"binary for the other architecture", edit("linux_arm64/usr/local/bin/probe-hub", func(f *file) { f.body = fakeELF(elf.EM_X86_64) }), "linux_arm64/usr/local/bin/probe-hub: ELF machine EM_X86_64, want EM_AARCH64"},
		{"binary that is not ELF", edit("linux_amd64/usr/local/bin/probe-hub", func(f *file) { f.body = []byte("#!/bin/sh\n") }), "linux_amd64/usr/local/bin/probe-hub: not an ELF file"},
		{"missing platform", without("linux_arm64/"), "linux_arm64: missing platform"},
		{"extra platform", with(dir("linux_386/", 0o755, 0)), "linux_386: unexpected platform"},
		{"short CA bundle", edit("linux_amd64/etc/ssl/certs/ca-certificates.crt", func(f *file) { f.body = caBundle(1) }), "linux_amd64/etc/ssl/certs/ca-certificates.crt: 1 certificates, want at least 100"},
		{"root account", edit("linux_amd64/etc/passwd", func(f *file) { f.body = []byte("root:x:0:0:root:/root:/bin/sh\n") }), "linux_amd64/etc/passwd: account"},
		{"a second account", edit("linux_arm64/etc/passwd", func(f *file) { f.body = append(f.body, "root:x:0:0:root:/root:/bin/sh\n"...) }), "linux_arm64/etc/passwd: want exactly one account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := check(tarOf(t, tc.mutate(expectedLayout("amd64", "arm64"))), []string{"amd64", "arm64"})
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Fatalf("no problem contains %q; got %q", tc.want, problems)
		})
	}
}

func TestRunRejectsAnEmptyArchitectureList(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"rootfs.tar"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d without architectures, want 2", code)
	}
}

func TestRunExitCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs.tar")
	if err := os.WriteFile(path, tarOf(t, expectedLayout("amd64")).Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{path, "amd64"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("expected layout: exit %d, stdout %q", code, stdout.String())
	}
	stdout.Reset()
	if code := run([]string{path, "amd64", "riscv64"}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "riscv64: no ELF machine known") {
		t.Fatalf("unknown architecture: exit %d, stdout %q", code, stdout.String())
	}
}
```

- [ ] **Step 2: 跑红**

```sh
cd /Users/xjetry/work/vibe/probe-docker && go test -count=1 ./scripts/checkimage > /tmp/pd-t2-red.log 2>&1; echo $?
```

Expected：1，日志含 `undefined: check`（以及 `runUID`、`machines` 等未定义）。

- [ ] **Step 3: 实现**

`scripts/checkimage/main.go`：

```go
// checkimage 核对 hub 镜像的根文件系统。输入是 buildx 以 --output type=tar 导出的多平台结果：
// 每个平台一个 linux_<arch>/ 目录，里面是该平台镜像的完整文件树，不含容器运行时注入的 /dev、/proc 等。
//
// spec §14 要求镜像只含静态二进制、CA 证书与非 root 用户。这里逐条目核对类型、权限与属主，缺少或
// 多出任何条目都拒绝：往镜像里加东西必须同时改 want，这份清单与 Dockerfile 一起审阅。
package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
)

// runUID 是镜像的运行用户与主组，与 Dockerfile 的 USER、rootfs 阶段写进 /etc/passwd 的数同一个。
const runUID = 65532

// minCACerts：Alpine 3.21 的证书包在 2026-09-26 有 121 张根证书。远少于此说明拿到的不是完整的
// 系统证书包，通知出站 HTTPS 会对许多接收方校验失败。
const minCACerts = 100

const (
	passwdPath = "etc/passwd"
	groupPath  = "etc/group"
	caPath     = "etc/ssl/certs/ca-certificates.crt"
	binPath    = "usr/local/bin/probe-hub"
)

type entry struct {
	mode     fs.FileMode // 类型位、权限位与 sticky 位
	uid, gid int
}

var want = map[string]entry{
	// 空卷挂到 /data 时 Docker 沿用镜像里这个目录的属主，hub 以 runUID 写库。
	"data":          {fs.ModeDir | 0o755, runUID, runUID},
	"etc":           {fs.ModeDir | 0o755, 0, 0},
	groupPath:       {0o644, 0, 0},
	passwdPath:      {0o644, 0, 0},
	"etc/ssl":       {fs.ModeDir | 0o755, 0, 0},
	"etc/ssl/certs": {fs.ModeDir | 0o755, 0, 0},
	caPath:          {0o644, 0, 0},
	// SQLite 找不到可写的临时目录时，排序溢出、临时表与建索引都报 disk I/O error (6410)。
	"tmp":           {fs.ModeDir | fs.ModeSticky | 0o777, 0, 0},
	"usr":           {fs.ModeDir | 0o755, 0, 0},
	"usr/local":     {fs.ModeDir | 0o755, 0, 0},
	"usr/local/bin": {fs.ModeDir | 0o755, 0, 0},
	binPath:         {0o755, 0, 0},
}

// machines：镜像索引的平台标签来自构建时的 TARGETARCH，Dockerfile 按它取二进制。二者不符时，
// 能经 binfmt 执行其他架构的宿主照样能把容器跑起来，只有 ELF 头能揭示。
var machines = map[string]elf.Machine{
	"amd64": elf.EM_X86_64,
	"arm64": elf.EM_AARCH64,
}

func check(r io.Reader, arches []string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	expected := map[string]string{} // 平台目录 → GOARCH
	for _, a := range arches {
		if _, ok := machines[a]; !ok {
			add("%s: no ELF machine known for this architecture; add it to machines", a)
			continue
		}
		expected["linux_"+a] = a
	}

	headers := map[string]map[string]*tar.Header{}
	bodies := map[string][]byte{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			add("read tar: %v", err)
			return problems
		}
		name := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, "./"), "/")
		platform, rel, _ := strings.Cut(name, "/")
		if headers[platform] == nil {
			headers[platform] = map[string]*tar.Header{}
		}
		if rel == "" {
			continue
		}
		headers[platform][rel] = hdr
		switch rel {
		case passwdPath, groupPath, caPath, binPath:
			b, err := io.ReadAll(tr)
			if err != nil {
				add("%s: read: %v", name, err)
				continue
			}
			bodies[name] = b
		}
	}

	for _, platform := range slices.Sorted(maps.Keys(headers)) {
		if _, ok := expected[platform]; !ok {
			add("%s: unexpected platform", platform)
		}
	}
	for _, platform := range slices.Sorted(maps.Keys(expected)) {
		got, ok := headers[platform]
		if !ok {
			add("%s: missing platform", platform)
			continue
		}
		for _, rel := range slices.Sorted(maps.Keys(want)) {
			w := want[rel]
			hdr, ok := got[rel]
			if !ok {
				add("%s/%s: missing", platform, rel)
				continue
			}
			if m := hdr.FileInfo().Mode(); m != w.mode {
				add("%s/%s: mode %v, want %v", platform, rel, m, w.mode)
			}
			if hdr.Uid != w.uid || hdr.Gid != w.gid {
				add("%s/%s: owner %d:%d, want %d:%d", platform, rel, hdr.Uid, hdr.Gid, w.uid, w.gid)
			}
		}
		for _, rel := range slices.Sorted(maps.Keys(got)) {
			if _, ok := want[rel]; !ok {
				add("%s/%s: unexpected entry", platform, rel)
			}
		}
		if b, ok := bodies[platform+"/"+passwdPath]; ok {
			if err := checkAccount(b, 7); err != nil {
				add("%s/%s: %v", platform, passwdPath, err)
			}
		}
		if b, ok := bodies[platform+"/"+groupPath]; ok {
			if err := checkAccount(b, 4); err != nil {
				add("%s/%s: %v", platform, groupPath, err)
			}
		}
		if b, ok := bodies[platform+"/"+caPath]; ok {
			if n := countCerts(b); n < minCACerts {
				add("%s/%s: %d certificates, want at least %d", platform, caPath, n, minCACerts)
			}
		}
		if b, ok := bodies[platform+"/"+binPath]; ok {
			machine := machines[expected[platform]]
			f, err := elf.NewFile(bytes.NewReader(b))
			switch {
			case err != nil:
				add("%s/%s: not an ELF file: %v", platform, binPath, err)
			case f.Machine != machine:
				add("%s/%s: ELF machine %v, want %v", platform, binPath, f.Machine, machine)
			}
		}
	}
	return problems
}

// checkAccount 要求文件里只列运行用户一个账户：passwd 七列、group 四列，第三列（uid 或 gid）为 runUID，
// passwd 的第四列（主组）也为 runUID。
func checkAccount(b []byte, fields int) error {
	line := strings.TrimSuffix(string(b), "\n")
	if line == "" || strings.Contains(line, "\n") {
		return fmt.Errorf("want exactly one account, got %q", line)
	}
	f := strings.Split(line, ":")
	if len(f) != fields {
		return fmt.Errorf("malformed line %q", line)
	}
	id := strconv.Itoa(runUID)
	if f[2] != id || (fields == 7 && f[3] != id) {
		return fmt.Errorf("account %q is not %s:%s", line, id, id)
	}
	return nil
}

// countCerts 数 PEM 里的 CERTIFICATE 块；块之间的注释行由 pem.Decode 跳过。
func countCerts(b []byte) int {
	n := 0
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			return n
		}
		if block.Type == "CERTIFICATE" {
			n++
		}
	}
}

// run 是可测的 CLI 入口。架构清单为空时没有平台可核对，等价于"全部通过"，必须在入口拒绝。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: checkimage <rootfs.tar> <arch>...")
		return 2
	}
	f, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer f.Close()
	problems := check(f, args[1:])
	for _, p := range problems {
		fmt.Fprintln(stdout, p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 4: 跑绿**

```sh
cd /Users/xjetry/work/vibe/probe-docker && go test -count=1 ./scripts/checkimage > /tmp/pd-t2-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && go vet ./scripts/... > /tmp/pd-t2-vet.log 2>&1; echo $?
```

Expected：都是 0。

- [ ] **Step 5: 缺陷注入**

`git add scripts/checkimage` 建基准。每项：改 `scripts/checkimage/main.go`，`git diff --stat` 列出它，跑 `go test -count=1 ./scripts/checkimage > /tmp/pd-t2-inject-<项>.log 2>&1; echo $?` 期望 1 并核对失败的子测试，`git checkout -- scripts/checkimage/main.go` 还原后复跑期望 0。

(a) `want["tmp"]` 的 `fs.ModeDir | fs.ModeSticky | 0o777` 改为 `fs.ModeDir | 0o777`：`TestAcceptsTheExpectedLayout` 失败于 `linux_amd64/tmp: mode dtrwxrwxrwx, want drwxrwxrwx`，`TestRejectsEachDeviation/tmp_without_the_sticky_bit` 失败于 `no problem contains`。
(b) 删掉属主比较的 `if hdr.Uid != w.uid || …` 块：`TestRejectsEachDeviation/data_owned_by_root` 失败。
(c) 删掉"`unexpected entry`"那段循环：`TestRejectsEachDeviation/a_shell` 失败。
(d) 删掉 `case f.Machine != machine:` 分支：`TestRejectsEachDeviation/binary_for_the_other_architecture` 失败。
(e) 删掉"`unexpected platform`"那段循环：`TestRejectsEachDeviation/extra_platform` 失败。

- [ ] **Step 6: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-docker && git add scripts/checkimage && git commit -m "scripts: checkimage 逐条目核对 hub 镜像的根文件系统" -m "输入是 buildx 多平台 tar 输出。每个平台核对条目集合、类型、权限、属主，账户只有 65532，证书包完整，二进制的 ELF 机器类型与平台标签一致。标签与二进制不符时能经 binfmt 跨架构执行的宿主照样能把容器跑起来，只有 ELF 头能揭示。"
```

---

### Task 3: Makefile：发布构建参数与 hub 构建命令各只写一处

**Files:**
- Modify: `Makefile`

**Interfaces:**
- Produces：`RELEASE_GOFLAGS`（`-trimpath -ldflags "-X main.version=$(VERSION)"`）；`hub_build`（`$(call hub_build,<GOARCH>,<输出路径>)` 展开为一条 `env GOOS=linux GOARCH=… CGO_ENABLED=0 go build $(RELEASE_GOFLAGS) -o "…" ./cmd/hub`）。Task 4 的 `docker` 目标调用 `hub_build`。`make release` 的产物与参数不变。

- [ ] **Step 1: 记下改动前的展开**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make -n release VERSION=v0.0.0-rc1 > /tmp/pd-t3-n-before.log 2>&1; echo $?
```

Expected：0。日志里 agent 与 hub 的构建命令都含 `go build -trimpath -ldflags "-X main.version=v0.0.0-rc1"`。

- [ ] **Step 2: 改 Makefile**

在 `HUB_LINUX_ARCHES := amd64 arm64` 之后加：

```make

# 发布产物的构建参数（§14）：版本经 ldflags 注入，-trimpath 去掉构建机路径。agent 与 hub、
# tar 包与镜像里的 hub 都经它构建，任何一种产物都不会单独漂移。
RELEASE_GOFLAGS = -trimpath -ldflags "-X main.version=$(VERSION)"

# 一个 Linux hub 二进制：$(1) 为 GOARCH，$(2) 为输出路径。release 打包与 docker 镜像都调用它。
hub_build = env GOOS=linux GOARCH=$(1) CGO_ENABLED=0 go build $(RELEASE_GOFLAGS) -o "$(2)" ./cmd/hub
```

`release` 配方里两处构建行改为：

```make
	@set -e; for arch in $(AGENT_LINUX_ARCHES); do \
	  case $$arch in armv7) gflags="GOARCH=arm GOARM=7" ;; *) gflags="GOARCH=$$arch" ;; esac; \
	  env GOOS=linux CGO_ENABLED=0 $$gflags go build $(RELEASE_GOFLAGS) -o "dist/build/probe-agent-linux-$$arch" ./cmd/agent; \
	done; \
	for arch in $(HUB_LINUX_ARCHES); do \
	  $(call hub_build,$$arch,dist/build/probe-hub-linux-$$arch); \
	done
```

（配方行以制表符开头。其余行不动。）

- [ ] **Step 3: 展开逐字不变**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make -n release VERSION=v0.0.0-rc1 > /tmp/pd-t3-n-after.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && cmp /tmp/pd-t3-n-before.log /tmp/pd-t3-n-after.log > /tmp/pd-t3-cmp.log 2>&1; echo $?
```

Expected：两条都是 0。`cmp` 不为 0 时 `diff` 两份日志，差异只可能是空白，改到逐字相同。

- [ ] **Step 4: 缺陷注入**

`git add Makefile` 建基准。把 `RELEASE_GOFLAGS` 里的 `-trimpath ` 删掉，`git diff --stat` 列出 `Makefile`。

```sh
cd /Users/xjetry/work/vibe/probe-docker && make -n release VERSION=v0.0.0-rc1 > /tmp/pd-t3-n-inject.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && diff /tmp/pd-t3-n-before.log /tmp/pd-t3-n-inject.log > /tmp/pd-t3-diff-inject.log 2>&1; echo $?
```

Expected：第一条 0，第二条 1；`/tmp/pd-t3-diff-inject.log` 里 agent 循环与 hub 循环两处都少了 `-trimpath`——证明两类产物都经 `RELEASE_GOFLAGS`。`git checkout -- Makefile`，复跑 Step 3 期望 0。

- [ ] **Step 5: 真实构建一次**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make release VERSION=v0.0.0-rc1 > /tmp/pd-t3-release.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && d=$(mktemp -d) && tar -xzf dist/probe-hub_linux_arm64.tar.gz -C "$d" && go version -m "$d/probe-hub" > /tmp/pd-t3-hub-m.log 2>&1; echo $?
```

Expected：两条都是 0。`/tmp/pd-t3-hub-m.log` 含 `-trimpath=true`、`-ldflags="-X main.version=v0.0.0-rc1"`、`CGO_ENABLED=0`、`GOARCH=arm64`。

- [ ] **Step 6: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-docker && git add Makefile && git commit -m "build: 发布构建参数与 hub 构建命令各只写一处" -m "RELEASE_GOFLAGS 统一版本注入与 -trimpath，hub_build 是构建一个 Linux hub 二进制的唯一写法，release 打包与随后的镜像共用。make -n release 的展开与改动前逐字相同。"
```

---

### Task 4: Dockerfile、.dockerignore 与 `make docker`（构建、核对、装入本机）

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`
- Modify: `Makefile`（新变量与 `docker` 目标；`.PHONY`）
- Modify: `.gitignore`（`/build/`）

**Interfaces:**
- Consumes：`hub_build`（Task 3）；`go run ./scripts/checkimage <tar> <arch>...`（Task 2）；`go run ./scripts/checkstatic <file>...`（既有）。
- Produces：`make docker VERSION=v…`：两个平台的 hub 二进制在 `build/image/linux/<arch>/probe-hub`，两个平台的根文件系统在 `build/image/rootfs.tar` 并通过 checkimage，本机平台的镜像以 `ghcr.io/xjetry/probe-hub:<VERSION>` 装进本机 docker。Make 变量与函数供 Task 5、6 使用：`DOCKER_IMAGE`（`ghcr.io/xjetry/probe-hub`）、`DOCKER_PLATFORMS`（`linux/amd64,linux/arm64`）、`DOCKER_BUILDER`（`probe-hub-buildkit-v0.33.0`）、`docker_build`（`docker buildx build --builder $(DOCKER_BUILDER) -f Dockerfile`，调用处自己接输出参数与末尾的 `.`）、`check_image_version`（配方里以 `@$(check_image_version)` 使用）。镜像：`/usr/local/bin/probe-hub`，`USER 65532:65532`，`VOLUME /data`，`EXPOSE 8080`，`ENTRYPOINT ["/usr/local/bin/probe-hub"]`，`CMD ["serve", "--db", "/data/probe.db", "--listen", "0.0.0.0:8080"]`。

- [ ] **Step 1: Makefile、.gitignore、.dockerignore**

`.PHONY` 行末尾加 ` docker`。Makefile 末尾追加（配方行以制表符开头）：

```make

# hub 镜像（§14）：ghcr.io/xjetry/probe-hub:<version>，平台由 HUB_LINUX_ARCHES 展开。
# 镜像里不编译 Go：hub 二进制经 hub_build 构建到 IMAGE_BIN_DIR，Dockerfile 按 TARGETARCH 取用。
DOCKER_IMAGE := ghcr.io/xjetry/probe-hub
comma := ,
empty :=
space := $(empty) $(empty)
DOCKER_PLATFORMS := $(subst $(space),$(comma),$(addprefix linux/,$(HUB_LINUX_ARCHES)))
# 构建上下文里的二进制按 linux/<arch>/probe-hub 排列，.dockerignore 只放行这些文件。
# 不放在 dist/ 下：release.yml 以 dist/* 整体上传发布资产，而镜像在 make release 之后、
# gh release create 之前构建，目录混进 dist/ 就会被 dist/* 展开进上传参数。
IMAGE_BIN_DIR := build/image
# 构建节点固定为这一版 BuildKit（docker-container 驱动），本地、CI 与发布用同一个：docker 自带的
# docker 驱动在经典镜像存储上不支持多平台构建，而是否启用 containerd 存储是宿主的配置。
# 构建器名带版本号：改 BUILDKIT_VERSION 即换用新构建器，旧的不会被沿用；不用时 docker buildx rm 删除。
# 首次运行时 docker buildx inspect 会报 no builder found，随后创建。
BUILDKIT_VERSION := v0.33.0
BUILDKIT_IMAGE := moby/buildkit:$(BUILDKIT_VERSION)@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3
DOCKER_BUILDER := probe-hub-buildkit-$(BUILDKIT_VERSION)
docker_build = docker buildx build --builder $(DOCKER_BUILDER) -f Dockerfile

# 镜像 tag 与版本号逐字相同：probe-hub version 打印的就是 tag。Docker 的 tag 只允许 [A-Za-z0-9_.-]、
# 首字符不为 . 与 -、至多 128 个字符，带构建元数据（+）的版本因而不能发布镜像，在构建之前拒绝。
check_image_version = if [ -z '$(VERSION)' ]; then echo "VERSION is required, e.g. make docker VERSION=v0.1.0" >&2; exit 1; fi; \
	if ! printf '%s\n' '$(VERSION)' | grep -Eqx '[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}'; then \
	  echo "VERSION '$(VERSION)' cannot be an image tag: only [A-Za-z0-9_.-], at most 128 characters, no + build metadata" >&2; exit 1; fi

# 本地构建并核对（§14）：两个平台都构建、导出根文件系统交给 checkimage，再把本机平台装进 docker。
# 多平台结果不能 --load：经典镜像存储不接受多平台索引（docker exporter does not currently support
# exporting manifest lists），不给 --platform 时构建的是构建节点的本机平台。
# 面板随 go:embed 进二进制，先 make web：漏掉它，镜像里的 /admin/ 只有 503 说明页。
docker:
	@$(check_image_version)
	$(MAKE) web
	rm -rf $(IMAGE_BIN_DIR)
	@set -e; for arch in $(HUB_LINUX_ARCHES); do \
	  mkdir -p "$(IMAGE_BIN_DIR)/linux/$$arch"; \
	  $(call hub_build,$$arch,$(IMAGE_BIN_DIR)/linux/$$arch/probe-hub); \
	done
	go run ./scripts/checkstatic $(foreach a,$(HUB_LINUX_ARCHES),$(IMAGE_BIN_DIR)/linux/$(a)/probe-hub)
	docker buildx inspect $(DOCKER_BUILDER) > /dev/null || docker buildx create --name $(DOCKER_BUILDER) --driver docker-container --driver-opt image=$(BUILDKIT_IMAGE) --bootstrap
	$(docker_build) --platform $(DOCKER_PLATFORMS) --output type=tar,dest=$(IMAGE_BIN_DIR)/rootfs.tar .
	go run ./scripts/checkimage $(IMAGE_BIN_DIR)/rootfs.tar $(HUB_LINUX_ARCHES)
	$(docker_build) -t $(DOCKER_IMAGE):$(VERSION) --load .
```

`.gitignore` 的"构建产物"一组加一行 `/build/`。

`.dockerignore`：

```
# 构建上下文只放行 make docker 预先构建的 hub 二进制，Dockerfile 只 COPY 它们。
# 其余一概排除：node_modules、参考项目、.git 与本地库文件不该进入镜像构建。
*
!build/image/linux/*/probe-hub
```

- [ ] **Step 2: VERSION 的拒绝路径**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make docker > /tmp/pd-t4-nover.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make docker VERSION=v0.1.0+b1 > /tmp/pd-t4-plus.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && grep -c 'go build' /tmp/pd-t4-nover.log /tmp/pd-t4-plus.log > /tmp/pd-t4-nobuild.log 2>&1; echo $?
```

Expected：前两条为 2，日志分别含 `VERSION is required` 与 `VERSION 'v0.1.0+b1' cannot be an image tag`。第三条为 1（grep 没有匹配），`/tmp/pd-t4-nobuild.log` 两行计数都是 0：在构建之前就拒绝了。

- [ ] **Step 3: 跑红（还没有 Dockerfile）**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make docker VERSION=v0.0.0-dev > /tmp/pd-t4-red.log 2>&1; echo $?
```

Expected：2。日志里 checkstatic 已通过（没有 `not an ELF`、`PT_INTERP` 之类的行），构建器 `probe-hub-buildkit-v0.33.0` 已创建（或已存在），失败在 buildx 读不到 Dockerfile（日志含 `Dockerfile` 与 `no such file or directory`）。

- [ ] **Step 4: 写 Dockerfile**

```dockerfile
# hub 镜像（spec §14）：FROM scratch，只含静态二进制、CA 证书与非 root 用户。
# 镜像里不编译 Go：make docker 用与 release 同一条构建命令把二进制产出到 build/image/linux/<arch>/，
# 这里按 TARGETARCH 取用。根文件系统由 scripts/checkimage 逐条目核对，改这里须同步改那份清单。

# 这一阶段只产出与架构无关的文件，固定在构建节点的本机平台上运行；最终阶段没有 RUN，
# 所以多平台构建不在目标架构上执行任何程序。
FROM --platform=$BUILDPLATFORM alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507 AS rootfs
# CA 证书供通知出站 HTTPS（Go 在 Linux 上先读 /etc/ssl/certs/ca-certificates.crt）；
# --upgrade 取 3.21 仓库里当前的证书包，而不是基础镜像构建时的那份。
# /data 交给运行用户：空的命名卷或匿名卷挂到 /data 时，Docker 把镜像里这个目录的属主带到卷上。
# /tmp 为 1777：SQLite 的排序溢出、临时表与建索引写临时文件，找不到可写的临时目录时报 disk I/O error (6410)。
RUN apk add --no-cache --upgrade ca-certificates-bundle \
 && mkdir -p /rootfs/etc/ssl/certs /rootfs/data \
 && mkdir -m 1777 /rootfs/tmp \
 && cp /etc/ssl/certs/ca-certificates.crt /rootfs/etc/ssl/certs/ \
 && printf 'probe-hub:x:65532:65532:probe-hub:/nonexistent:/sbin/nologin\n' > /rootfs/etc/passwd \
 && printf 'probe-hub:x:65532:\n' > /rootfs/etc/group \
 && chown 65532:65532 /rootfs/data

FROM scratch
ARG TARGETARCH
LABEL org.opencontainers.image.source="https://github.com/xjetry/probe"
# COPY --from 保留源阶段的属主（/data 为 65532），来自构建上下文的文件归 root。
COPY --from=rootfs /rootfs/ /
# 放在容器的默认 PATH 里：docker exec <容器> probe-hub passwd … 按名字就能执行（§14）。
COPY build/image/linux/${TARGETARCH}/probe-hub /usr/local/bin/probe-hub
# 数字形式，与 /data 的属主、/etc/passwd 里的账户是同一个 uid，不经名字解析。
USER 65532:65532
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/probe-hub"]
# 容器里监听非 loopback 是预期的，hub 的启动告警照旧；反代与 --trusted-proxies 由部署者配（§14）。
CMD ["serve", "--db", "/data/probe.db", "--listen", "0.0.0.0:8080"]
```

- [ ] **Step 5: 固定值仍可解析**

```sh
cd /Users/xjetry/work/vibe/probe-docker && docker buildx imagetools inspect alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507 > /tmp/pd-t4-alpine.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && docker buildx imagetools inspect moby/buildkit:v0.33.0@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3 > /tmp/pd-t4-buildkit.log 2>&1; echo $?
```

Expected：都是 0，两份日志的 `Manifests` 都列出 `linux/amd64` 与 `linux/arm64`。

- [ ] **Step 6: 跑绿**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make docker VERSION=v0.0.0-dev > /tmp/pd-t4-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && docker image inspect ghcr.io/xjetry/probe-hub:v0.0.0-dev --format '{{.Architecture}} {{.Config.User}} {{json .Config.Entrypoint}} {{json .Config.Cmd}} {{json .Config.Volumes}} {{json .Config.ExposedPorts}}' > /tmp/pd-t4-inspect.log 2>&1; echo $?
```

Expected：都是 0。`/tmp/pd-t4-green.log` 在 `go run ./scripts/checkimage` 之后没有问题行，末尾有 `naming to ghcr.io/xjetry/probe-hub:v0.0.0-dev`。`/tmp/pd-t4-inspect.log` 为 `arm64 65532:65532 ["/usr/local/bin/probe-hub"] ["serve","--db","/data/probe.db","--listen","0.0.0.0:8080"] {"/data":{}} {"8080/tcp":{}}`（本机为 arm64）。另看一眼构建日志里 `rootfs 2/2` 的 RUN 只以 `linux/arm64` 出现一次（实验 2）。

- [ ] **Step 7: 缺陷注入与一次性 SQLite 实验**

`git add Dockerfile .dockerignore .gitignore Makefile` 建基准。

(a) 删掉 Dockerfile 里 ` && mkdir -m 1777 /rootfs/tmp \` 这一行。`git diff --stat` 列出 `Dockerfile`。

```sh
cd /Users/xjetry/work/vibe/probe-docker && make docker VERSION=v0.0.0-inject > /tmp/pd-t4-inject-a.log 2>&1; echo $?
```

Expected：2，日志含 `linux_amd64/tmp: missing` 与 `linux_arm64/tmp: missing`，没有走到 `--load`。

趁注入还在，做 Review Focus 第 3 条的一次性实验（程序不入库，放在私有临时目录；二进制在本机平台 arm64 上跑）：

```sh
cd /Users/xjetry/work/vibe/probe-docker && docker buildx build --builder probe-hub-buildkit-v0.33.0 -f Dockerfile -t probe-hub-notmp:exp --load . > /tmp/pd-t4-notmp-build.log 2>&1; echo $?
```

Expected：0（绕过 checkimage，直接装入没有 `/tmp` 的镜像）。建实验程序：

```sh
exp=$(mktemp -d /tmp/pd-t4-sqlite.XXXXXX) && echo "$exp" > /tmp/pd-t4-exp-dir && cp /Users/xjetry/work/vibe/probe-docker/go.sum "$exp/" && cd "$exp" && cat > go.mod <<'EOF'
module sqlitetmp

go 1.27.1

require modernc.org/sqlite v1.59.0

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.48.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
EOF
cat > main.go <<'EOF'
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// 让排序与临时表的数据量超出页缓存，逼 SQLite 把中间结果写进临时文件。
func main() {
	db, err := sql.Open("sqlite", "file:/data/t.db?_pragma=journal_mode(WAL)")
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		"CREATE TABLE IF NOT EXISTS p(x)",
		"DELETE FROM p",
		"WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < 300000) INSERT INTO p SELECT randomblob(64) FROM c",
		"PRAGMA cache_size=16",
		"CREATE INDEX IF NOT EXISTS p_x ON p(x)",
		"DROP INDEX IF EXISTS p_x",
		"CREATE TEMP TABLE t AS SELECT * FROM p",
		"SELECT count(*) FROM (SELECT x FROM p ORDER BY x)",
	} {
		if _, err := db.Exec(q); err != nil {
			fmt.Printf("%.40s: %v\n", q, err)
		} else {
			fmt.Printf("%.40s: ok\n", q)
		}
	}
}
EOF
GOFLAGS=-mod=mod GOPROXY=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o sqlitetmp . > /tmp/pd-t4-sqlite-build.log 2>&1; echo $?
```

Expected：0。在没有 `/tmp` 的镜像里以镜像的运行用户跑它：

```sh
exp=$(cat /tmp/pd-t4-exp-dir) && docker volume create probe-exp-sqlite > /dev/null && docker run --rm -v probe-exp-sqlite:/data -v "$exp/sqlitetmp:/sqlitetmp:ro" --entrypoint /sqlitetmp probe-hub-notmp:exp > /tmp/pd-t4-sqlite-notmp.log 2>&1; echo $?
```

Expected：0（程序自己不以失败退出），`/tmp/pd-t4-sqlite-notmp.log` 里 `CREATE INDEX`、`CREATE TEMP TABLE`、`SELECT count(*)` 三行都是 `disk I/O error (6410)`。

还原：`git checkout -- Dockerfile`，重建并在真镜像里复跑：

```sh
cd /Users/xjetry/work/vibe/probe-docker && make docker VERSION=v0.0.0-dev > /tmp/pd-t4-green2.log 2>&1; echo $?
exp=$(cat /tmp/pd-t4-exp-dir) && docker run --rm -v probe-exp-sqlite:/data -v "$exp/sqlitetmp:/sqlitetmp:ro" --entrypoint /sqlitetmp ghcr.io/xjetry/probe-hub:v0.0.0-dev > /tmp/pd-t4-sqlite-tmp.log 2>&1; echo $?
docker image rm probe-hub-notmp:exp > /tmp/pd-t4-rm.log 2>&1; docker volume rm probe-exp-sqlite >> /tmp/pd-t4-rm.log 2>&1; echo $?
```

Expected：三条都是 0，`/tmp/pd-t4-sqlite-tmp.log` 八行都是 `ok`。两份实验日志的结论写进提交信息。

(b) 删掉 Dockerfile 里 ` && chown 65532:65532 /rootfs/data` 这一段（前一行末尾的 ` \` 一并调整，保持 RUN 语法正确）。`git diff --stat` 列出 `Dockerfile`。`make docker VERSION=v0.0.0-inject > /tmp/pd-t4-inject-b.log 2>&1; echo $?` 期望 2，日志含 `linux_amd64/data: owner 0:0, want 65532:65532`。`git checkout -- Dockerfile`。

(c) Makefile 的 `docker` 目标里 `$(call hub_build,$$arch,…)` 的第一个参数改成 `amd64`（两个槽位都放 amd64 二进制）。`git diff --stat` 列出 `Makefile`。`make docker VERSION=v0.0.0-inject > /tmp/pd-t4-inject-c.log 2>&1; echo $?` 期望 2：checkstatic 通过（两个都是静态 ELF），失败在 `linux_arm64/usr/local/bin/probe-hub: ELF machine EM_X86_64, want EM_AARCH64`。`git checkout -- Makefile`。

还原后 `make docker VERSION=v0.0.0-dev > /tmp/pd-t4-green3.log 2>&1; echo $?` 期望 0。

- [ ] **Step 8: 提交**

按 Step 7 的实际输出核对下面的提交信息，与输出不符的句子改成实际情况再提交：

```sh
cd /Users/xjetry/work/vibe/probe-docker && git add Dockerfile .dockerignore .gitignore Makefile && git commit -m "build: hub 的 Docker 镜像，make docker 构建两个平台并核对根文件系统" -m "镜像 FROM scratch，二进制由 hub_build 预先构建、按 TARGETARCH COPY 到 /usr/local/bin；CA 证书与账户文件来自固定在构建节点本机平台运行的 alpine 阶段，最终阶段没有 RUN。构建器是按 digest 固定的 BuildKit（docker-container 驱动），两个平台以 tar 导出交给 checkimage，本机平台 --load。/tmp 为 1777：同一个 SQLite 程序在没有 /tmp 的镜像里 CREATE INDEX、临时表与大排序都报 disk I/O error (6410)，在本镜像里全部成功（modernc.org/sqlite v1.59.0，30 万行，cache_size=16）。"
```

---

### Task 5: scripts/docker-smoke.sh 与 `make docker-smoke`

**Files:**
- Create: `scripts/docker-smoke.sh`（可执行）
- Modify: `Makefile`（`docker-smoke` 目标；`docker` 配方末尾调用它；`lint` 的 shellcheck 清单；`.PHONY`）

**Interfaces:**
- Consumes：Task 1 的两段报错文字；Task 4 的镜像与 `DOCKER_IMAGE`、`check_image_version`。
- Produces：`make docker-smoke VERSION=v… [SMOKE_PLATFORM=linux/<arch>]`，冒烟本机 docker 里已有的 `ghcr.io/xjetry/probe-hub:<VERSION>`。脚本的环境变量约定：`IMAGE`（必填）、`VERSION`（必填，镜像必须报告它）、`SMOKE_PLATFORM`（空表示 docker 的默认平台）——Task 6 的回读脚本按这个约定直接调用它。`make docker` 此后以冒烟结束。

- [ ] **Step 1: 写冒烟脚本**

`scripts/docker-smoke.sh`，写完 `chmod +x`：

```sh
#!/bin/sh
# hub 镜像冒烟（spec §12、§14）：以默认参数起容器，/admin/ 返回嵌入的面板，docker exec 经 stdin
# 设密码后能登录，docker stop 后以 0 退出。另外钉住部署时最容易踩到的几条：运行用户与卷属主、
# passwd 没收到输入时的报错、库目录不可写时的退出、非 loopback 监听与时区回退的告警、README 里
# 不依赖镜像内 shell 的排查办法。只创建与删除带本次运行前缀的容器和卷；工件目录保留，路径在开头打印。
set -eu
: "${IMAGE:?IMAGE is required, e.g. ghcr.io/xjetry/probe-hub:v0.1.0}"
: "${VERSION:?VERSION is required: the image must report exactly this version}"
# 空表示 docker 的默认平台；发布后回读时逐个架构给出。
platform=${SMOKE_PLATFORM:-}
# 带 shell 的工具镜像：读卷里的属主、预置不可写的卷、在 hub 的网络命名空间里发请求。不进入产物。
tool=alpine:3.21
work=$(mktemp -d)
echo "docker smoke artifacts: $work (image $IMAGE${platform:+, platform $platform})"
run_id=probe-smoke-$(basename "$work")
hub=$run_id-hub
denied=$run_id-denied
data=$run_id-data
denied_data=$run_id-denied-data

cleanup() {
  docker rm -f "$hub" "$denied" > /dev/null 2>&1 || true
  docker volume rm "$data" "$denied_data" > /dev/null 2>&1 || true
}
trap cleanup EXIT
# dash 与 busybox ash 被信号终止时不执行 EXIT trap；转成 exit，清理照常发生。
trap 'exit 1' INT TERM HUP

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# docker run 带上指定的平台；未指定时不加参数，由 docker 取默认平台。
drun() {
  if [ -n "$platform" ]; then
    docker run --platform "$platform" "$@"
  else
    docker run "$@"
  fi
}

got=$(drun --rm "$IMAGE" version) || fail "probe-hub version exited $?"
[ "$got" = "$VERSION" ] || fail "image reports version '$got', want '$VERSION'"
echo "version ok: $got"

docker volume create "$data" > /dev/null
drun -d --name "$hub" -p 127.0.0.1::8080 -v "$data:/data" "$IMAGE" > /dev/null
hostport=$(docker port "$hub" 8080/tcp) || fail "port 8080 of $hub is not published"
base="http://127.0.0.1:${hostport##*:}"

# 三种失败分开报：容器已退出（没起来）、到上限仍无 HTTP 应答（起了但没在监听）、有应答但不是面板。
# 上限 30 秒：发布后回读在 QEMU 模拟的 arm64 上跑同一段，启动比本机慢。
deadline=$(($(date +%s) + 30))
while :; do
  state=$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$hub")
  case $state in
    running*) ;;
    *)
      docker logs "$hub" >&2
      fail "hub container is not running ($state)"
      ;;
  esac
  rc=0
  status=$(curl -sS --max-time 2 -o "$work/admin.html" -w '%{http_code}' "$base/admin/" 2> "$work/admin.curl") || rc=$?
  [ "$rc" = 0 ] && break
  if [ "$(date +%s)" -ge "$deadline" ]; then
    docker logs "$hub" >&2
    cat "$work/admin.curl" >&2
    fail "no HTTP answer on $base/admin/ within 30s (curl exit $rc)"
  fi
  sleep 0.5
done
# 503 是 internal/hub/web 的"面板没有构建进二进制"说明页：镜像里的 hub 缺了 make web 的产物。
[ "$status" = 200 ] || {
  cat "$work/admin.html" >&2
  fail "/admin/ returned $status, want 200"
}
grep -q 'id="root"' "$work/admin.html" || fail "/admin/ is not the panel index"
echo "admin ok: $base/admin/"

docker logs "$hub" > "$work/hub.log" 2>&1
# 容器里监听 0.0.0.0 是预期的，告警照旧（§5.4、§14）：能直连这个端口的人都能绕过反代并自带转发头。
grep -q 'listening on a non-loopback address' "$work/hub.log" || {
  cat "$work/hub.log" >&2
  fail "startup log lacks the non-loopback listen warning"
}
# 镜像里没有 /etc/localtime、默认不设 TZ：退回 UTC 并告警（§5.4），README 据此要求显式给时区。
grep -q 'host time zone could not be resolved; using UTC' "$work/hub.log" || {
  cat "$work/hub.log" >&2
  fail "startup log lacks the UTC fallback warning"
}
echo "startup warnings ok"

# README 与 §14 的命令按名字执行 probe-hub：二进制必须在容器的默认 PATH 里。
got=$(docker exec "$hub" probe-hub version 2> "$work/exec.err") || {
  cat "$work/exec.err" >&2
  fail "docker exec $hub probe-hub version failed: probe-hub is not runnable by name"
}
[ "$got" = "$VERSION" ] || fail "docker exec probe-hub version printed '$got', want '$VERSION'"

# 不带 -i 时容器里的 stdin 是 /dev/null：passwd 必须报没有收到密码，且不改管理员表。
rc=0
docker exec "$hub" probe-hub passwd --db /data/probe.db > "$work/passwd-noinput.log" 2>&1 || rc=$?
[ "$rc" != 0 ] || fail "passwd without -i succeeded"
grep -q 'no password on stdin' "$work/passwd-noinput.log" || {
  cat "$work/passwd-noinput.log" >&2
  fail "passwd without -i did not report the missing input"
}
docker exec "$hub" probe-hub stats --db /data/probe.db > "$work/stats.log" 2>&1 || {
  cat "$work/stats.log" >&2
  fail "probe-hub stats"
}
grep -qx 'admin: 0' "$work/stats.log" || {
  cat "$work/stats.log" >&2
  fail "passwd without input changed the admin table"
}
echo "passwd without -i ok"

pw='smoke admin password 2026'
printf '%s\n' "$pw" | docker exec -i "$hub" probe-hub passwd --db /data/probe.db > "$work/passwd.log" 2>&1 || {
  cat "$work/passwd.log" >&2
  fail "passwd via docker exec -i"
}
login() {
  curl -sS -o "$work/login.json" -D "$work/login.headers" -w '%{http_code}' -H 'Content-Type: application/json' \
    --data "{\"password\":\"$1\"}" "$base/probe.v1.AdminService/Login"
}
[ "$(login 'not the admin password')" = 401 ] || {
  cat "$work/login.json" >&2
  fail "a wrong password was not rejected with 401"
}
[ "$(login "$pw")" = 200 ] || {
  cat "$work/login.json" >&2
  fail "login with the password set through docker exec"
}
grep -qi '^set-cookie: probe_session=' "$work/login.headers" || fail "login did not set the session cookie"
echo "passwd and login ok"

# hub 以 uid 65532 运行：它在新建的命名卷里写出的库文件属于 65532（Docker 把镜像里 /data 的属主带到空卷上）。
docker run --rm -v "$data:/data" "$tool" stat -c '%u:%g %n' /data /data/probe.db > "$work/owner.log" 2>&1 || {
  cat "$work/owner.log" >&2
  fail "stat the data volume"
}
awk '$1 != "65532:65532" { bad = 1 } END { exit bad || NR != 2 }' "$work/owner.log" || {
  cat "$work/owner.log" >&2
  fail "data volume entries are not owned by 65532:65532"
}
# README 的排查办法：镜像里没有 shell，用工具镜像进入 hub 的网络命名空间发请求。
docker run --rm --network "container:$hub" "$tool" wget -q -O /dev/null http://127.0.0.1:8080/admin/ > "$work/netns.log" 2>&1 || {
  cat "$work/netns.log" >&2
  fail "request from the hub's network namespace"
}
echo "volume owner and debugging recipes ok"

# docker stop 发 SIGTERM：hub 的关停路径排空请求、停下后台循环并关库后以 0 退出。非 0 说明
# SIGTERM 没有走到这条路径，退出前的落盘无从保证。
docker stop "$hub" > /dev/null
code=$(docker inspect -f '{{.State.ExitCode}}' "$hub")
[ "$code" = 0 ] || {
  docker logs "$hub" >&2
  fail "hub exited $code on docker stop, want 0"
}
echo "stop ok"

# 库目录不可写（绑定了属主为 root 的宿主目录）：卷里已有内容时 Docker 不改它的属主，
# 用预置了 root 文件的卷模拟。hub 必须退出，报错里写出库路径。
docker volume create "$denied_data" > /dev/null
docker run --rm -v "$denied_data:/data" "$tool" sh -c 'touch /data/placeholder && chown 0:0 /data /data/placeholder && chmod 755 /data' || fail "prepare the root-owned volume"
drun -d --name "$denied" -v "$denied_data:/data" "$IMAGE" > /dev/null
deadline=$(($(date +%s) + 30))
until [ "$(docker inspect -f '{{.State.Status}}' "$denied")" = exited ]; do
  [ "$(date +%s)" -lt "$deadline" ] || {
    docker logs "$denied" >&2
    fail "hub kept running on an unwritable /data"
  }
  sleep 0.5
done
code=$(docker inspect -f '{{.State.ExitCode}}' "$denied")
docker logs "$denied" > "$work/denied.log" 2>&1
[ "$code" = 1 ] || {
  cat "$work/denied.log" >&2
  fail "hub on an unwritable /data exited $code, want 1"
}
grep -q 'open database /data/probe.db' "$work/denied.log" || {
  cat "$work/denied.log" >&2
  fail "error on an unwritable /data does not name the database path"
}
echo "unwritable data dir ok"
echo "docker smoke passed: $IMAGE${platform:+ ($platform)}"
```

- [ ] **Step 2: Makefile**

`.PHONY` 行末尾加 ` docker-smoke`。`lint` 的 shellcheck 行改为：

```make
	shellcheck -s sh deploy/install.sh deploy/openrc/probe-agent scripts/docker-smoke.sh
```

`docker` 配方最后一行（`--load .` 那行）之后加一行：

```make
	$(MAKE) docker-smoke
```

`docker` 目标之后追加：

```make

# 冒烟本机 docker 里已有的 $(DOCKER_IMAGE):$(VERSION)：make docker 构建后调用；发布后回读时先按平台
# docker pull，再由回读脚本以同样的环境变量直接调用脚本。SMOKE_PLATFORM 为空时用 docker 的默认平台。
docker-smoke:
	@$(check_image_version)
	IMAGE='$(DOCKER_IMAGE):$(VERSION)' VERSION='$(VERSION)' SMOKE_PLATFORM='$(SMOKE_PLATFORM)' scripts/docker-smoke.sh
```

- [ ] **Step 3: shellcheck**

```sh
cd /Users/xjetry/work/vibe/probe-docker && shellcheck -s sh scripts/docker-smoke.sh > /tmp/pd-t5-sc.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 4: 冒烟 Task 4 的镜像**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make docker-smoke VERSION=v0.0.0-dev > /tmp/pd-t5-smoke.log 2>&1; echo $?
```

Expected：0。日志依次有 `version ok: v0.0.0-dev`、`admin ok`、`startup warnings ok`、`passwd without -i ok`、`passwd and login ok`、`volume owner and debugging recipes ok`、`stop ok`、`unwritable data dir ok`、`docker smoke passed`。

- [ ] **Step 5: 完整的 `make docker` 与墙钟**

```sh
cd /Users/xjetry/work/vibe/probe-docker && /usr/bin/time -p make docker VERSION=v0.0.0-dev > /tmp/pd-t5-docker.log 2>&1; echo $?
```

Expected：0，日志末尾是冒烟的 `docker smoke passed` 与 `time` 的 `real` 行。`real` 的秒数记下来，Task 7 的 CI 耗时估计与提交信息引用它。

- [ ] **Step 6: 缺陷注入**

先 `git add scripts/docker-smoke.sh Makefile` 建基准。每项改完 `git diff --stat` 必须列出所改文件；验完 `git checkout -- <文件>` 还原。重建都用 `make docker VERSION=v0.0.0-inject`，除非另写。

(a) 面板没进二进制：删掉 Makefile `docker` 配方里的 `$(MAKE) web` 一行，再删掉已构建的面板 `rm -rf internal/hub/web/dist/assets internal/hub/web/dist/index.html`（不入库，`.gitkeep` 保留，后续 `make docker` 会重建）。`make docker VERSION=v0.0.0-inject > /tmp/pd-t5-inject-a.log 2>&1; echo $?` 期望 2，失败于 `/admin/ returned 503, want 200`，打印的页面含 `The admin panel has not been built into this binary`。还原 Makefile。
(b) passwd 空输入：删掉 `cmd/hub/passwd.go` 里 `if line == "" { return "", errNoPasswordInput }` 连同注释。期望 2，失败于 `passwd without -i did not report the missing input`，打印的日志含 `password must be at least 12 characters`。还原。
(c) hub 以 root 运行：Dockerfile 的 `USER 65532:65532` 改为 `USER 0:0`（checkimage 只看文件系统，不看镜像配置，冒烟照常走到）。期望 2，失败于 `data volume entries are not owned by 65532:65532`，打印的 `owner.log` 里 `/data/probe.db` 为 `0:0`。还原。
(d) 库路径缺失：`internal/hub/store/store.go` 的 `return nil, fmt.Errorf("open database %s: %w", path, err)` 改回 `return nil, err`。期望 2，失败于 `error on an unwritable /data does not name the database path`，打印的日志为 `error: unable to open database file (14)`。还原。
(e) 二进制不在 PATH：Dockerfile 的 COPY 目标改为 `/probe-hub`，`ENTRYPOINT` 改为 `["/probe-hub"]`。`make docker VERSION=v0.0.0-inject > /tmp/pd-t5-inject-e1.log 2>&1; echo $?` 期望 2，先被 checkimage 拦下（`usr/local/bin/probe-hub: missing`、`probe-hub: unexpected entry`）。绕过 checkimage 直接装入并冒烟：

```sh
cd /Users/xjetry/work/vibe/probe-docker && docker buildx build --builder probe-hub-buildkit-v0.33.0 -f Dockerfile -t ghcr.io/xjetry/probe-hub:v0.0.0-inject --load . > /tmp/pd-t5-inject-e2.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make docker-smoke VERSION=v0.0.0-inject > /tmp/pd-t5-inject-e3.log 2>&1; echo $?
```

期望 0 与 2；冒烟失败于 `probe-hub is not runnable by name`，打印的 `exec.err` 含 `executable file not found in $PATH`。还原 Dockerfile。
(f) 非 loopback 告警：删掉 `cmd/hub/serve.go` 里 `if !isLoopback(*listen) { log.Warn(...) }` 整块。期望 2，失败于 `startup log lacks the non-loopback listen warning`。还原。
(g) 排查办法：把脚本里 `wget` 的地址端口改为 `8081`（模拟 README 写错的办法）。期望 2，失败于 `request from the hub's network namespace`。还原。
(h) SIGTERM 没走关停路径：删掉 `cmd/hub/serve.go` 里 `signal.Notify(terminate, syscall.SIGTERM)` 一行。期望 2，失败于 `hub exited N on docker stop, want 0`（N 非 0，把实际值记进提交信息）。还原。
(i) 版本不符：

```sh
cd /Users/xjetry/work/vibe/probe-docker && docker tag ghcr.io/xjetry/probe-hub:v0.0.0-dev ghcr.io/xjetry/probe-hub:v0.0.0-other > /tmp/pd-t5-inject-i1.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make docker-smoke VERSION=v0.0.0-other > /tmp/pd-t5-inject-i2.log 2>&1; echo $?
```

期望 0 与 2，失败于 `image reports version 'v0.0.0-dev', want 'v0.0.0-other'`。这一项不改文件，不需要 `git diff`。

全部还原后：`git status --porcelain` 只列出本任务尚未提交的两个文件；`make docker VERSION=v0.0.0-dev > /tmp/pd-t5-green.log 2>&1; echo $?` 期望 0。

- [ ] **Step 7: 冒烟没有留下资源**

"空输出即通过"的检查先拿已知非空的输入冒烟一次：

```sh
docker volume create probe-smoke-filtercheck > /dev/null && docker volume ls -q --filter name=probe-smoke- > /tmp/pd-t5-filter-known.log 2>&1; echo $?
docker volume rm probe-smoke-filtercheck > /dev/null && docker volume ls -q --filter name=probe-smoke- > /tmp/pd-t5-vols.log 2>&1; echo $?
docker ps -a --format '{{.Names}}' --filter name=probe-smoke- > /tmp/pd-t5-ctrs.log 2>&1; echo $?
```

Expected：三条都是 0；第一份日志恰为 `probe-smoke-filtercheck`，后两份为空。

- [ ] **Step 8: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-docker && git add scripts/docker-smoke.sh Makefile && git commit -m "scripts: hub 镜像冒烟，make docker 以它结束" -m "起容器后区分没起来、没应答与不是面板三种失败，/admin/ 只接受 200 的面板页（503 是面板没构建进二进制）。另钉住：probe-hub 按名字可执行、passwd 不带 -i 时报没收到输入且不改管理员表、hub 写出的库属 65532、库目录不可写时以 1 退出并写出路径、非 loopback 与 UTC 回退的告警、docker stop 后以 0 退出、README 的网络命名空间排查办法。本机 make docker 墙钟 <秒数> 秒。"
```

（`<秒数>` 换成 Step 5 实测的 `real` 值，提交信息里只留数字。）

---

### Task 6: 发布：`release-channel`、`docker-push`、回读与 release.yml

**Files:**
- Create: `scripts/docker-readback.sh`（可执行）
- Modify: `Makefile`（`RELEASE_CHANNEL`、`docker_latest`、`release-channel`、`docker-push`、`docker-latest`、`docker-readback`；lint；`.PHONY`）
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes：Task 4 的 `docker` 目标与 `DOCKER_*`、`docker_build`、`check_image_version`；Task 5 的冒烟脚本与其环境变量约定（`IMAGE`、`VERSION`、`SMOKE_PLATFORM`）。
- Produces：`make -s release-channel VERSION=…` 打印 `stable` 或 `prerelease`；`make docker-push VERSION=…`（跑完 `make docker` 后推送两个平台，正式版本另推 `latest`）；`make -s docker-latest` 打印 registry 上 `latest` 的 digest 或 `absent`；`make docker-readback VERSION=… LATEST_BEFORE=…`。release.yml 按"make ci → make release → QEMU → 登录 → 记下 latest → docker-push → 回读 → gh release create"的顺序调用它们。

- [ ] **Step 1: Makefile**

`.PHONY` 行末尾加 ` release-channel docker-push docker-latest docker-readback`。`lint` 的 shellcheck 行改为：

```make
	shellcheck -s sh deploy/install.sh deploy/openrc/probe-agent scripts/docker-smoke.sh scripts/docker-readback.sh
```

`docker-smoke` 目标之后追加：

```make

# 预发布判定（§14）：去掉构建元数据（+ 及之后）后仍含 - 就是预发布。GitHub Release 是否标为 prerelease、
# 镜像是否推 latest 都读它，判定只在这一处。
RELEASE_CHANNEL = $(shell v='$(VERSION)'; case "$${v%%+*}" in (*-*) echo prerelease ;; (*) echo stable ;; esac)
docker_latest = $(if $(filter stable,$(RELEASE_CHANNEL)),-t $(DOCKER_IMAGE):latest)

release-channel:
	@if [ -z '$(VERSION)' ]; then echo "VERSION is required, e.g. make release-channel VERSION=v0.1.0" >&2; exit 1; fi
	@echo $(RELEASE_CHANNEL)

# 发布镜像：先走完 make docker（两个平台的根文件系统核对、本机平台冒烟），再推送两个平台。
# 只由 release.yml 在登录 ghcr 之后调用；推送沿用 make docker 的构建器与缓存，构建参数同一处。
docker-push:
	@$(check_image_version)
	$(MAKE) docker
	$(docker_build) --platform $(DOCKER_PLATFORMS) -t $(DOCKER_IMAGE):$(VERSION) $(docker_latest) --push .

# 发布后回读（release.yml 调用）：推送前记下 latest 的指向，推送后回读版本、匿名可取与 latest。
docker-latest:
	@IMAGE_REPO=$(DOCKER_IMAGE) scripts/docker-readback.sh latest

docker-readback:
	@$(check_image_version)
	IMAGE_REPO=$(DOCKER_IMAGE) VERSION='$(VERSION)' CHANNEL=$(RELEASE_CHANNEL) ARCHES='$(HUB_LINUX_ARCHES)' scripts/docker-readback.sh verify '$(LATEST_BEFORE)'
```

- [ ] **Step 2: 预发布判定的用例与注入**

```sh
cd /Users/xjetry/work/vibe/probe-docker && sh -c 'set -e; for pair in v1.2.3=stable v1.2.3-rc.1=prerelease v1.2.3+build-5=stable v1.2.3-rc.1+b.2=prerelease; do got=$(make -s release-channel VERSION="${pair%%=*}"); [ "$got" = "${pair#*=}" ] || { echo "FAIL ${pair%%=*}: got $got, want ${pair#*=}"; exit 1; }; echo "ok ${pair%%=*} $got"; done' > /tmp/pd-t6-channel.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make -s release-channel > /tmp/pd-t6-channel-nover.log 2>&1; echo $?
```

Expected：第一条 0，四行 `ok`；第二条 2，日志含 `VERSION is required`。

注入：`git add Makefile` 建基准，把 `RELEASE_CHANNEL` 里的 `"$${v%%+*}"` 改成 `"$$v"`，`git diff --stat` 列出 `Makefile`，复跑第一条期望 1，失败于 `FAIL v1.2.3+build-5: got prerelease, want stable`（构建元数据里的 `-` 被当成预发布）。`git checkout -- Makefile`，复跑期望 0。

- [ ] **Step 3: 推送命令的展开**

`make -n` 只打印不执行（`$(MAKE)` 的子 make 同样只打印），可以不推送地核对 `latest` 的取舍：

```sh
cd /Users/xjetry/work/vibe/probe-docker && make -n docker-push VERSION=v1.2.3 > /tmp/pd-t6-push-stable.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make -n docker-push VERSION=v1.2.3-rc.1 > /tmp/pd-t6-push-pre.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && grep -e '--push' /tmp/pd-t6-push-stable.log > /tmp/pd-t6-push-stable.line 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && grep -e '--push' /tmp/pd-t6-push-pre.log > /tmp/pd-t6-push-pre.line 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && grep -c 'probe-hub:latest' /tmp/pd-t6-push-stable.line /tmp/pd-t6-push-pre.line > /tmp/pd-t6-latest-count.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make docker-push VERSION=v1.2.3+b1 > /tmp/pd-t6-push-plus.log 2>&1; echo $?
```

Expected：前四条 0；两个 `.line` 文件各一行，都含 `--platform linux/amd64,linux/arm64` 与 `-t ghcr.io/xjetry/probe-hub:<版本>`；第五条 0，计数为 stable 1、pre 0；最后一条 2，日志含 `cannot be an image tag`，没有任何 `go build` 与 `--push`。

注入：`git add Makefile`，`docker_latest` 里的 `filter stable` 改成 `filter prerelease`，`git diff --stat` 列出 `Makefile`，复跑前五条：计数变为 stable 0、pre 1。`git checkout -- Makefile`，复跑恢复 1 与 0。

- [ ] **Step 4: 回读脚本**

`scripts/docker-readback.sh`，写完 `chmod +x`：

```sh
#!/bin/sh
# 发布后回读 hub 镜像（spec §14）：release.yml 在 make docker-push 之后、gh release create 之前调用。
#   docker-readback.sh latest          打印 registry 上 latest 当前的 digest；取不到时打印 absent
#   docker-readback.sh verify BEFORE   回读刚推送的版本：匿名可取、各架构冒烟、latest 的指向；
#                                      BEFORE 是推送前 latest 子命令的输出
# 参数经 make docker-latest / make docker-readback 传入：镜像名、架构集合与预发布判定只在 Makefile 定义。
set -eu
: "${IMAGE_REPO:?IMAGE_REPO is required}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# 不存在与读取失败都记为 absent：对预发布，推送前取不到而推送后取到了，就是 latest 被移动，按失败处理；
# 两次都读取失败时无从比较，这是这项检查照不到的情形。
latest_digest() {
  docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$IMAGE_REPO:latest" || echo absent
}

case "${1:-}" in
latest)
  latest_digest
  ;;
verify)
  before=${2:-}
  [ -n "$before" ] || fail "verify needs the latest digest recorded before the push (or absent)"
  : "${VERSION:?VERSION is required}" "${CHANNEL:?CHANNEL is required}" "${ARCHES:?ARCHES is required}"
  image=$IMAGE_REPO:$VERSION
  # 匿名可取：新建的 ghcr 包默认私有时匿名 docker pull 会失败，与 Release 资产要求仓库公开（§14）同一理由。
  # 首次发布在这里失败时，把包的可见性改为公开后重跑 release job。
  anon=$(mktemp -d)
  DOCKER_CONFIG=$anon docker manifest inspect "$image" > /dev/null || fail "$image is not anonymously pullable; make the ghcr package public, then re-run the release job"
  for arch in $ARCHES; do
    docker pull --platform "linux/$arch" "$image"
    IMAGE=$image VERSION=$VERSION SMOKE_PLATFORM=linux/$arch "$(dirname "$0")/docker-smoke.sh"
  done
  tag_digest=$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$image")
  now=$(latest_digest)
  case $CHANNEL in
  stable) [ "$now" = "$tag_digest" ] || fail "latest points at $now, want $tag_digest" ;;
  prerelease) [ "$now" = "$before" ] || fail "a prerelease moved latest from $before to $now" ;;
  *) fail "CHANNEL must be stable or prerelease, got '$CHANNEL'" ;;
  esac
  echo "readback ok: $image ($CHANNEL, latest $now)"
  ;;
*)
  echo "usage: docker-readback.sh latest | verify BEFORE" >&2
  exit 2
  ;;
esac
```

本机能跑到的只有失败路径（包尚不存在）；成功路径首次在推 tag 的 release job 里运行：

```sh
cd /Users/xjetry/work/vibe/probe-docker && shellcheck -s sh scripts/docker-readback.sh > /tmp/pd-t6-sc.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && scripts/docker-readback.sh > /tmp/pd-t6-usage.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make -s docker-latest > /tmp/pd-t6-latest.out 2> /tmp/pd-t6-latest.err; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make docker-readback VERSION=v0.0.0-dev > /tmp/pd-t6-nobefore.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-docker && make docker-readback VERSION=v0.0.0-dev LATEST_BEFORE=absent > /tmp/pd-t6-missing.log 2>&1; echo $?
```

Expected：第一条 0。第二条 2（`IMAGE_REPO is required`）。第三条 0，`/tmp/pd-t6-latest.out` 恰为 `absent`，`.err` 里是 imagetools 取不到的报错（错误没有被吞）。第四条 2，日志含 `verify needs the latest digest`。第五条 2，日志含 `is not anonymously pullable`（包尚不存在，走的是首次发布时同一条失败路径）。

- [ ] **Step 5: release.yml**

查两个 action 的提交（v4 为主版本 tag；带注解的 tag 另有一行 `^{}`，用那一行的 SHA，没有就用 tag 本身那一行）：

```sh
git ls-remote https://github.com/docker/setup-qemu-action 'refs/tags/v4' 'refs/tags/v4^{}' > /tmp/pd-t6-qemu-sha.log 2>&1; echo $?
git ls-remote https://github.com/docker/login-action 'refs/tags/v4' 'refs/tags/v4^{}' > /tmp/pd-t6-login-sha.log 2>&1; echo $?
docker buildx imagetools inspect tonistiigi/binfmt:qemu-v10.2.3 --format '{{.Manifest.Digest}}' > /tmp/pd-t6-binfmt.log 2>&1; echo $?
```

Expected：都是 0。两份 SHA 日志各至少一行；binfmt 日志为 `sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0`（不同则说明该 tag 被重推，用日志里的值并在报告里写明）。三份输出原样附进实现报告。

`.github/workflows/release.yml` 改为（`<…>` 两处替换成上面查得的 40 位 SHA，其余逐字照写）：

```yaml
name: release
on:
  push:
    tags: ["v*"]
# contents: write 建 GitHub Release；packages: write 推 ghcr.io/xjetry/probe-hub。
permissions:
  contents: write
  packages: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5
        with:
          go-version-file: go.mod
      - uses: pnpm/action-setup@b906affcce14559ad1aafd4ab0e942779e9f58b1 # v4
        with:
          version: 12
      - uses: actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020 # v4
        with:
          node-version: 26
          cache: pnpm
          cache-dependency-path: web/pnpm-lock.yaml
      - uses: bufbuild/buf-action@8c6a16e16f12ba20b6470afa9c2ba9b5ba8c97c3 # v1
        with:
          setup_only: true
      - run: make ci
      # 与本地验收同一构建入口；版本注入与静态门禁都在 make release 里。
      - run: make release VERSION="$GITHUB_REF_NAME"
      # 回读要在 amd64 运行器上运行 arm64 镜像；构建本身不需要它（Dockerfile 的最终阶段没有 RUN）。
      # 排在登录之前：binfmt 容器以特权运行，此时运行器上还没有 ghcr 凭据。镜像按 digest 固定，不经 Actions 缓存。
      - uses: docker/setup-qemu-action@<setup-qemu-action v4 的 SHA> # v4
        with:
          image: docker.io/tonistiigi/binfmt:qemu-v10.2.3@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0
          platforms: arm64
          cache-image: false
      - uses: docker/login-action@<login-action v4 的 SHA> # v4
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ github.token }}
      - id: latest-before
        run: echo "digest=$(make -s docker-latest)" >> "$GITHUB_OUTPUT"
      # 镜像先于 GitHub Release 发布，gh release create 是最后一步：之前任何一步失败都能原样重跑本 job
      # （同一 tag 重推即覆盖）；反过来，Release 已建而镜像失败时，重跑会停在 release 已存在。
      - run: make docker-push VERSION="$GITHUB_REF_NAME"
      - run: make docker-readback VERSION="$GITHUB_REF_NAME" LATEST_BEFORE="$LATEST_BEFORE"
        env:
          LATEST_BEFORE: ${{ steps.latest-before.outputs.digest }}
      # prerelease 与镜像是否推 latest 读同一个判定（make release-channel）。
      - run: |
          case "$(make -s release-channel VERSION="$GITHUB_REF_NAME")" in
            prerelease) extra=--prerelease ;;
            stable) extra= ;;
            *) echo "make release-channel failed" >&2; exit 1 ;;
          esac
          gh release create "$GITHUB_REF_NAME" dist/* --verify-tag --title "$GITHUB_REF_NAME" ${extra:+"$extra"}
        env:
          GH_TOKEN: ${{ github.token }}
```

- [ ] **Step 6: actionlint 与注入**

```sh
cd /Users/xjetry/work/vibe/probe-docker && go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/release.yml .github/workflows/ci.yml > /tmp/pd-t6-actionlint.log 2>&1; echo $?
```

Expected：0。注入：`git add .github/workflows/release.yml`，把 `steps.latest-before.outputs.digest` 改成 `steps.latest-befor.outputs.digest`，`git diff --stat` 列出该文件，复跑期望非 0 且日志指出 `latest-befor` 未定义；`git checkout -- .github/workflows/release.yml`，复跑期望 0。

- [ ] **Step 7: lint 全量**

```sh
cd /Users/xjetry/work/vibe/probe-docker && make lint > /tmp/pd-t6-lint.log 2>&1; echo $?
```

Expected：0（shellcheck 覆盖两个新脚本）。

- [ ] **Step 8: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-docker && git add Makefile scripts/docker-readback.sh .github/workflows/release.yml && git commit -m "ci: 推 tag 时先发布并回读 hub 镜像，再建 GitHub Release" -m "预发布判定下沉到 Makefile 的 RELEASE_CHANNEL，Release 的 prerelease 与镜像是否推 latest 读同一处。make docker-push 先走完 make docker 的核对与冒烟再推送两个平台；回读按平台拉取并冒烟，核对匿名可取与 latest 的指向，通过后才 gh release create，之前任何一步失败都可原样重跑。QEMU 只供回读运行 arm64 镜像，binfmt 镜像按 digest 固定。成功路径首次在推 tag 时运行。"
```

---

### Task 7: CI 与 README

**Files:**
- Modify: `.github/workflows/ci.yml`
- Create 或 Modify: `README.md`（仓库根；另一份计划可能同时新建，见"执行顺序与并行"）

**Interfaces:**
- Consumes：`make docker`（Task 4、5）；Task 1、4、5 的行为与报错文字；Task 6 的 `latest` 口径。

- [ ] **Step 1: ci.yml**

在 `- run: make ci` 之后加：

```yaml
      # 镜像的构建与冒烟（§12）：两个平台构建并核对根文件系统，本机平台装进 docker 后冒烟；不推送。
      - run: make docker VERSION=v0.0.0-ci
```

耗时预期（写进提交信息）：这一步在 ubuntu-latest 上预计增加 2–4 分钟。依据：两份 `-trimpath` 的 hub 冷编译（与 `make ci` 的构建缓存键不同；本机 16 核冷缓存 8 s 与 6 s，运行器 4 vCPU，按慢 3–5 倍估计）、再跑一次面板构建、首次拉取 BuildKit 与 alpine 并建构建器（本机 7 s）、两次镜像构建（本机 8 s 与 6 s）、冒烟（三次 argon2id 哈希与三个工具容器）。本机 `make docker` 的实测墙钟见 Task 5 Step 5；首次 CI 运行后由控制端回读实际耗时。

- [ ] **Step 2: actionlint**

```sh
cd /Users/xjetry/work/vibe/probe-docker && go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/ci.yml .github/workflows/release.yml > /tmp/pd-t7-actionlint.log 2>&1; echo $?
```

Expected：0。注入：`git add .github/workflows/ci.yml`，把新加的 `- run:` 缩进少两格，`git diff --stat` 列出该文件，复跑期望非 0；`git checkout -- .github/workflows/ci.yml`，复跑期望 0。

- [ ] **Step 3: README**

```sh
cd /Users/xjetry/work/vibe/probe-docker && test -f README.md; echo $?
```

输出 1（不存在）时新建 `README.md`，内容为下面的标题行、简介行，再接 Docker 一节；输出 0 时只在文件末尾追加 Docker 一节，不动已有内容。

标题与简介（仅新建时）：

````markdown
# probe

自托管服务器监控探针：agent 采集主机指标并上报，hub 存储、展示并对外提供查询。
````

Docker 一节：

````markdown
## 用 Docker 运行 hub

镜像 `ghcr.io/xjetry/probe-hub:<版本>`，含 linux/amd64 与 linux/arm64。正式版本同时推为 `latest`；预发布版本（tag 含 `-`，如 `v0.2.0-rc.1`）不动 `latest`。镜像基于 `scratch`，只有静态链接的 `probe-hub`、CA 证书与 uid 65532 的非 root 用户，没有 shell。

```sh
docker volume create probe-data
docker run -d --name probe --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v probe-data:/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/xjetry/probe-hub:v0.1.0
```

默认参数是 `serve --db /data/probe.db --listen 0.0.0.0:8080`。镜像名之后写的任何参数都会替换这一整组默认参数，要加参数时连同默认的一起写全：

```sh
docker run -d --name probe --restart unless-stopped -p 127.0.0.1:8080:8080 -v probe-data:/data \
  ghcr.io/xjetry/probe-hub:v0.1.0 \
  serve --db /data/probe.db --listen 0.0.0.0:8080 --timezone Asia/Shanghai
```

### 设置管理员密码

hub 没有经网络的首次设置页，密码在容器里设置；hub 运行中设置即生效，不需要重启：

```sh
docker exec -it probe probe-hub passwd --db /data/probe.db
```

从脚本设置时经 stdin 传一行，必须带 `-i`。不带 `-i` 时容器里的 stdin 是空的，`passwd` 报 `no password on stdin` 并且不做任何修改：

```sh
printf '%s\n' "$ADMIN_PASSWORD" | docker exec -i probe probe-hub passwd --db /data/probe.db
```

### 反代与 `--trusted-proxies`

hub 只提供明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止。容器里监听 `0.0.0.0` 是预期的，启动日志里的 `listening on a non-loopback address` 告警照旧出现：能直连这个端口的人可以绕过反代并自带转发头。因此：

- 端口只发布到本机回环（`-p 127.0.0.1:8080:8080`，反代在宿主上），或者不发布端口，让反代容器与 hub 在同一个 Docker 网络里转发到 `http://probe:8080`。
- `--trusted-proxies` 写 hub 看到的反代地址（TCP 对端；CIDR 列表，逗号分隔）。只有来自这些地址的 `X-Forwarded-For`、`X-Forwarded-Proto` 才被采信。不设置时一律不信：登录失败锁定与按来源地址的限流都按反代的地址计，所有访客共用一个计数，会话 cookie 也不带 `Secure`。

反代与 hub 在同一个网络时，给网络固定网段、只让这两个容器加入，并信任这个网段：

```sh
docker network create --subnet 172.30.0.0/24 probe-net
docker run -d --name probe --restart unless-stopped --network probe-net -v probe-data:/data -e TZ=Asia/Shanghai \
  ghcr.io/xjetry/probe-hub:v0.1.0 \
  serve --db /data/probe.db --listen 0.0.0.0:8080 --trusted-proxies 172.30.0.0/24
# 反代容器以 --network probe-net 加入，把请求转给 http://probe:8080
```

### 时区

镜像里没有 `/etc/localtime`（时区数据已嵌入二进制）。不指定时区时，hub 按 UTC 判定流量周期的重置日，并在启动日志里告警 `host time zone could not be resolved; using UTC`。用 `-e TZ=Asia/Shanghai` 指定（不替换默认参数），或在写全的参数里加 `--timezone Asia/Shanghai`；两者都给时以 `--timezone` 为准。

### 环境变量

| 变量 | 作用 |
|---|---|
| `TZ` | IANA 时区名；没有给 `--timezone` 时使用 |
| `PROBE_OFFLINE_AFTER` | 节点离线判定时长（Go duration，`10s` 到 `3m`，默认 `30s`）；上报间隔、退避上限与告警宽限期的下限都由它推出 |

### 数据卷与备份

- `/data` 由 uid 65532 写入。新建的命名卷或匿名卷挂上时，Docker 把镜像里 `/data` 的属主带过去，无需处理。绑定宿主目录（`-v /srv/probe:/data`）时，目录须可被 uid 65532 写入（Linux 宿主上 `chown 65532:65532 /srv/probe`），否则 hub 启动即退出，报错 `open database /data/probe.db: …`。
- 库由 `probe.db` 与同名的 `probe.db-wal`、`probe.db-shm` 三个文件组成，已提交但尚未写回 `probe.db` 的数据只在 `-wal` 里：运行中只复制 `probe.db` 会丢数据。备份时先停容器，再把三个文件（或整个卷）一起复制：

```sh
docker stop probe
docker run --rm -v probe-data:/data -v "$PWD":/backup alpine:3.21 tar -C /data -czf /backup/probe-data.tgz .
docker start probe
```

### 排查

镜像里没有 shell，`docker exec probe sh` 不可用。可以：

- 看日志：`docker logs probe`。
- 查版本与各表行数：`docker exec probe probe-hub version`、`docker exec probe probe-hub stats --db /data/probe.db`。
- 看卷里的文件：挂同一个卷起一个带 shell 的临时容器，`docker run --rm -v probe-data:/data alpine:3.21 ls -ln /data`。
- 从 hub 自己的网络里发请求：`docker run --rm --network container:probe alpine:3.21 wget -qO /dev/null http://127.0.0.1:8080/admin/ && echo ok`。
````

- [ ] **Step 4: 实跑 README 的写法**

README 的每条可执行写法在本机镜像上跑一遍（镜像 tag 换成 `v0.0.0-dev`，名字加 `probe-readme` 前缀，只发布到回环的随机端口）。脚本放私有临时文件，不入库：

```sh
cat > /tmp/pd-t7-readme.sh <<'EOF'
set -eu
img=ghcr.io/xjetry/probe-hub:v0.0.0-dev
w=$(mktemp -d /tmp/pd-t7.XXXXXX)
cleanup() {
  docker rm -f probe-readme probe-readme-client > /dev/null 2>&1 || true
  docker volume rm probe-readme-data > /dev/null 2>&1 || true
  docker network rm probe-readme-net > /dev/null 2>&1 || true
}
trap cleanup EXIT
wait_listening() {
  deadline=$(($(date +%s) + 30))
  until docker logs probe-readme 2>&1 | grep -q 'hub listening'; do
    [ "$(date +%s)" -lt "$deadline" ] || { docker logs probe-readme; echo "FAIL: hub did not start"; exit 1; }
    sleep 0.5
  done
}
docker volume create probe-readme-data > /dev/null
docker run -d --name probe-readme -p 127.0.0.1::8080 -v probe-readme-data:/data -e TZ=Asia/Shanghai "$img" > /dev/null
wait_listening
docker logs probe-readme > "$w/logs" 2>&1
grep -q 'timezone=Asia/Shanghai' "$w/logs" || { echo "FAIL: TZ not applied"; exit 1; }
if grep -q 'host time zone could not be resolved' "$w/logs"; then echo "FAIL: UTC fallback despite TZ"; exit 1; fi
printf '%s\n' 'readme admin password 2026' | docker exec -i probe-readme probe-hub passwd --db /data/probe.db
docker exec probe-readme probe-hub version
docker exec probe-readme probe-hub stats --db /data/probe.db > "$w/stats"
grep -qx 'admin: 1' "$w/stats" || { echo "FAIL: password not stored"; exit 1; }
docker run --rm -v probe-readme-data:/data alpine:3.21 ls -ln /data
docker run --rm --network container:probe-readme alpine:3.21 wget -qO /dev/null http://127.0.0.1:8080/admin/
docker stop probe-readme > /dev/null
docker run --rm -v probe-readme-data:/data -v "$w":/backup alpine:3.21 tar -C /data -czf /backup/probe-data.tgz .
tar -tzf "$w/probe-data.tgz" > "$w/backup.list"
grep -qx './probe.db' "$w/backup.list" || { echo "FAIL: backup lacks probe.db"; exit 1; }
docker rm probe-readme > /dev/null
rc=0
docker run --rm "$img" --timezone Asia/Shanghai > "$w/override" 2>&1 || rc=$?
[ "$rc" = 2 ] && grep -q '^usage: probe-hub' "$w/override" || { echo "FAIL: arguments after the image did not replace the default command"; exit 1; }
docker network create --subnet 172.30.0.0/24 probe-readme-net > /dev/null
docker run -d --name probe-readme --network probe-readme-net -v probe-readme-data:/data "$img" \
  serve --db /data/probe.db --listen 0.0.0.0:8080 --trusted-proxies 172.30.0.0/24 > /dev/null
wait_listening
docker run --rm --name probe-readme-client --network probe-readme-net alpine:3.21 wget -qO /dev/null http://probe-readme:8080/admin/
echo "readme recipes ok: $w"
EOF
sh /tmp/pd-t7-readme.sh > /tmp/pd-t7-readme.log 2>&1; echo $?
```

Expected：0，日志末行 `readme recipes ok`。`docker network create` 报网段冲突（`Pool overlaps`）时，把脚本里的网段换成本机未用的 /24 重跑，并在报告里写明——README 的网段只是示例。

- [ ] **Step 5: 清理本计划留下的镜像 tag**

```sh
docker image rm ghcr.io/xjetry/probe-hub:v0.0.0-dev ghcr.io/xjetry/probe-hub:v0.0.0-inject ghcr.io/xjetry/probe-hub:v0.0.0-other > /tmp/pd-t7-rmi.log 2>&1; echo $?
docker image ls --format '{{.Repository}}:{{.Tag}}' ghcr.io/xjetry/probe-hub > /tmp/pd-t7-left.log 2>&1; echo $?
```

Expected：第一条可能因某个 tag 不存在而非 0（日志逐个说明）；第二条 0 且日志为空。构建器 `probe-hub-buildkit-v0.33.0` 保留，下次 `make docker` 复用。

- [ ] **Step 6: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-docker && git add .github/workflows/ci.yml && git commit -m "ci: 每次构建并冒烟 hub 镜像" -m "make docker VERSION=v0.0.0-ci：两个平台构建并核对根文件系统，本机平台冒烟，不推送。预计为 ubuntu-latest 增加 2–4 分钟，主要是两份 -trimpath 的 hub 冷编译与冒烟；本机实测 <秒数> 秒。"
cd /Users/xjetry/work/vibe/probe-docker && git add README.md && git commit -m "docs: README 增加用 Docker 运行 hub 的一节" -m "docker run、经 docker exec 设密码（脚本里必须 -i）、反代与 --trusted-proxies、时区、环境变量、数据卷属主与备份（probe.db 与 -wal、-shm 一起复制）、无 shell 时的排查办法。每条写法在本机镜像上实跑过。"
```

（`<秒数>` 换成 Task 5 Step 5 实测的 `real` 值，提交信息里只留数字；Step 4 的网段若换过，README 仍保留示例网段，报告里写明。）

---

## 执行顺序与并行

- 任务依赖：Task 1、2、3 互不依赖；Task 4 依赖 2、3；Task 5 依赖 1、4；Task 6 依赖 5；Task 7 依赖 5（CI 跑 `make docker`）与 6（README 的 `latest` 口径）。单一实现者按 1→7 顺序做。
- 与 macOS agent 计划（工作树 `/Users/xjetry/work/vibe/probe-macos`，分支 `m5m6-macos`）：两边都改 `Makefile`（release 配方、`.PHONY`、lint 的 shellcheck 清单）、`.github/workflows/release.yml`、`.github/workflows/ci.yml`、`README.md`。后合并者变基到 main，按下面合并，再重跑验证：
  - Makefile：两边都保留。macOS 的 darwin 构建行改用 `$(RELEASE_GOFLAGS)`——变基后 `grep -n -- '-trimpath' Makefile` 只应命中 `RELEASE_GOFLAGS` 的定义那一行；`.PHONY` 与 shellcheck 清单取并集。
  - release.yml：两边的步骤都保留，`gh release create … dist/*` 仍是最后一步；darwin 资产随 `dist/*` 上传，镜像构建上下文在 `build/` 下不受影响。
  - ci.yml：macOS runner 的 job 与本计划的 `make docker` 步骤并存。
  - README.md：两份计划都可能新建它，合成一份：一个标题与简介，macOS 安装一节与 Docker 一节并列。
  - 变基后重跑：`make ci`、`make release VERSION=v0.0.0-rc1`、`make docker VERSION=v0.0.0-dev`、两个 workflow 的 actionlint。
- 与公开页计划（`/Users/xjetry/work/vibe/probe-public`，分支 `m5m6-public`）：它改 `cmd/hub/serve.go` 与 web；本计划 Task 1 改 `store.Open` 与 `passwd.go`，Task 5 的注入临时改 `serve.go` 但不提交，重叠小。两者合并后，README 的环境变量表与参数说明按当时的主干重新枚举（`grep -rn 'os.Getenv' cmd/hub internal/hub`、`probe-hub serve -h`），镜像默认 CMD 是否需要新参数（如 `--public-dir`）由控制端决定；checkimage 的清单若因此要加条目，与 Dockerfile 同步改。
- 合并前固定三步：`git log --oneline $(git merge-base main HEAD)..main` 读标题找同类实现；`git merge-tree --write-tree --name-only main HEAD` 无副作用预演冲突；生成物冲突不手解，改源文件重跑 `make gen`。

## 首次发布检查清单（控制端）

release job 的成功路径（推送、回读、`latest`）本机无法演练，首次推 tag 时核对：

1. `github.com/xjetry/probe` 已公开（§14）。
2. 新建的 ghcr 包默认私有时，release job 会在 `make docker-readback` 的匿名检查处失败，此时 Release 尚未创建：把 `ghcr.io/xjetry/probe-hub` 的可见性改为 Public，重跑 job。
3. 包页面显示关联到 `xjetry/probe`（`org.opencontainers.image.source` 标签）。
4. 另一台机器上匿名 `docker pull ghcr.io/xjetry/probe-hub:<tag>`（正式版本另拉 `:latest`），`docker run --rm ghcr.io/xjetry/probe-hub:<tag> version` 打印 `<tag>`。
5. job 日志里两个架构的冒烟都以 `docker smoke passed` 结束，回读以 `readback ok` 结束。

## spec 待同步（由控制端决定是否回写）

1. §14 hub Docker 镜像：二进制在 `/usr/local/bin/probe-hub`（`docker exec … probe-hub` 按名字执行）；镜像另含空的 `/data`（属主 65532）与 `/tmp`（1777，SQLite 临时文件）。
2. §14 发布：带构建元数据（`+`）的 tag 不能成为镜像 tag，`make docker`/`docker-push` 拒绝，这类 tag 在发布流水线里整体失败；镜像先于 GitHub Release 推送并回读；预发布判定在 Makefile 的 `RELEASE_CHANNEL`；新建的 ghcr 包首次发布须设为公开。
3. §14 构建：镜像的构建与推送都在 Makefile，构建器是按 digest 固定的 BuildKit；release 流水线不用 build-push-action。
4. §12 Docker 镜像：冒烟要求 `/admin/` 200 且为面板页（503 表示面板没构建进二进制），另有根文件系统的逐条目核对（`scripts/checkimage`）。

## 自查记录

1. spec 覆盖：§14 hub Docker 镜像条 → Task 4（Dockerfile：scratch、CA、非 root、`/data`、默认参数、时区不装 tzdata）、Task 5（冒烟：`/admin`、`passwd`、告警）、Task 6（buildx 发布两个平台、预发布不打 latest）、Task 7（README 的反代、`--trusted-proxies`、`passwd`、时区）；§14 发布条 → Task 3（构建参数一处）、Task 6（与 `gh release create` 的先后、prerelease 判定一处）；§14 静态门禁 → Task 4 的 `docker` 目标对镜像二进制跑 checkstatic；§12 Docker 镜像条 → Task 5；§12 缺陷注入 → 每个任务的注入步骤；§5.4 → 冒烟的告警断言与 README；§5.3 → `passwd` 只经 `docker exec`。
2. 占位扫描：除任务说明要求实现者自查的两个 action SHA（Task 6 Step 5 给出查法与替换位置）与两处实测墙钟（Task 5 Step 5 产出）外，没有待定内容。
3. 名称一致：`hub_build`、`RELEASE_GOFLAGS`、`DOCKER_IMAGE`、`DOCKER_PLATFORMS`、`DOCKER_BUILDER`、`docker_build`、`check_image_version`、`RELEASE_CHANNEL`、`docker_latest`、`IMAGE_BIN_DIR`、`errNoPasswordInput`、脚本环境变量 `IMAGE`/`VERSION`/`SMOKE_PLATFORM`/`IMAGE_REPO`/`CHANNEL`/`ARCHES` 在定义处与使用处一致；报错文字 `no password on stdin`、`open database /data/probe.db` 在 Task 1 产出、Task 5 与 README 引用。
4. Review Focus：五条各有落点（见该节），其中第 3 条的行为证据是一次性实验，回归由 checkimage 的结构断言承载。

## 执行修正（执行与整分支审阅后记录；代码以分支为准）

**计划文字与实测不符（没改行为）**
- Task 3 Step 5：带 `-trimpath` 时 go1.27.1 的 buildinfo 不记录 `-ldflags`，`go version -m` 看不到版本注入；核对版本改为运行二进制的 `version`（独立复现：同一程序单独 `-ldflags` 时 buildinfo 有该行，加 `-trimpath` 后消失，两种构建运行时都打印注入值）。
- Task 4 Step 6：docker-container 驱动的 `--load` 打印 `importing to docker`，不是 `naming to`；装入与否以 `docker image inspect` 为准。
- Task 6 Step 4：回读脚本无参数时 macOS `/bin/sh` 退出 1、dash 退出 2，差别只在 shell。
- Task 2 注入 (d) 按计划写法会编译失败（红因不对），改了注入方式重做。

**源自计划原文、执行后审阅改掉的缺陷**
- Task 2 `checkimage`：`run` 的"清单为空等价于全部通过"不成立（unexpected platform 循环承载该防线）；"只有 ELF 头能揭示"是排他句；`tmp` 注释把临时表写成一律报错（§14：页缓存装不下才写临时文件，小查询不触发）；group、passwd 主组、passwd 列数、read tar 出错报告四条断言没有测试钉住；类型改按 `Typeflag` 核对、权限只比 `07777`；`./` 前缀归一化删除（真实导出没有这种前缀）。
- Task 4：`check_image_version` 把 `$(VERSION)` 拼进 shell 源码（`v1'x'` 拼成 `v1x` 通过）且 `grep -x` 按行放行；改为读配方环境按字节判定。`COPY` 不带 `--chmod` 时权限位随构建机 umask（002 的桌面 Linux 会在 checkimage 处失败）。
- Task 5 冒烟：Docker 29.4.0 在 exec 失败时把 OCI 报错写到 stdout，两个流一起落文件；hub 启动即退出时先判容器是否已退出再查端口；运行被测镜像必须 `--pull=never`（否则本机无该 tag 时拉 ghcr 已发布镜像冒烟，版本断言照样通过）；四个一次性容器要带运行前缀并列入清理；docker stop 退出码逐条区分（1 关停路径报错、137 超宽限期被 SIGKILL、143 未订阅 SIGTERM）。
- Task 6 回读：`docker-latest`/`LATEST_BEFORE` 的"推送前记下 latest"判定在读取失败时误报或放行，删除；改为直接性质判定（stable：latest == 本次 digest；prerelease：latest 不存在或 ≠ 本次），"不存在"只认 buildx 的 `not found` 报错，其余读取失败一律 FAIL。`latest` 改为回读通过后才由 `docker-promote` 用 `imagetools create` 按已回读的 digest 移动，再回读 latest；单源为索引时 `imagetools create` 不改 digest（单平台清单为源会得到新索引），promote 仍回读确认。回读逐平台按平台清单 digest 拉取并冒烟（同一索引 digest 按两个平台先后拉取会 `cannot overwrite digest`），根文件系统经构建器 `FROM <repo>@<索引 digest>` 导出两平台 tar 交同一个 checkimage——不用 `docker export`（带 11 个运行时注入条目，需要第二份忽略清单）；导出由 digest 寻址承载内容一致，构建器本地已有同 digest 的 blob 时不一定重新下载。release.yml 加 `persist-credentials: false`、`concurrency: { group: release }`、`timeout-minutes`；registry 由 `make -s docker-registry` 打印、login 引用它。
- VERSION 消费面收口：make 先展开整条配方再执行第一行，配方第一行的 shell 检查拦不住同一配方后面的 `$(shell)`，所以在解析 Makefile 时对 `$(value VERSION)` 含 `$` 用 `$(error)` 拒绝；shell 层只有一份 `check_version`，release / docker / docker-smoke / docker-push / docker-readback / docker-promote / release-channel 第一行都调它，release 与 docker 同一规则；`RELEASE_CHANNEL` 改为 make 文本函数；`SMOKE_PLATFORM` 等命令行变量由 make 导出到配方环境，不再拼进 shell。这些规则与回读判定各有入库的桩测试（`make ci` 的 `script-test`）。
- 冒烟的工具镜像按索引 digest 引用、不指定平台（经典镜像存储里同一索引 digest 已绑定某架构时按别的平台拉取报 `cannot overwrite digest`；逐次解析平台清单会撞 Docker Hub 匿名限流）；alpine 的 digest 只定义一处，Dockerfile 经 `--build-arg` 取得。

**首次发布检查清单补充**
6. 回读把"latest 不存在"限定为 buildx 报 `<引用>: not found`，运行器上的 buildx 版本若措辞不同，预发布的 `docker-promote` 会以 cannot read latest 失败（封闭方向）：看日志，必要时把该版本的措辞加进判定。
7. job 日志里 `docker-readback` 应有两个平台各一次 `docker smoke passed`、checkimage 无问题行、`readback ok`；正式版本另有 `docker-promote` 的 `pushing … to …:latest` 与回读 latest == 该 digest。
8. 一次推多个 tag 时同组运行串行，排队中被替换而取消的 tag 需手工重跑。
