#!/bin/sh
# mini-cloud 部署（路由器侧，v0.10.0）——在路由器上运行（busybox ash 兼容，无 bash 特性）
#
# 职责：从开发机拉取产物 → 备份旧二进制 → 停服务（确认旧进程退出）→ 安装（同目录原子替换）→
#       脱离会话启动（setsid）→ 健康检查轮询（15×1s）→ 失败自动回滚并验证恢复。
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
PORT="8088"

# ============================================================
# 函数定义（必须整体先于使用：POSIX sh 执行到定义行才生效，I11）
# ============================================================

# LOG_START：start_daemon 启动前的日志行数；health_check 只检查其后新增的行
LOG_START=0

# start_daemon 脱离会话启动（P15：setsid + 显式重定向，行为可预期；回退普通后台。
# 注：AX6 实测无 setsid，回退 & 启动并已证明可跨会话存活）
start_daemon() {
    cd "$INSTALL_DIR" || return 1
    LOG_START=$(wc -l < "$LOG" 2>/dev/null)
    LOG_START="${LOG_START:-0}"
    if command -v setsid >/dev/null 2>&1; then
        setsid sh -c "exec \"$INSTALL_DIR/$BINARY\" -config \"$CONFIG\" >> \"$LOG\" 2>&1" &
    else
        "$INSTALL_DIR/$BINARY" -config "$CONFIG" >> "$LOG" 2>&1 &
    fi
}

# health_check 轮询（15×1s）：进程存活 + 启动后日志新增「Gateway 启动」行。
# 不依赖 wget（AX6 BusyBox wget 无 -S 选项，实测确认），进程死亡提前判失败。
# 返回 0=通过，1=失败
health_check() {
    _pid="$1"
    i=0
    while [ "$i" -lt 15 ]; do
        if ! kill -0 "$_pid" 2>/dev/null; then
            return 1
        fi
        if tail -n "+$((LOG_START + 1))" "$LOG" 2>/dev/null | grep -q 'Gateway 启动'; then
            return 0
        fi
        sleep 1
        i=$((i+1))
    done
    return 1
}

# rollback_old 回滚旧版本并验证恢复（P16：回滚后同样健康检查，确认「已恢复」）
rollback_old() {
    echo "==> 回滚旧版本"
    kill $(pgrep -f "$BINARY") 2>/dev/null || true
    sleep 1
    if [ ! -f "$INSTALL_DIR/$BINARY.prev" ]; then
        echo "错误：无旧版本可回滚（.prev 不存在）"
        return 1
    fi
    mv "$INSTALL_DIR/$BINARY.prev" "$INSTALL_DIR/$BINARY"
    start_daemon
    sleep 1
    _rollback_pid=$(pgrep -f "$BINARY" 2>/dev/null | head -1)
    _rollback_pid="${_rollback_pid:-0}"
    if [ "$_rollback_pid" = "0" ] || ! health_check "$_rollback_pid"; then
        echo "错误：回滚后服务仍未恢复，请人工介入"
        return 1
    fi
    echo "已恢复旧版本（日志确认 Gateway 启动）"
    return 0
}

# ============================================================
# 主流程
# ============================================================

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
# pgrep -f 按完整命令行匹配（AX6 已确认有 pgrep；二进制名超 15 字符时 killall 按 comm 会截断失效，I13）
kill $(pgrep -f "$BINARY") 2>/dev/null || true
i=0
while [ "$OLD_PID" != "0" ] && kill -0 "$OLD_PID" 2>/dev/null && [ "$i" -lt 15 ]; do
    sleep 1
    i=$((i+1))
done
if [ "$OLD_PID" != "0" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    echo "错误：旧进程 $OLD_PID 未退出，中止部署"
    exit 1
fi

# 安装（P17：/tmp 与 /data 跨文件系统，先拷到目标目录临时名，再同目录 mv 原子替换）
echo "==> 安装新二进制"
cp "$NEW" "$INSTALL_DIR/$BINARY.new"
chmod +x "$INSTALL_DIR/$BINARY.new"
mv "$INSTALL_DIR/$BINARY.new" "$INSTALL_DIR/$BINARY"
rm -f "$NEW"

# 从配置读取监听端口（默认 8088）
PORT=$(sed -n 's/.*"listen"[^"]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -1 | sed 's/.*://')
PORT="${PORT:-8088}"

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

echo "==> 健康检查（进程存活 + 日志出现 Gateway 启动，轮询 15×1s）"
if health_check "$NEW_PID"; then
    echo "==> 部署成功（PID $NEW_PID，日志确认启动）"
    grep 'version=' "$LOG" | tail -1
    echo "==> UPX 后体积（铁律 10 观察项，记入 PROGRESS.md）: $(wc -c < "$INSTALL_DIR/$BINARY") 字节"
    exit 0
fi

echo "==> 健康检查失败，回滚"
rollback_old
exit 1
