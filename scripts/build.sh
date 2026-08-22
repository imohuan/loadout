#!/usr/bin/env bash
# Loadout 构建脚本（Linux / macOS）
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> 构建 Linux 二进制 bin/loadout"
go build -o bin/loadout ./apps/server

echo "==> 完成：bin/loadout"
