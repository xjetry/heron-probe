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

# hub 绑定的 agent 版本（§14.1）。默认取仓库根 AGENT_VERSION 文件，它是绑定的唯一事实源；命令行可另给，本地
# 验收构建给出与 VERSION 相同的值以产出完整的一套。它经 ldflags 进入 hub 的构建命令，与 VERSION 同理在解析时
# 按原文拒绝 $：命令行给出的值是递归展开变量，其中的 $(shell …) 会在每次引用处执行。用 := 读文件：环境里的
# 同名变量不顶替仓库文件（makefile 里的赋值优先于环境），只有命令行显式给出才覆盖。导出到配方环境：配方一律经
# 环境变量 $$AGENT_VERSION 引用它，不把 $(AGENT_VERSION) 展开拼进 shell 源码，值里的引号或分号因而改写不了命令。读文件只用 shell 内建（read、printf）：
# 解析 Makefile 时就会执行这一行，而发布规则测试在只放了少数只读工具的 PATH 下运行 make（scripts/release-rules-test.sh）。
AGENT_VERSION := $(strip $(shell read -r v < AGENT_VERSION; printf '%s' "$$v"))
ifneq ($(findstring $$,$(value AGENT_VERSION)),)
$(error AGENT_VERSION '$(value AGENT_VERSION)' contains '$$', which make would expand as a variable or function reference; use a plain version such as v0.1.0)
endif
export AGENT_VERSION

# shell 层（check_version）：每个消费 VERSION 的目标第一行调用它，只此一份。镜像 tag 与版本号逐字相同
# （heron-hub version 打印的就是 tag），Docker 的 tag 只允许 [A-Za-z0-9_.-]、首字符不为 . 与 -、至多 128 个
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

.PHONY: gen lint test build hub-binary binaries ci e2e e2e-matrix compat-e2e bound-agent-e2e fixtures web-install web-lint web-test web-e2e web release-full release-hub-only release-kind agent-version script-test docker docker-smoke release-channel docker-registry docker-push docker-readback docker-promote

web-install:
	pnpm --dir web install --frozen-lockfile

# TS 客户端与 Go 代码同一口径：都由 buf 生成并入库，ci 要求生成目录没有改动或未跟踪文件。
gen: web-install
	buf generate

# buf breaking 对照的分支：本地默认是本仓库的 main（任务分支在工作树里跑 lint 就能发现破坏兼容的 proto 改动）；
# CI 的 pull_request 检出没有本地 main，由 ci.yml 传 origin/main。在 main 自身上跑是与自己比较，恒通过。
BUF_AGAINST_BRANCH ?= main
lint:
	go mod tidy -diff
	buf lint
	buf breaking --against ".git#branch=$(BUF_AGAINST_BRANCH)"
	@unformatted="$$(gofmt -l $$(git ls-files '*.go'))"; if [ -n "$$unformatted" ]; then printf 'gofmt: %s\n' $$unformatted >&2; exit 1; fi
	shellcheck -s sh deploy/install.sh deploy/install-hub.sh deploy/install-macos.sh deploy/openrc/heron-agent scripts/docker-smoke.sh scripts/docker-readback.sh scripts/docker-readback-test.sh scripts/release-rules-test.sh scripts/release-assets-test.sh scripts/image-platform-ref.sh scripts/docker-builder.sh
	shellcheck -s sh scripts/compat-download.sh scripts/compat-e2e.sh scripts/compat-download-test.sh
	shellcheck -s sh scripts/update-credentials-accept.sh
	go vet ./...
	GOOS=linux go vet ./...
	GOOS=darwin go vet ./...
	go tool staticcheck ./...
	@# 另外两个 GOOS 用已经按本机平台编译好的二进制：`GOOS=linux go tool staticcheck` 会把工具本身交叉编译成 linux 产物再执行，exec 直接失败；
	@# go tool -n 给出本机产物的路径（缺失时先构建），对它设 GOOS 才是"按另一个平台分析这份源码"。
	staticcheck=$$(go tool -n staticcheck) && GOOS=linux "$$staticcheck" ./... && GOOS=darwin "$$staticcheck" ./...
	go tool govulncheck ./...
	go run ./scripts/releasekind -check-file AGENT_VERSION

