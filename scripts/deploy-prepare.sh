#!/bin/bash
# mini-cloud 部署准备（开发机侧，v0.10.0）
#
# 职责：交叉编译 ARM64 静态二进制 + UPX 压缩，把产物放入暂存目录，
#       供路由器通过 scripts/deploy-router.sh 主动拉取。
#
# 部署方向（铁律 5 / docs/process.md）：路由器作为 SSH 客户端主动连接开发机拉取；
# 开发机不作为客户端登录路由器。本脚本不含任何凭证。
#
# 用法：./scripts/deploy-prepare.sh
# 可覆盖变量：STAGING_DIR（暂存目录，默认 ~/mini-cloud-staging）、
#             UPX（upx 路径，默认 ~/upx-5.2.1-amd64_linux/upx）
set -euo pipefail

cd "$(dirname "$0")/.."

# 开发环境（env.sh 不入库，按本机实际路径配置，模板见 env.example.sh）
if [ -f ./env.sh ]; then
    # shellcheck disable=SC1091
    source ./env.sh
fi

STAGING_DIR="${STAGING_DIR:-$HOME/mini-cloud-staging}"
UPX_BIN="${UPX:-$HOME/upx-5.2.1-amd64_linux/upx}"

echo "==> 交叉编译（ARM64 静态）"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o /tmp/mini-cloud-arm64 .

echo "==> UPX 压缩"
"$UPX_BIN" --best -o /tmp/mini-cloud-arm64-upx /tmp/mini-cloud-arm64 >/dev/null

echo "==> 放入暂存目录: $STAGING_DIR"
mkdir -p "$STAGING_DIR"
cp /tmp/mini-cloud-arm64-upx "$STAGING_DIR/mini-cloud-arm64-upx"

SIZE=$(stat -c%s "$STAGING_DIR/mini-cloud-arm64-upx")
echo "==> 完成：$STAGING_DIR/mini-cloud-arm64-upx"
echo "==> UPX 后体积（铁律 10 观察项，记入 PROGRESS.md）: $SIZE 字节"
