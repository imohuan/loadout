#!/usr/bin/env bash
# Loadout Server 一键部署（Linux x86_64 / systemd）
#
# 用法:
#   bash deploy-loadout.sh                 # 默认 v0.1.0, 端口 18000
#   bash deploy-loadout.sh v0.2.0 8080     # 指定版本与端口
#
# 产物来自 GitHub Release: loadout-server-linux-amd64.tar.gz
# 完成后: systemctl enable --now loadout  (开机自启)
set -euo pipefail

VERSION="${1:-v0.1.0}"
PORT="${2:-18000}"
HOME_DIR="/opt/services/loadout"
BIN_DIR="/usr/local/bin"
URL="https://github.com/imohuan/loadout/releases/download/${VERSION}/loadout-server-linux-amd64.tar.gz"

if [ "$(id -u)" -ne 0 ]; then
    echo "请以 root 运行: sudo bash deploy-loadout.sh"
    exit 1
fi

echo "==> [1/6] 停止旧服务（升级部署必须，避免文件占用与新版本不生效）"
if systemctl list-unit-files 2>/dev/null | grep -q '^loadout\.service'; then
    if systemctl is-active --quiet loadout; then
        systemctl stop loadout
        echo "    已停止运行中的 loadout"
    else
        echo "    loadout 服务存在但未在运行，跳过"
    fi
else
    echo "    未安装 loadout 服务（首次部署），跳过"
fi

echo "==> [2/6] 下载 ${VERSION} 产物"
mkdir -p "${HOME_DIR}"
cd "${HOME_DIR}"
curl -fsSL -o loadout.tar.gz "${URL}" || {
    echo "下载失败: ${URL}"
    echo "检查版本号是否存在（gh release list）或网络可达 github.com"
    exit 1
}

echo "==> [3/6] 解压"
tar -xzf loadout.tar.gz && rm -f loadout.tar.gz
SRC_DIR=""
for d in loadout loadout-server; do
    [ -d "${d}" ] && SRC_DIR="${d}" && break
done
[ -z "${SRC_DIR}" ] && { echo "错误: 解压后未找到 loadout 目录"; exit 1; }

echo "==> [4/6] 安装二进制 + systemd 单元"
# 覆盖前先删除旧文件：目标二进制正被进程执行时 cp 会报 ETXTBSY (Text file busy)。
# systemd 启动的实例 [1/6] 已停；此处兜底处理手动启动（nohup 等）的旧实例。
if [ -f "${BIN_DIR}/loadout" ]; then
    if command -v fuser >/dev/null 2>&1 && fuser "${BIN_DIR}/loadout" >/dev/null 2>&1; then
        echo "    警告: 检测到手动启动的进程占用 ${BIN_DIR}/loadout，正在终止..."
        fuser -k "${BIN_DIR}/loadout" 2>/dev/null || true
        sleep 2
    fi
    rm -f "${BIN_DIR}/loadout"
fi
cp "${SRC_DIR}/loadout" "${BIN_DIR}/loadout"
chmod +x "${BIN_DIR}/loadout"
useradd -r -m loadout 2>/dev/null || true
sed -e "s|^Environment=LOADOUT_SERVER_ADDR=.*|Environment=LOADOUT_SERVER_ADDR=:${PORT}|" \
    -e "/^Environment=LOADOUT_SERVER_ADDR=/a Environment=LOADOUT_HOME_DIR=${HOME_DIR}" \
    "${SRC_DIR}/loadout.service" > /etc/systemd/system/loadout.service

echo "==> [5/6] 数据目录权限 + 重载 systemd"
chown -R loadout:loadout "${HOME_DIR}"
systemctl daemon-reload

echo "==> [6/6] 启动（开机自启）"
# 上次启动失败会残留 failed 状态，start 前先清除，否则 enable --now 可能拒绝
systemctl reset-failed loadout 2>/dev/null || true
systemctl enable --now loadout

echo ""
echo "==> 部署完成!"
systemctl status loadout --no-pager | head -8
echo ""
echo "  验证: curl http://localhost:${PORT}"
echo "  数据: ${HOME_DIR}（首次启动生成 admin-password，账号 admin）"
echo "  修改配置: vim /etc/systemd/system/loadout.service 后 systemctl daemon-reload && systemctl restart loadout"