# deploy 包的替身测试每个用例都用 sh 真跑一遍安装脚本，负载高时整包要六七分钟（make ci 里实测 395 秒），
# 逼近 go test 给每个测试二进制的默认 10 分钟超时，到点被杀看起来像随机失败。只给这个包放宽到 20 分钟，其余包
# 保持默认，卡住的测试仍尽早暴露。包清单先落到变量：go list 失败或清单为空时整条命令失败，它的退出码不会被管道
# 吞掉；再滤掉 deploy，滤空时 grep 以 1 退出、同样失败。两处都防的是退化成不带包参数、只测当前目录。
# -race：hub 的内存缓存、写协程与跨包锁序都靠并发纪律维持，竞态只有检测器照得到，没有它的绿灯证明不了这些不变式。
# 检测器下各包慢 3–10 倍，store 包在本机实测约 10 分钟，贴着默认的每包超时，所以放宽到 20 分钟。
# deploy 包是替身脚本测试，没有并发可查，不带 -race。
# deploy/agent.mk 对所有配方 export CGO_ENABLED=0（产物一律纯 Go 构建），而 -race 在 Linux 上依赖 cgo（Go 1.27.2
# 的 cmd/go 只对 darwin 豁免，所以 macOS 开发机上看不到这个失败）：go test -race 会以 "requires cgo" 退出。这条命令
# 因此在自己的命令行上显式 CGO_ENABLED=1，只覆盖这一次 go test 的环境；产物构建仍由 agent.mk 的 export 决定。
test:
	all=$$(go list ./...) && [ -n "$$all" ] && pkgs=$$(printf '%s\n' "$$all" | grep -vx github.com/xjetry/heron-probe/deploy) && CGO_ENABLED=1 go test -race -count=1 -timeout 20m $$pkgs
	go test -count=1 -timeout 20m ./deploy/

# 发布规则（版本号守卫、预发布判定）与回读判定的回归检查：只跑 make 的检查、-n 展开与 docker 桩，
# 不构建、不访问 registry。
script-test:
	MAKE='$(MAKE)' scripts/release-rules-test.sh
	scripts/release-assets-test.sh
	scripts/docker-readback-test.sh
	scripts/compat-download-test.sh

# 类型检查覆盖全部 tsconfig（面板与公开页、vite 配置、e2e、测试），lint 按各文件所属的 tsconfig 取类型信息。
# react-hooks 的推荐集把几条规则定为 warn，eslint 遇 warn 仍退出 0；--max-warnings 0 让门禁不带 warning 通过。
web-lint: web-install
	pnpm --dir web exec tsc -b
	pnpm --dir web exec eslint . --max-warnings 0

web-test: web-install
	pnpm --dir web exec vitest run

# 真实浏览器验证正式 hub 的同域主题与 Passkey；运行前执行 pnpm --dir web exec playwright install。
web-e2e: hub-binary
	pnpm --dir web exec playwright test

# 两个入口的产物落在 internal/hub/web/dist（面板）与 dist-public（公开页）供 go:embed；不入库，缺产物时 hub 也能编译并给出说明页。
web: web-install
	pnpm --dir web run build

# build 验证全部已有的包在本机、Linux 与 darwin 的 amd64、arm64 上都能编译。darwin 的采集文件带 build tag，
# Linux 上的 CI 里 lint 的 GOOS=darwin go vet 只为本机架构编译它们；另一个架构只有这里编译得到，而 purego 按架构分文件实现。
# agent 与更新器还要按 release 的 AGENT_LINUX_ARCHES 逐架构编译：其中 armv7、386 是 32 位，int/uint 只有 32 位，
# 常量转换与溢出只在这些架构上暴露；只编 64 位的 ci 会放过它们，等到发布构建才失败。
# 二进制产物由 binaries 生成，只有 e2e 需要它。
build:
	go build ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...
	GOOS=darwin GOARCH=amd64 go build ./...
	GOOS=darwin GOARCH=arm64 go build ./...
	@set -e; for arch in $(AGENT_LINUX_ARCHES); do \
	  $(agent_goarch); \
	  echo "GOOS=linux $$gflags go build ./cmd/agent ./cmd/updater"; \
	  env GOOS=linux CGO_ENABLED=0 $$gflags go build ./cmd/agent ./cmd/updater; \
	done

