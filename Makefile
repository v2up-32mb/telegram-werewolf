GO     ?= go
GOFMT  ?= gofmt
BINARY ?= bin/werewolf

.PHONY: fmt vet test test-race build build-all generate lint sqlc sqlc-check vuln check

fmt:
	$(GOFMT) -w cmd/werewolf/*.go

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

build:
	CGO_ENABLED=0 $(GO) build -o $(BINARY) ./cmd/werewolf

build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -o $(BINARY)-linux-amd64 ./cmd/werewolf
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -o $(BINARY)-linux-arm64 ./cmd/werewolf

generate:
	$(GO) generate ./...

# Task 43：CI 门禁的本地等价目标。工具版本经 go.mod tool 指令固定
# （golangci-lint v2.12.2 / sqlc v1.30.0 / govulncheck v1.7.0）。
lint:
	$(GO) tool golangci-lint run ./...

# sqlc 生成（本机 linux/arm64 因 wazero SIGILL 不可运行，以 CI amd64 为准）。
sqlc:
	$(GO) tool sqlc generate

sqlc-check: sqlc
	git diff --exit-code -- internal/storage/sqlc

# 依赖漏洞扫描（需要网络拉取漏洞库，不纳入 check）。
vuln:
	$(GO) tool govulncheck ./...

# check 为本地全量门禁：vet + 全量测试 + 构建 + lint + 全仓 gofmt + diff。
check: vet test build lint
	@test -z "$$($(GOFMT) -l $$(git ls-files '*.go'))" || { echo "gofmt needed:"; $(GOFMT) -l $$(git ls-files '*.go'); exit 1; }
	git diff --check
