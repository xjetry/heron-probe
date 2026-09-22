export CGO_ENABLED=0

.PHONY: gen lint test build binaries ci e2e fixtures

gen:
	buf generate

lint:
	buf lint
	go vet ./...
	GOOS=linux go vet ./...

test:
	go test -count=1 ./...

# build 只验证全部已有的包在三个目标平台都能编译，所以从第一个 Go 包起每个提交上都有意义；
# 二进制产物由 binaries 生成，只有 e2e 需要它。
build:
	go build ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...

binaries:
	go build -o bin/probe-hub ./cmd/hub
	GOOS=linux GOARCH=amd64 go build -o bin/probe-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o bin/probe-agent-linux-arm64 ./cmd/agent

ci: gen lint test build
	git diff --exit-code -- gen

fixtures:
	scripts/capture-proc.sh docker-debian

e2e: binaries
	scripts/e2e.sh