# 开发构建的 hub 同样绑定仓库文件里的 agent 版本，面板与节点在线更新按发布时的口径工作；版本仍是 dev。
hub-binary: web
	go build -ldflags "-X main.agentVersion=$$AGENT_VERSION" -o bin/heron-hub ./cmd/hub

binaries: hub-binary
	GOOS=linux GOARCH=amd64 go build -o bin/heron-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o bin/heron-agent-linux-arm64 ./cmd/agent
	go run ./scripts/checkstatic bin/heron-agent-linux-amd64 bin/heron-agent-linux-arm64

ci: gen lint test script-test web-lint web-test web build
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

# 只构建当前 hub：旧 agent 只从固定发布包取得，不在工作区构建或覆盖。
compat-e2e: hub-binary
	scripts/compat-e2e.sh $(E2E_TIER1)

# 只发 hub 的 release 实际发出去的组合是 hub 加绑定版本的 agent（spec §14.1）：用绑定版本已发布的 agent 包跑
# 同一套端到端，不以当前源码构建替代。摘要取自用受信公钥验签的 SHA256SUMS，经 boundagent pin 写成与兼容基线
# 同格式的清单，compat-e2e 经 COMPAT_PIN 接收；清单写在被忽略的 build/ 下，不进入仓库。
bound-agent-e2e: hub-binary
	@mkdir -p build/bound-agent
	go run ./scripts/boundagent pin -version "$$AGENT_VERSION" -out build/bound-agent/pin.json -linux-arches "$(AGENT_LINUX_ARCHES)" -darwin-arches "$(AGENT_DARWIN_ARCHES)"
	COMPAT_PIN=build/bound-agent/pin.json scripts/compat-e2e.sh $(E2E_TIER1)

# agent 组的架构表、构建参数、打包配方与打包输入登记集中在 deploy/agent.mk：只发 hub 的门禁把这个
# 片段本身当作 agent 组的输入（spec §14.1），主 Makefile 只保留 hub 自己的架构与构建参数。
include deploy/agent.mk

HUB_LINUX_ARCHES := amd64 arm64

# hub 的版本注入（spec §14.1）：版本与绑定的 agent 版本都经 ldflags 注入，-trimpath 去掉构建机路径；
# 版本部分与 agent 组共用 deploy/agent.mk 的 RELEASE_LDFLAGS，任何一种产物的版本注入都不会单独漂移。
# agent 与更新器没有 agentVersion 这个变量，不给它们。
HUB_GOFLAGS = -trimpath -ldflags "$(RELEASE_LDFLAGS) -X main.agentVersion=$$AGENT_VERSION"

# 一个 Linux hub 二进制：$(1) 为 GOARCH，$(2) 为输出路径。发布目标与 docker 镜像都调用它。
hub_build = env GOOS=linux GOARCH=$(1) CGO_ENABLED=0 go build $(HUB_GOFLAGS) -o "$(2)" ./cmd/hub

# 发布目标（spec §14.1）：完整 release 与只发 hub 各一个，调用方先取 make -s release-kind 的判定再调用对应的目标。
# 两个目标开头各自再判定一次，不符即失败，单独调用也绕不过规则。不设在 make 里再分派的 release：make 对含
# $(MAKE) 的配方行在 -n 下也整行执行，判定会在 dry-run 里真的跑（发布规则测试的受限 PATH 把 go 设成绊线）。
# 同理 $(MAKE) web 单独占一行。本地验收与线上发布调用同一组目标，产物与版本注入完全一致。
#
# 静态门禁只收 Linux 产物：它守的是"与 libc 无关"（不带 PT_INTERP 与 DT_NEEDED），这是 Linux 发行版之间的约束。
# darwin 产物是 Mach-O，依赖 /usr/lib/libSystem.B.dylib 与 libresolv.9.dylib（otool -L）：Go 在 darwin 上经 libSystem
# 发起系统调用，macOS 也不提供静态链接的系统库，"不带动态依赖"在这里不成立也无须成立；darwin 的 CGO_ENABLED=0
# 由 agent_build 的显式 env 承载（deploy/agent.mk）。
# VERSION 在构建前端之前检查：不合规时立即报错，不等 web 目标跑完；AGENT_VERSION 的判定紧跟其后，判定不过时
# 不产出任何东西。
# 三个安装脚本在全部 tar 包打好之后写入本版版本号与每个 tar 包的 SHA-256（deploy/releasestamp，§5.7），脚本只按
# 写进去的清单校验；写入读的是 dist 下的 *.tar.gz，所以必须排在打包之后。SHA256SUMS 最后生成，覆盖写入后的脚本，
# 供人工核对，脚本不以它为依据。写入后的脚本在这里过 shellcheck：make lint 只查源码，这一步查的是实际发布的字节，
# 写入格式出错时在发布任何东西之前失败（release.yml 先跑 make ci，shellcheck 已在运行器上）。
assert_release_kind = kind=$$(go run ./scripts/releasekind -version "$$VERSION" -agent "$$AGENT_VERSION") || exit 1; \
	[ "$$kind" = $(1) ] || { echo "release-$(1) does not match this release's kind: $$kind" >&2; exit 1; }
