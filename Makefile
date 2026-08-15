GO     ?= go
GOFMT  ?= gofmt
BINARY ?= bin/werewolf

.PHONY: fmt vet test test-race build build-all generate check

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

check: vet test build
	@test -z "$$($(GOFMT) -l cmd/werewolf/*.go)" || { echo "gofmt needed:"; $(GOFMT) -l cmd/werewolf/*.go; exit 1; }
	git diff --check
