export CGO_ENABLED=0

.PHONY: gen lint test build binaries ci e2e e2e-matrix fixtures web-install web-test web release docker

web-install:
	pnpm --dir web install --frozen-lockfile

# TS 客户端与 Go 代码同一口径：都由 buf 生成并入库，ci 要求生成目录没有改动或未跟踪文件。
gen: web-install
	buf generate

lint:
	go mod tidy -diff
	buf lint
	shellcheck -s sh deploy/install.sh deploy/openrc/probe-agent
	go vet ./...
	GOOS=linux go vet ./...
	GOOS=darwin go vet ./...

test:
	go test -count=1 ./...

web-test: web-install
	pnpm --dir web exec vitest run

# 产物落在 internal/hub/web/dist 供 go:embed；不入库，缺产物时 hub 也能编译并给出说明页。
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

ci: gen lint test web-test web build
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
# VERSION 在构建前端之前检查：缺参时立即报错，不等 web 目标跑完。
release:
	@if [ -z "$(VERSION)" ]; then echo "VERSION is required, e.g. make release VERSION=v0.1.0" >&2; exit 1; fi
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
# 检查从配方环境读 $$VERSION，不把 $(VERSION) 拼进 shell 源码：git tag 名允许 ' $ ( 等字符，拼进去的值
# 能改写检查本身（v1'x' 在单引号里拼接后就成了 v1x）。make 把命令行与环境给出的 VERSION 都放进配方
# 环境；若日后改为在 Makefile 里赋值而不导出，这里读到空值、报 VERSION is required，不会放行。
# 通过之后 VERSION 只含 [A-Za-z0-9_.-]，配方里其余位置直接展开 $(VERSION) 才是安全的。
# 字符集按字节判断而不按行匹配：grep -x 逐行比对，带换行的值每一行各自合法就会整体放行。删掉允许的
# 字节后只应剩下末尾的哨兵 /（哨兵让结尾的换行不被命令替换吞掉）；${VERSION##[.-]*} 为空即首字符是 . 或 -。
check_image_version = if [ -z "$$VERSION" ]; then echo "VERSION is required, e.g. make docker VERSION=v0.1.0" >&2; exit 1; fi; \
	if [ "$$(printf '%s/' "$$VERSION" | LC_ALL=C tr -d 'A-Za-z0-9_.-')" != / ] || [ -z "$${VERSION\#\#[.-]*}" ] || [ $${\#VERSION} -gt 128 ]; then \
	  echo "VERSION '$$VERSION' cannot be an image tag: only [A-Za-z0-9_.-], not starting with . or -, at most 128 characters, no + build metadata" >&2; exit 1; fi

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