release_clean = rm -rf dist/build dist/*.tar.gz dist/SHA256SUMS dist/install.sh dist/install-hub.sh dist/install-macos.sh && mkdir -p dist/build
hub_binaries = for arch in $(HUB_LINUX_ARCHES); do $(call hub_build,$$arch,dist/build/heron-hub-linux-$$arch); done
HUB_STATIC = $(addprefix dist/build/heron-hub-linux-,$(HUB_LINUX_ARCHES))
hub_pack = for arch in $(HUB_LINUX_ARCHES); do \
	  pkg="dist/pkg-hub-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/heron-hub-linux-$$arch" "$$pkg/heron-hub"; \
	  cp deploy/systemd/heron-hub.service "$$pkg/heron-hub.service"; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/heron-hub_linux_$$arch.tar.gz" heron-hub heron-hub.service; \
	  rm -rf "$$pkg"; \
	done

release-full:
	@$(check_version)
	@$(call assert_release_kind,full)
	$(MAKE) web
	$(release_clean)
	@set -e; $(agent_build); $(call updater_build,$(AGENT_LINUX_ARCHES)); $(hub_binaries)
	go run ./scripts/checkstatic $(AGENT_STATIC) $(call updater_static,$(AGENT_LINUX_ARCHES)) $(HUB_STATIC)
	@set -e; $(agent_pack); $(call updater_pack,$(AGENT_LINUX_ARCHES)); $(hub_pack)
	rm -rf dist/build
	go run ./scripts/stampinstall -version $(VERSION) -dir dist $(AGENT_INSTALLERS) deploy/install-hub.sh
	shellcheck -s sh dist/install.sh dist/install-hub.sh dist/install-macos.sh
	cd dist && sha256sum *.tar.gz install.sh install-hub.sh install-macos.sh > SHA256SUMS

# 只发 hub（spec §14.1）：门禁排在任何构建之前，agent 组的输入自绑定版本以来变过就不产出任何东西。hub 组之外，
# agent 的两个安装脚本取自绑定版本的 release，验签与核对都在 scripts/boundagent 里。复制来的两个脚本不是本版构建
# 的，stampinstall 不写它们；它们与 hub 组一起进本版的 SHA256SUMS。
release-hub-only:
	@$(check_version)
	@$(call assert_release_kind,hub-only)
	go run ./scripts/agentinputs -base "$$AGENT_VERSION"
	$(MAKE) web
	$(release_clean)
	@set -e; $(call updater_build,$(HUB_LINUX_ARCHES)); $(hub_binaries)
	go run ./scripts/checkstatic $(call updater_static,$(HUB_LINUX_ARCHES)) $(HUB_STATIC)
	@set -e; $(call updater_pack,$(HUB_LINUX_ARCHES)); $(hub_pack)
	rm -rf dist/build
	go run ./scripts/stampinstall -version $(VERSION) -dir dist deploy/install-hub.sh
	shellcheck -s sh dist/install-hub.sh
	go run ./scripts/boundagent fetch -version "$$AGENT_VERSION" -dir dist -linux-arches "$(AGENT_LINUX_ARCHES)" -darwin-arches "$(AGENT_DARWIN_ARCHES)"
	cd dist && sha256sum *.tar.gz install.sh install-hub.sh install-macos.sh > SHA256SUMS

# hub 镜像（§14）：ghcr.io/xjetry/heron-hub:<version>，平台由 HUB_LINUX_ARCHES 展开。
# 镜像里不编译 Go：hub 二进制经 hub_build 构建到 IMAGE_BIN_DIR，Dockerfile 按 TARGETARCH 取用。
DOCKER_IMAGE := ghcr.io/xjetry/heron-hub
# registry 主机只由 DOCKER_IMAGE 推出；release.yml 登录时经 make docker-registry 取用，不另写一份。
DOCKER_REGISTRY = $(firstword $(subst /, ,$(DOCKER_IMAGE)))
comma := ,
empty :=
space := $(empty) $(empty)
DOCKER_PLATFORMS := $(subst $(space),$(comma),$(addprefix linux/,$(HUB_LINUX_ARCHES)))
# 构建上下文里的二进制按 linux/<arch>/heron-hub 排列，.dockerignore 只放行这些文件。
# 不放在 dist/ 下：release.yml 以 dist/* 整体上传发布资产，而镜像在发布目标之后、
# gh release create 之前构建，目录混进 dist/ 就会被 dist/* 展开进上传参数。
IMAGE_BIN_DIR := build/image
# 构建节点固定为这一版 BuildKit（docker-container 驱动），本地、CI 与发布用同一个：docker 自带的
# docker 驱动在经典镜像存储上不支持多平台构建，而是否启用 containerd 存储是宿主的配置。
# 构建器名带版本号：改 BUILDKIT_VERSION 即换用新构建器，旧的不会被沿用；不用时 docker buildx rm 删除。
# 同名构建器由 scripts/docker-builder.sh 核对驱动与镜像后才沿用（只改 digest 时名字不变），不存在时创建。
BUILDKIT_VERSION := v0.33.0
BUILDKIT_IMAGE := moby/buildkit:$(BUILDKIT_VERSION)@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3
DOCKER_BUILDER := heron-hub-buildkit-$(BUILDKIT_VERSION)
# alpine 按 digest 固定，只在这里定义：Dockerfile 的 rootfs 阶段经 --build-arg 取用，冒烟的工具镜像
# （读卷属主、预置不可写的卷、在 hub 的网络命名空间里发请求）经环境变量 TOOL_IMAGE 取用。
ALPINE_IMAGE := alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507
docker_build = docker buildx build --builder $(DOCKER_BUILDER) -f Dockerfile --build-arg ALPINE_IMAGE=$(ALPINE_IMAGE)
ensure_builder = scripts/docker-builder.sh $(DOCKER_BUILDER) $(BUILDKIT_IMAGE)

# 本地构建并核对（§14）：两个平台都构建、导出根文件系统交给 checkimage，再把本机平台装进 docker。
# 多平台结果不能 --load：经典镜像存储不接受多平台索引（docker exporter does not currently support
# exporting manifest lists），不给 --platform 时构建的是构建节点的本机平台。
# 面板与公开页随 go:embed 进二进制，先 make web：漏掉它，镜像里的 /admin/ 与 / 都只有 503 说明页。
docker:
	@$(check_version)
	$(MAKE) web
	rm -rf $(IMAGE_BIN_DIR)
	@set -e; for arch in $(HUB_LINUX_ARCHES); do \
	  mkdir -p "$(IMAGE_BIN_DIR)/linux/$$arch"; \
	  $(call hub_build,$$arch,$(IMAGE_BIN_DIR)/linux/$$arch/heron-hub); \
	done
	go run ./scripts/checkstatic $(foreach a,$(HUB_LINUX_ARCHES),$(IMAGE_BIN_DIR)/linux/$(a)/heron-hub)
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

# 发布种类（spec §14.1）：full 或 hub-only，判定只在 scripts/releasekind。调用方据此调用 release-full 或
# release-hub-only，两个目标开头也读它。
release-kind:
	@$(check_version)
	@go run ./scripts/releasekind -version "$$VERSION" -agent "$$AGENT_VERSION"

# 发布流水线的回读读它（release.yml 里安装脚本回读取绑定版本），不在 workflow 里另读一遍仓库文件。
agent-version:
	@printf '%s\n' "$$AGENT_VERSION"

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
