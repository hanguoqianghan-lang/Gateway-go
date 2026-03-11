# ==============================================================================
# IoT Gateway Makefile
# 纯Go实现，无痛交叉编译
# ==============================================================================

# ── 基础变量 ──────────────────────────────────────────────────────────────────
MODULE      := github.com/gateway/gateway
BINARY      := gateway
CMD_PKG     := ./cmd/gateway

# 版本信息（从 git tag 获取，找不到则用 "dev"）
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
               -X $(MODULE)/internal/version.Version=$(VERSION) \
               -X $(MODULE)/internal/version.BuildTime=$(BUILD_TIME)

# ==============================================================================
.PHONY: all build build-linux build-arm64 build-arm build-windows clean test lint tidy help

# ── 默认目标：本机构建 ────────────────────────────────────────────────────────
all: build

## build: 本机构建（使用纯Go实现，无需CGO）
build:
	@echo ">>> 构建本机二进制..."
	CGO_ENABLED=0 \
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD_PKG)
	@echo ">>> 输出: bin/$(BINARY)"

## build-linux: 构建 Linux AMD64 二进制
build-linux:
	@echo ">>> 构建 Linux AMD64 二进制..."
	CGO_ENABLED=0 \
	GOOS=linux \
	GOARCH=amd64 \
	go build -trimpath -ldflags "$(LDFLAGS)" \
	         -o bin/$(BINARY)-linux-amd64 $(CMD_PKG)
	@echo ">>> 输出: bin/$(BINARY)-linux-amd64"

## build-arm64: 构建 ARM64 二进制（RK3568J / openEuler）
build-arm64:
	@echo ">>> 构建 ARM64 二进制..."
	CGO_ENABLED=0 \
	GOOS=linux \
	GOARCH=arm64 \
	go build -trimpath -ldflags "$(LDFLAGS)" \
	         -o bin/$(BINARY)-linux-arm64 $(CMD_PKG)
	@echo ">>> 输出: bin/$(BINARY)-linux-arm64"

## build-arm: 构建 ARMv7 二进制
build-arm:
	@echo ">>> 构建 ARMv7 二进制..."
	CGO_ENABLED=0 \
	GOOS=linux \
	GOARCH=arm \
	GOARM=7 \
	go build -trimpath -ldflags "$(LDFLAGS)" \
	         -o bin/$(BINARY)-linux-arm $(CMD_PKG)
	@echo ">>> 输出: bin/$(BINARY)-linux-arm"

## build-windows: 构建 Windows 二进制
build-windows:
	@echo ">>> 构建 Windows 二进制..."
	CGO_ENABLED=0 \
	GOOS=windows \
	GOARCH=amd64 \
	go build -trimpath -ldflags "$(LDFLAGS)" \
	         -o bin/$(BINARY)-windows-amd64.exe $(CMD_PKG)
	@echo ">>> 输出: bin/$(BINARY)-windows-amd64.exe"

## build-all: 构建所有平台二进制
build-all: build-linux build-arm64 build-arm build-windows
	@echo ">>> 所有平台构建完成"

# ── 工具目标 ──────────────────────────────────────────────────────────────────
## test: 运行单元测试
test:
	go test -race -timeout 60s ./...

## lint: 静态检查（需要安装 golangci-lint）
lint:
	golangci-lint run ./...

## tidy: 整理 go.mod / go.sum
tidy:
	go mod tidy

## clean: 清理编译产物
clean:
	rm -rf bin/

## help: 显示此帮助信息
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | \
	  sed 's/## //' | \
	  awk -F: '{printf "  \033[36m%-28s\033[0m %s\n", $$1, $$2}'

# ==============================================================================
# 使用示例
# ------------------------------------------------------------------------------
# 本机构建：
#   make build
#
# 交叉编译 ARM64（RK3568J / openEuler）：
#   make build-arm64
#
# 构建所有平台：
#   make build-all
#
# 注意：纯Go实现，无需安装交叉编译工具链，无需配置CGO环境
# ==============================================================================
