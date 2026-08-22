#!/usr/bin/env bash
# Loadout 打包脚本（Linux/macOS）
# 包含 server 和 desktop 两个目标

set -e

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${1:-all}"

build_server() {
    echo ""
    echo "==> 构建 Loadout Server"
    cd "$ROOT_DIR/apps/server"
    go build -o ../../bin/loadout .
    echo "  OK   bin/loadout"
}

build_desktop() {
    echo ""
    echo "==> 构建 Loadout Desktop"
    bash "$ROOT_DIR/scripts/pack-desktop.sh"
}

case "$TARGET" in
    server)
        build_server
        ;;
    desktop)
        build_desktop
        ;;
    all)
        build_server
        build_desktop
        ;;
    *)
        echo "用法: $0 [server|desktop|all]"
        exit 1
        ;;
esac

echo ""
echo "==> 全部完成!"
