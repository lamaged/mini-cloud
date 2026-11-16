#!/bin/sh
# mini-cloud 部署（路由器侧，v0.10.0）——在路由器上运行（busybox ash 兼容，无 bash 特性）
#
# 职责：从开发机拉取产物 → 备份旧二进制 → 停服务 → 安装 → 重启 → 健康检查 → 失败自动回滚。
#
# 部署方向（铁律 5 / docs/process.md）：本脚本由路由器执行，路由器作为 SSH 客户端
# 主动连接开发机拉取（scp 交互式输入开发机密码）；开发机不作为客户端登录路由器。
# 本脚本不保存任何凭证。
#
# 用法（在路由器上）：sh deploy-router.sh
# 可覆盖变量：DEV_HOST（开发机 IP，默认 192.168.2.21）、DEV_USER（开发机用户，默认 test）、
#             STAGING（开发机暂存目录名，默认 mini-cloud-staging）、
#             INSTALL_DIR（安装目录，默认 /data/mini-cloud）、
#             CONFIG（配置文件路径，默认 /data/mini-cloud/config/config.json）
set -e

DEV_HOST="${DEV_HOST:-192.168.2.21}"
DEV_USER="${DEV_USER:-test}"
STAGING="${STAGING:-mini-cloud-staging}"
INSTALL_DIR="${INSTALL_DIR:-/data/mini-cloud}"
CONFIG="${CONFIG:-$INSTALL_DIR/config/config.json}"
BINARY="mini-cloud-arm64-upx"
NEW="/tmp/mini-cloud-new"

echo "==> 从开发机拉取产物（如提示请输入开发机登录密码）"
scp "$DEV_USER@$DEV_HOST:$STAGING/$BINARY" "$NEW"
if [ ! -s "$NEW" ]; then
    echo "错误：拉取的文件为空或不存在"
    exit 1
fi

if [ -f "$INSTALL_DIR/$BINARY" ]; then
    echo "==> 备份旧二进制（.prev，供回滚）"
    cp "$INSTALL_DIR/$BINARY" "$INSTALL_DIR/$BINARY.prev"
fi

echo "==> 停止服务"
pkill -f "$BINARY" 2>/dev/null || killall "$BINARY" 2>/dev/null || true
sleep 2

echo "==> 安装新二进制"
mv "$NEW" "$INSTALL_DIR/$BINARY"
chmod +x "$INSTALL_DIR/$BINARY"

# 从配置读取监听端口（默认 8088）
PORT=$(sed -n 's/.*"listen"[^"]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -1 | sed 's/.*://')
PORT="${PORT:-8088}"

echo "==> 启动服务（端口 $PORT）"
cd "$INSTALL_DIR" || exit 1
./$BINARY -config "$CONFIG" >> /dev/null 2>&1 &
sleep 3

echo "==> 健康检查 http://127.0.0.1:$PORT/（期望 401 未认证或 200）"
CODE=$(wget -q -S -O /dev/null "http://127.0.0.1:$PORT/" 2>&1 | \
    grep -o 'HTTP/1\.[01] [0-9][0-9][0-9]' | tail -1 | awk '{print $2}') || true
CODE="${CODE:-000}"

if [ "$CODE" = "401" ] || [ "$CODE" = "200" ]; then
    echo "==> 部署成功（HTTP $CODE）"
    echo "==> UPX 后体积（铁律 10 观察项，记入 PROGRESS.md）: $(wc -c < "$INSTALL_DIR/$BINARY") 字节"
    exit 0
fi

echo "==> 健康检查失败（HTTP $CODE），回滚旧版本"
pkill -f "$BINARY" 2>/dev/null || killall "$BINARY" 2>/dev/null || true
sleep 1
if [ -f "$INSTALL_DIR/$BINARY.prev" ]; then
    mv "$INSTALL_DIR/$BINARY.prev" "$INSTALL_DIR/$BINARY"
    cd "$INSTALL_DIR" || exit 1
    ./$BINARY -config "$CONFIG" >> /dev/null 2>&1 &
    echo "已恢复旧版本"
fi
exit 1
