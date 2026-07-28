#!/usr/bin/env bash
# 构建 Vue3 前端，并把构建产物平铺拷贝到 dist/artifacts/web/
# 产物布局（install-server.sh 的 --dist 约定）：
#   dist/artifacts/web/index.html
#   dist/artifacts/web/assets/...
# 也可由 release.sh 在最终打包前再统一 cp -a 一次确保产物最新。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT_DIR="dist/artifacts/web"

echo "==> 安装前端依赖"
cd web
npm install --no-audit --no-fund
echo "==> 构建前端"
npm run build
cd "$ROOT"

echo "==> 平铺拷贝到 $OUT_DIR"
rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"
cp -a web/dist/. "$OUT_DIR/"

echo "完成：前端产物在 $OUT_DIR/"
ls -la "$OUT_DIR/"
