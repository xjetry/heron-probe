export CGO_ENABLED=0

# 版本号（VERSION）的两层守卫。不变式：make 展开后的 $(VERSION) 与调用方给的原文逐字相同，且只含
# [A-Za-z0-9_.-]、首字符不是 . 或 -、至多 128 字节。发布物的版本号与镜像 tag 取展开后的 $(VERSION)，
# 而 GitHub Release 建在 release.yml 的原始 tag 名上：展开与原文不同时，二进制、镜像 tag 与 Release 就指向
# 不同的版本号，这正是守卫存在的理由。
#
# make 层：原文里的 $ 会被当作变量或函数引用展开，$(shell …) 在每次引用处执行。make 在执行配方第一行
# 之前就展开整条配方，配方里的 shell 检查拦不住同一配方后面的引用，所以在解析 Makefile 时按未展开的
# 原文（$(value VERSION)）拒绝。这之后 $(VERSION) 的展开是恒等的。
ifneq ($(findstring $$,$(value VERSION)),)
$(error VERSION '$(value VERSION)' contains '$$', which make would expand as a variable or function reference; use a plain version such as v0.1.0)
endif

# shell 层（check_version）：每个消费 VERSION 的目标第一行调用它，只此一份。镜像 tag 与版本号逐字相同
# （probe-hub version 打印的就是 tag），Docker 的 tag 只允许 [A-Za-z0-9_.-]、首字符不为 . 与 -、至多 128 个
# 字符；带构建元数据（+）的 tag 因而不能成为镜像 tag，§14 规定这类 tag 的发布整体失败，release 的 tar 包
# 与镜像用同一规则，不单独放宽。
# 检查读配方环境里的 $$VERSION，不把 $(VERSION) 拼进 shell 源码：git tag 名允许 ' ( 等字符，拼进去的值
# 能改写检查本身（v1'x' 在单引号里拼接后成了 v1x）。make 把命令行与环境给出的 VERSION 放进配方环境：
# 命令行来源放的是展开值，环境来源放的是原文（GNU make 3.81 与 4.x 实测），二者与原文相同都由上面的
# make 层守卫承载；若日后改为在 Makefile 里赋值而不导出，这里读到空值、报 VERSION is required，不会放行。
# 按字节判断而不用 grep：grep 按行匹配，任一行合法即整体放行，带换行的值会漏过。删掉允许的字节后只应
# 剩下末尾的哨兵 /（删完后剩下的换行都落在末尾，没有哨兵就会被命令替换吞掉）；shell 的 ${VERSION##[.-]*}
# 为空即首字符是 . 或 -。通过之后 $(VERSION) 只含 [A-Za-z0-9_.-]，配方里其余位置直接展开它才是安全的。
check_version = if [ -z "$$VERSION" ]; then echo "VERSION is required, e.g. VERSION=v0.1.0" >&2; exit 1; fi; \
	if [ "$$(printf '%s/' "$$VERSION" | LC_ALL=C tr -d 'A-Za-z0-9_.-')" != / ] || [ -z "$${VERSION\#\#[.-]*}" ] || [ $${\#VERSION} -gt 128 ]; then \
	  echo "VERSION '$$VERSION' cannot be an image tag: only [A-Za-z0-9_.-], not starting with . or -, at most 128 characters, no + build metadata" >&2; exit 1; fi

.PHONY: gen lint test build binaries ci e2e e2e-matrix fixtures web-install web-test web release script-test docker docker-smoke release-channel docker-registry docker-push docker-readback docker-promote

web-install:
	pnpm --dir web install --frozen-lockfile

# TS 客户端与 Go 代码同一口径：都由 buf 生成并入库，ci 要求生成目录没有改动或未跟踪文件。
gen: web-install
	buf generate

lint:
	go mod tidy -diff
	buf lint
	shellcheck -s sh deploy/install.sh deploy/openrc/probe-agent scripts/docker-smoke.sh scripts/docker-readback.sh scripts/docker-readback-test.sh scripts/release-rules-test.sh scripts/image-platform-ref.sh scripts/docker-builder.sh
	go vet ./...
	GOOS=linux go vet ./...
	GOOS=darwin go vet ./...

test:
	go test -count=1 ./...

# 发布规则（版本号守卫、预发布判定）与回读判定的回归检查：只跑 make 的检查、-n 展开与 docker 桩，
# 不构建、不访问 registry。
script-test:
	MAKE='$(MAKE)' scripts/release-rules-test.sh
	scripts/docker-readback-test.sh

web-test: web-install
	pnpm --dir web exec vitest run

# 两个入口的产物落在 internal/hub/web/dist（面板）与 dist-public（公开页）供 go:embed；不入库，缺产物时 hub 也能编译并给出说明页。
web: web-install
	pnpm --dir web run build

# build 验证全部已有的包在本机以及 Linux amd64、arm64 上都能编译；
# 二进制产物由 binaries 生成，只有 e2e 需要它。
build:
	go build ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...

binaries: web
	go build -o bin/probe-hub ./cmd/hub
	GOOS=linux GOARCH=amd64 go build -o bin/probe-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o bin/probe-agent-linux-arm64 ./cmd/agent
	go run ./scripts/checkstatic bin/probe-agent-linux-amd64 bin/probe-agent-linux-arm64

ci: gen lint test script-test web-test web build
	status="$$(git status --porcelain -- gen web/src/gen)" || exit $$?; \
	if [ -n "$$status" ]; then printf '%s\n' "$$status"; exit 1; fi

fixtures:
	scripts/capture-proc.sh docker-debian

# 一级发行版每次都跑；二级发版前跑。镜像与期望系统名成对，前一个失败就停。
E2E_TIER1 := debian:bookworm-slim=Debian alpine:3.21=Alpine
E2E_TIER2 := ubuntu:24.04=Ubuntu rockylinux:9=Rocky

e2e: binaries
	@for pair in $(E2E_TIER1); do \
	  AGENT_IMAGE="$${pair%%=*}" EXPECT_OS="$${pair#*=}" scripts/e2e.sh || exit $$?; \
	done

e2e-matrix: binaries
	@for pair in $(E2E_TIER1) $(E2E_TIER2); do \
	  AGENT_IMAGE="$${pair%%=*}" EXPECT_OS="$${pair#*=}" scripts/e2e.sh || exit $$?; \
	done

# 发布产物矩阵：agent 五个 Linux 架构，hub 两个。架构集合只在这两个变量维护，
# 静态门禁与打包清单都由它们展开，不存在第二份文件清单。
AGENT_LINUX_ARCHES := amd64 arm64 armv7 386 riscv64
HUB_LINUX_ARCHES := amd64 arm64

# 发布产物的构建参数（§14）：版本经 ldflags 注入，-trimpath 去掉构建机路径。agent 与 hub、
# tar 包与镜像里的 hub 都经它构建，任何一种产物都不会单独漂移。
RELEASE_GOFLAGS = -trimpath -ldflags "-X main.version=$(VERSION)"

# 一个 Linux hub 二进制：$(1) 为 GOARCH，$(2) 为输出路径。release 打包与 docker 镜像都调用它。
hub_build = env GOOS=linux GOARCH=$(1) CGO_ENABLED=0 go build $(RELEASE_GOFLAGS) -o "$(2)" ./cmd/hub

# 本地验收与线上发布走同一目标，产物与版本注入完全一致（release.yml 只调用它）。
# 打包的 tar 前设 COPYFILE_DISABLE=1：macOS 的 bsdtar 否则会把扩展属性打成 ._* 条目，busybox 解包会带出多余文件。
# 另加 --no-xattrs：bsdtar 仍会把 com.apple.provenance 之类的扩展属性写成 pax 扩展头，GNU tar 解包时逐条目告警，产物里也带上宿主元数据；
# bsdtar 与 GNU tar 都认这个选项，本地与 CI 构建同一写法。
# 说明写在 recipe 之外：recipe 是反斜杠续行拼成的一条 shell 命令，行内的 # 会把其后的续行一并注释掉。
# VERSION 在构建前端之前检查：不合规时立即报错，不等 web 目标跑完。
release:
	@$(check_version)
	$(MAKE) web
	rm -rf dist/build dist/*.tar.gz dist/SHA256SUMS dist/install.sh
	mkdir -p dist/build
	@set -e; for arch in $(AGENT_LINUX_ARCHES); do \
	  case $$arch in armv7) gflags="GOARCH=arm GOARM=7" ;; *) gflags="GOARCH=$$arch" ;; esac; \
	  env GOOS=linux CGO_ENABLED=0 $$gflags go build $(RELEASE_GOFLAGS) -o "dist/build/probe-agent-linux-$$arch" ./cmd/agent; \
	done; \
	for arch in $(HUB_LINUX_ARCHES); do \
	  $(call hub_build,$$arch,dist/build/probe-hub-linux-$$arch); \
	done
	go run ./scripts/checkstatic $(addprefix dist/build/probe-agent-linux-,$(AGENT_LINUX_ARCHES)) $(addprefix dist/build/probe-hub-linux-,$(HUB_LINUX_ARCHES))
	@set -e; for arch in $(AGENT_LINUX_ARCHES); do \
	  pkg="dist/pkg-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/probe-agent-linux-$$arch" "$$pkg/probe-agent"; \
	  cp deploy/systemd/probe-agent.service "$$pkg/probe-agent.service"; \
	  cp deploy/openrc/probe-agent "$$pkg/probe-agent.openrc"; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/probe-agent_linux_$$arch.tar.gz" probe-agent probe-agent.service probe-agent.openrc; \
	  rm -rf "$$pkg"; \
	done; \
	for arch in $(HUB_LINUX_ARCHES); do \
	  pkg="dist/pkg-hub-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/probe-hub-linux-$$arch" "$$pkg/probe-hub"; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/probe-hub_linux_$$arch.tar.gz" probe-hub; \
	  rm -rf "$$pkg"; \
	done; \
	rm -rf dist/build
	cp deploy/install.sh dist/install.sh
	cd dist && sha256sum probe-*.tar.gz > SHA256SUMS

# hub 镜像（§14）：ghcr.io/xjetry/probe-hub:<version>，平台由 HUB_LINUX_ARCHES 展开。
# 镜像里不编译 Go：hub 二进制经 hub_build 构建到 IMAGE_BIN_DIR，Dockerfile 按 TARGETARCH 取用。
DOCKER_IMAGE := ghcr.io/xjetry/probe-hub
# registry 主机只由 DOCKER_IMAGE 推出；release.yml 登录时经 make docker-registry 取用，不另写一份。
DOCKER_REGISTRY = $(firstword $(subst /, ,$(DOCKER_IMAGE)))
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
# 同名构建器由 scripts/docker-builder.sh 核对驱动与镜像后才沿用（只改 digest 时名字不变），不存在时创建。
BUILDKIT_VERSION := v0.33.0
BUILDKIT_IMAGE := moby/buildkit:$(BUILDKIT_VERSION)@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3
DOCKER_BUILDER := probe-hub-buildkit-$(BUILDKIT_VERSION)
# alpine 按 digest 固定，只在这里定义：Dockerfile 的 rootfs 阶段经 --build-arg 取用，冒烟的工具镜像
# （读卷属主、预置不可写的卷、在 hub 的网络命名空间里发请求）经环境变量 TOOL_IMAGE 取用。
ALPINE_IMAGE := alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507
docker_build = docker buildx build --builder $(DOCKER_BUILDER) -f Dockerfile --build-arg ALPINE_IMAGE=$(ALPINE_IMAGE)
ensure_builder = scripts/docker-builder.sh $(DOCKER_BUILDER) $(BUILDKIT_IMAGE)

# 本地构建并核对（§14）：两个平台都构建、导出根文件系统交给 checkimage，再把本机平台装进 docker。
# 多平台结果不能 --load：经典镜像存储不接受多平台索引（docker exporter does not currently support
# exporting manifest lists），不给 --platform 时构建的是构建节点的本机平台。
# 面板随 go:embed 进二进制，先 make web：漏掉它，镜像里的 /admin/ 只有 503 说明页。
docker:
	@$(check_version)
	$(MAKE) web
	rm -rf $(IMAGE_BIN_DIR)
	@set -e; for arch in $(HUB_LINUX_ARCHES); do \
	  mkdir -p "$(IMAGE_BIN_DIR)/linux/$$arch"; \
	  $(call hub_build,$$arch,$(IMAGE_BIN_DIR)/linux/$$arch/probe-hub); \
	done
	go run ./scripts/checkstatic $(foreach a,$(HUB_LINUX_ARCHES),$(IMAGE_BIN_DIR)/linux/$(a)/probe-hub)
	$(ensure_builder)
	$(docker_build) --platform $(DOCKER_PLATFORMS) --output type=tar,dest=$(IMAGE_BIN_DIR)/rootfs.tar .
	go run ./scripts/checkimage $(IMAGE_BIN_DIR)/rootfs.tar $(HUB_LINUX_ARCHES)
	$(docker_build) -t $(DOCKER_IMAGE):$(VERSION) --load .
	$(MAKE) docker-smoke

# 冒烟本机 docker 里已有的 $(DOCKER_IMAGE):$(VERSION)：make docker 构建后调用；发布后回读时先按平台
# docker pull，再由回读脚本以同样的环境变量直接调用脚本。VERSION 与 SMOKE_PLATFORM（为空时用 docker 的
# 默认平台）由 make 从命令行或环境放进配方环境，脚本直接读，不在这里重新赋值拼进 shell 源码。
docker-smoke:
	@$(check_version)
	IMAGE='$(DOCKER_IMAGE):$(VERSION)' TOOL_IMAGE='$(ALPINE_IMAGE)' scripts/docker-smoke.sh

# 预发布判定（§14）：去掉构建元数据（+ 及之后）后仍含 - 就是预发布。GitHub Release 是否标为 prerelease、
# 镜像是否移动 latest 都读它，判定只在这一处。纯文本函数，不经 shell：make 展开配方时就求值，早于配方
# 里的 check_version，求值本身不能执行任何东西。
RELEASE_CHANNEL = $(if $(findstring -,$(firstword $(subst +, ,$(VERSION)))),prerelease,stable)

release-channel:
	@$(check_version)
	@echo $(RELEASE_CHANNEL)

docker-registry:
	@echo $(DOCKER_REGISTRY)

# 发布镜像，release.yml 依次调用下面三个目标，最后才 gh release create。不变式：latest 只会指向回读
# 通过的镜像；回读核对的是 registry 上实际存在的内容，不是构建时的中间产物。
#
# docker-push：先走完 make docker（两个平台的根文件系统核对、本机平台冒烟），再推送两个平台的版本 tag，
# 不碰 latest。推送沿用 make docker 的构建器与缓存，构建参数同一处。
docker-push:
	@$(check_version)
	$(MAKE) docker
	$(docker_build) --platform $(DOCKER_PLATFORMS) -t $(DOCKER_IMAGE):$(VERSION) --push .

# docker-readback：回读 registry 上版本 tag 指向的索引（匿名可取、逐平台冒烟、逐平台核对根文件系统），
# 通过后把 <版本> <索引 digest> 记到 READBACK_RECORD。
# docker-promote：只认这份记录。正式版本把 latest 移到记录里的 digest 并回读确认；预发布确认 latest
# 没有指向本次的 digest。
READBACK_RECORD := build/readback-record
docker-readback:
	@$(check_version)
	$(ensure_builder)
	mkdir -p build/tools
	go build -o build/tools/checkimage ./scripts/checkimage
	IMAGE_REPO=$(DOCKER_IMAGE) ARCHES='$(HUB_LINUX_ARCHES)' BUILDER=$(DOCKER_BUILDER) TOOL_IMAGE='$(ALPINE_IMAGE)' \
	  SMOKE=scripts/docker-smoke.sh CHECKIMAGE=build/tools/checkimage RECORD=$(READBACK_RECORD) scripts/docker-readback.sh verify

docker-promote:
	@$(check_version)
	IMAGE_REPO=$(DOCKER_IMAGE) CHANNEL=$(RELEASE_CHANNEL) RECORD=$(READBACK_RECORD) scripts/docker-readback.sh promote
