#!/usr/bin/env bash
# 读取仓库根目录 VERSION 文件作为统一版本号（语义化版本，如 v1.0.0）。
# 每次编译前根据改动大小手动调整 VERSION：
#   主版本(MAJOR)= 不兼容变更；次版本(MINOR)= 新功能；修订(PATCH)= 缺陷修复。
# Server / Agent / Web 三端共用此版本号，无需在各处分别维护。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cat "${ROOT}/VERSION" 2>/dev/null || echo "v1.0.0"
