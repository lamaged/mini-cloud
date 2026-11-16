#!/bin/sh
# mini-cloud 部署（路由器侧，v0.10.0）——在路由器上运行（busybox ash 兼容，无 bash 特性）
#
# 职责：从开发机拉取产物 → 备份旧二进制 → 停服务（确认旧进程退出）→ 安装 →
#       脱离会话启动（start-stop-daemon/setsid）→ 健康检查轮询 → 失败自动回滚。
#
# 部署方向（铁律 5 / docs/process.md）：本脚本由路由器执行，路由器作为 SSH 客户端
# 主动连接开发机拉取（scp 交互式输入开发机密码）；开发机不作为客户端登录路由器。
# 本脚本不保存任何凭证。日志写入 $INSTALL_DIR/mini-cloud.log（供验收清单核对版本号）。
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
LOG="$INSTALL_DIR/mini-cloud.log"

echo "==> 从开发机拉取产物（如提示请输入开发机登录密码）"
scp "$DEV_USER@$DEV_HOST:$STAGING/$BINARY" "$NEW"
if [ ! -s "$NEW" ]; then
    echo "错误：拉取的文件为空或不存在"
    exit 1
fi

# 记录旧进程 PID：健康检查必须确认应答者是「新起的进程」（I9-3）
OLD_PID=$(pgrep -f "$BINARY" 2>/dev/null | head -1)
OLD_PID="${OLD_PID:-0}"

if [ -f "$INSTALL_DIR/$BINARY" ]; then
    echo "==> 备份旧二进制（.prev，供回滚）"
    cp "$INSTALL_DIR/$BINARY" "$INSTALL_DIR/$BINARY.prev"
fi

echo "==> 停止服务并确认旧进程退出"
pkill -f "$BINARY" 2>/dev/null || killall "$BINARY" 2>/dev/null || true
i=0
while [ "$OLD_PID" != "0" ] && kill -0 "$OLD_PID" 2>/dev/null && [ "$i" -lt 15 ]; do
    sleep 1
    i=$((i+1))
done
if [ "$OLD_PID" != "0" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    echo "错误：旧进程 $OLD_PID 未退出，中止部署"
    exit 1
fi

echo "==> 安装新二进制"
mv "$NEW" "$INSTALL_DIR/$BINARY"
chmod +x "$INSTALL_DIR/$BINARY"

# 从配置读取监听端口（默认 8088）
PORT=$(sed -n 's/.*"listen"[^"]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -1 | sed 's/.*://')
PORT="${PORT:-8088}"

# 脱离会话启动（I9-1：断开 SSH 后进程不被杀）；日志落文件（I9-4）
start_daemon() {
    cd "$INSTALL_DIR" || return 1
    if command -v start-stop-daemon >/dev/null 2>&1; then
        start-stop-daemon -S -b -x "$INSTALL_DIR/$BINARY" -- -config "$CONFIG" >> "$LOG" 2>&1
    elif command -v setsid >/dev/null 2>&1; then
        setsid "$INSTALL_DIR/$BINARY" -config "$CONFIG" >> "$LOG" 2>&1 &
    else
        "$INSTALL_DIR/$BINARY" -config "$CONFIG" >> "$LOG" 2>&1 &
    fi
}

echo "==> 启动服务（端口 $PORT，日志 $LOG）"
start_daemon
sleep 1

# 新进程确认：必须是新 PID（I9-3：防止旧进程没死透导致假通过）
NEW_PID=$(pgrep -f "$BINARY" 2>/dev/null | head -1)
NEW_PID="${NEW_PID:-0}"
if [ "$NEW_PID" = "0" ] || [ "$NEW_PID" = "$OLD_PID" ]; then
    echo "错误：新进程未启动（NEW_PID=$NEW_PID OLD_PID=$OLD_PID）"
    rollback_old
    exit 1
fi

# 健康检查轮询（I9-2：弱网下启动可能超过 3 秒）
echo "==> 健康检查 http://127.0.0.1:$PORT/（轮询，期望 401 或 200）"
CODE="000"
i=0
while [ "$i" -lt 15 ]; do
    CODE=$(wget -q -S -O /dev/null "http://127.0.0.1:$PORT/" 2>&1 | \
        grep -o 'HTTP/1\.[01] [0-9][0-9][0-9]' | tail -1 | awk '{print $2}') || true
    CODE="${CODE:-000}"
    if [ "$CODE" = "401" ] || [ "$CODE" = "200" ]; then
        break
    fi
    # 新进程已死则提前判失败
    if ! kill -0 "$NEW_PID" 2>/dev/null; then
        break
    fi
    sleep 1
    i=$((i+1))
done

if [ "$CODE" = "401" ] || [ "$CODE" = "200" ]; then
    echo "==> 部署成功（HTTP $CODE，PID $NEW_PID）"
    echo "==> UPX 后体积（铁律 10 观察项，记入 PROGRESS.md）: $(wc -c < "$INSTALL_DIR/$BINARY") 字节"
    exit 0
fi

rollback_old() {
    echo "==> 回滚旧版本"
    pkill -f "$BINARY" 2>/dev/null || killall "$BINARY" 2>/dev/null || true
    sleep 1
    if [ -f "$INSTALL_DIR/$BINARY.prev" ]; then
        mv "$INSTALL_DIR/$BINARY.prev" "$INSTALL_DIR/$BINARY"
        start_daemon
        echo "已恢复旧版本"
    else
        echo "错误：无旧版本可回滚（.prev 不存在）"
    fi
}

echo "==> 健康检查失败（HTTP $CODE，PID $NEW_PID 存活：$(kill -0 "$NEW_PID" 2>/dev/null && echo 是 || echo 否)），回滚"
rollback_old
exit 1
