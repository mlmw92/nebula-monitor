#!/usr/bin/env bash
# 交叉编译 Agent / Server 为 linux 各架构产物，输出到 dist/artifacts/bin/
# （供 install-server.sh 自动识别，无需目标服务器安装 Go）。
# 产物布局（install-server.sh 的 --dist 约定）：
#   dist/artifacts/bin/server/linux/<arch>/server
#   dist/artifacts/bin/agent/linux/<arch>/agent
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-$(bash "$ROOT/build/version.sh")}"
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-X github.com/nebula/monitor/internal/version.Version=${VERSION} -X github.com/nebula/monitor/internal/version.BuildTime=${BUILD_TIME}"

OUT_DIR="dist/artifacts/bin"
mkdir -p "$OUT_DIR"

ARCHS=("amd64" "arm64" "arm")

for ARCH in "${ARCHS[@]}"; do
  echo "==> 交叉编译 linux/$ARCH (version=$VERSION)"
  mkdir -p "$OUT_DIR/agent/linux/$ARCH" "$OUT_DIR/server/linux/$ARCH"
  GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$OUT_DIR/agent/linux/$ARCH/agent" ./cmd/agent
  GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$OUT_DIR/server/linux/$ARCH/server" ./cmd/server
done

echo "完成：产物在 $OUT_DIR/agent/linux/<arch>/ 与 $OUT_DIR/server/linux/<arch>/"
echo "版本：$VERSION  构建时间：$BUILD_TIME"
