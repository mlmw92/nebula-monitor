BINARY_DIR := bin
PKG := github.com/nebula/monitor
GO := go

## 版本号统一来自仓库根目录 VERSION 文件；每次编译前按改动大小手动 bump 主/次/修订。
VERSION := $(shell cat VERSION 2>/dev/null || echo v1.0.0)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.BuildTime=$(BUILD_TIME)

.PHONY: all build build-agent build-server build-web cross clean tidy

all: build

## 完整构建：先构建前端（embed 所需），再构建 agent/server 二进制
build: build-agent build-web build-server

build-agent:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agent ./cmd/agent

build-server:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/server ./cmd/server

## 前端构建：在 web/ 下 npm install + vite build，产物平铺拷贝到 dist/artifacts/web/
build-web:
	cd web && npm install --no-audit --no-fund && npm run build
	rm -rf dist/artifacts/web
	mkdir -p dist/artifacts/web
	cp -a web/dist/. dist/artifacts/web/

tidy:
	$(GO) mod tidy

## 交叉编译三个架构到 dist/artifacts/bin/（依赖 build-web 准备前端）
cross: build-web
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/artifacts/bin/server/linux/amd64/server ./cmd/server
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/artifacts/bin/agent/linux/amd64/agent ./cmd/agent
	GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/artifacts/bin/server/linux/arm64/server ./cmd/server
	GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/artifacts/bin/agent/linux/arm64/agent ./cmd/agent
	GOOS=linux GOARCH=arm $(GO) build -ldflags "$(LDFLAGS)" -o dist/artifacts/bin/server/linux/arm/server ./cmd/server
	GOOS=linux GOARCH=arm $(GO) build -ldflags "$(LDFLAGS)" -o dist/artifacts/bin/agent/linux/arm/agent ./cmd/agent

clean:
	rm -rf $(BINARY_DIR) dist web/dist
