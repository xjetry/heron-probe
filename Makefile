export CGO_ENABLED=0

.PHONY: gen lint test build binaries ci e2e e2e-matrix fixtures web-install web-test web release

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
