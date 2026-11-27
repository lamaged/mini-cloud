#!/bin/sh /etc/rc.common
#
# initd-mini-cloud.sh —— mini-cloud 开机启动 + 手工调试（范本 A：只管理 mini-cloud）
#
# 【定位（v3，PM 拍板 2026-11-27）】脚本只做三件事：
#   按顺序执行 → 每步如实报告「开始/成功/失败」 → 失败给一句原因和建议。
#   外部问题（网络、云盘、时间）只检测和报告，不修复；异常恢复由人处理。
#
# 【两种场景，一套流程】
#   开机自动（rc.common 带 boot 调用）：后台执行，状态写日志；
#   手工 SSH（/etc/init.d/<服务名> start）：前台执行，每步实时打在屏幕上 + 同样写日志。
#
# 【安装】
#   cp initd-mini-cloud.sh /etc/init.d/mini-cloud
#   chmod +x /etc/init.d/mini-cloud
#   /etc/init.d/mini-cloud enable
#   reboot
#
# 【二选一】若用范本 B（initd-mini-cloud-mihello.sh），本脚本不要安装（会抢 8088 端口）。
#
# 【真机硬约束（都是实测踩出来的）】
#   1) mini-cloud 必须在 /data/mini-cloud 下启动（state/ 相对工作目录）。
#   2) 开机流程放在后台子进程里跑：boot 阶段 rc.common 会对主脚本发 TERM（实测约 40 秒），
#      不会波及它 fork 出来的子进程。
#   3) 所有等待都有上限；不用固定 sleep 等网络。
#
# 【占位符】下面带 <...> 的值必须按你的实际部署替换。

START=99

# ---- mini-cloud 路径与访问地址 ----
MC_DIR=/data/mini-cloud
MC_BIN="$MC_DIR/mini-cloud-arm64-upx"
MC_CFG="$MC_DIR/config/config.json"
MC_LOG="$MC_DIR/log/mini-cloud.log"
MC_HOST="<router-ip>"
MC_PORT=8088
MC_USER=""
MC_PASS=""

# ---- 等待参数 ----
NET_WAIT_MAX=120              # 120 × 5 秒 = 10 分钟
NET_PROBE=http://cloud.189.cn
CLOCK_FLOOR_EPOCH=1767225600  # 时钟下限 2026-01-01 UTC（只做「明显错误」过滤）
STOP_WAIT=5                   # 等进程退出的上限（秒）

# ---- 内部文件 ----
SELF="${initscript:-$0}"
LOG=/tmp/mini-cloud-boot.log
WORK_PID=/tmp/mini-cloud-work.pid   # 「运行号 PID 身份标记」（仅开机模式写）
LOCK=/tmp/mini-cloud.lock           # mkdir 原子锁
STOP_FLAG=/tmp/mini-cloud.stop      # 停止旗标（worker 在各阶段边界检查）
MC_MODE=boot                        # boot=开机（只写日志）；cli=手工（日志+屏幕）

# ============================================================
# 输出：ts 前缀 + 日志必写；cli 模式同时写屏幕
# ============================================================
ts() { date '+%m-%d %H:%M:%S'; }
say() {
    _m="$(ts) $*"
    echo "$_m" >> "$LOG"
    [ "$MC_MODE" = "cli" ] && echo "$_m"
}
log() { echo "$(ts) $*" >> "$LOG"; }        # 只写日志（重试细节等）

# 步骤三态：step 打印「[i/N] 标题........」（无换行），ok/fail 补结果
STEP_N=0; STEP_TOTAL=0; STEP_TITLE=""
step() {
    STEP_TOTAL="$1"; STEP_TITLE="$2"
    STEP_N=$((STEP_N+1))
    _s="$(ts) [$STEP_N/$STEP_TOTAL] $STEP_TITLE........"
    printf '%s' "$_s" >> "$LOG"
    [ "$MC_MODE" = "cli" ] && printf '%s' "$_s"
}
ok()   { _s="成功（$*）"; echo "$_s" >> "$LOG"; [ "$MC_MODE" = "cli" ] && echo "$_s"; }
fail() { _s="失败（$*）"; echo "$_s" >> "$LOG"; [ "$MC_MODE" = "cli" ] && echo "$_s"; }

# ============================================================
# 基础工具
# ============================================================
acquire_lock() {
    _i=0
    while ! mkdir "$LOCK" 2>/dev/null; do
        _lp=$(cat "$LOCK/pid" 2>/dev/null)
        if [ -n "$_lp" ] && ! kill -0 "$_lp" 2>/dev/null; then
            log "启动锁残留（持锁进程 $_lp 已退出）：确认没有 start/stop 在跑后，手工 rm -rf $LOCK"
            return 1
        fi
        _i=$((_i+1)); [ "$_i" -gt 50 ] && { log "获取启动锁超时（另一个 start/stop 正在进行）"; return 1; }
        sleep 1
    done
    echo $$ > "$LOCK/pid"
    return 0
}
release_lock() { rm -rf "$LOCK"; }

work_pid()  { cut -d' ' -f2 "$WORK_PID" 2>/dev/null; }
work_mark() { cut -d' ' -f3 "$WORK_PID" 2>/dev/null; }

# PID 身份校验（防进程号复用误杀）：存活 + cmdline 含身份标记
pid_is_worker() {
    _p="$1"; _m="$2"
    [ -n "$_p" ] && [ -n "$_m" ] || return 1
    kill -0 "$_p" 2>/dev/null || return 1
    tr '\0' ' ' < "/proc/$_p/cmdline" 2>/dev/null | grep -qF -- "$_m"
}

# 等进程消失（最多 N 秒）；0 = 已消失
wait_gone() {
    _pat="$1"; _lim="$2"; _i=0
    while [ "$_i" -lt "$_lim" ]; do
        pgrep -f "$_pat" >/dev/null 2>&1 || return 0
        sleep 1; _i=$((_i+1))
    done
    pgrep -f "$_pat" >/dev/null 2>&1 && return 1
    return 0
}

stop_requested() { [ -f "$STOP_FLAG" ]; }

# 启动前自检（配置类问题：缺文件直接报，不白等网络；错误上屏——人工调试先看到它）
preflight() {
    [ -d "$MC_DIR" ] || { say "  配置问题：目录不存在 $MC_DIR"; return 1; }
    [ -x "$MC_BIN" ] || { say "  配置问题：二进制不存在或不可执行 $MC_BIN"; return 1; }
    [ -f "$MC_CFG" ] || { say "  配置问题：配置文件不存在 $MC_CFG"; return 1; }
    mkdir -p "$(dirname "$MC_LOG")" 2>/dev/null
    ( : >> "$MC_LOG" ) 2>/dev/null || { say "  配置问题：日志不可写 $MC_LOG"; return 1; }
    return 0
}

# 网络就绪：拿到任何 HTTP 响应（含 3xx/4xx）即通
net_ready() {
    _r=$(curl -sS -m 5 -o /dev/null -w 'code=%{http_code}' "$NET_PROBE" 2>&1 | tr '\n' ' ')
    log "net probe: $_r"
    case "$_r" in
        code=[1-9]*) return 0 ;;
        *)           return 1 ;;
    esac
}

# 从 config.json 的 server 段读 Basic Auth（就绪探测用；截到 "clouds" 为止）
load_auth() {
    _srv=$(sed -n '1,/"clouds"/p' "$MC_CFG" 2>/dev/null)
    MC_USER=$(echo "$_srv" | grep -o '"username"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
    MC_PASS=$(echo "$_srv" | grep -o '"password"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
}

mc_running() { pgrep -f "$MC_BIN" >/dev/null 2>&1; }

# ============================================================
# 启动流程（开机与手工共用）
# ============================================================
work() {
    STEP_N=0; FAILED=""
    say "===== mini-cloud 启动 ====="

    # [1/5] 检查本地文件（放在网络等待之前：缺文件不用白等 10 分钟）
    step 5 "检查本地文件"
    if ! preflight; then
        fail "配置问题：见上方原因，修复后重试"; say "===== 启动失败（第 1 步）====="; return 1
    fi
    ok "文件与目录就绪"

    # [2/5] 检测网络
    step 5 "检测网络"
    _i=1; _net=1
    while [ "$_i" -le "$NET_WAIT_MAX" ]; do
        stop_requested && { ok "收到停止请求，中止"; FAILED="stopped"; say "===== 已中止 ====="; return 0; }
        if net_ready; then _net=0; break; fi
        sleep 5; _i=$((_i+1))
    done
    if [ "$_net" = "0" ]; then ok "第 $_i 次尝试"
    else
        fail "环境问题：$((NET_WAIT_MAX*5)) 秒内未连上 $NET_PROBE，请检查宽带/网线后重试"; say "===== 启动失败（第 2 步）====="; return 1
    fi

    # [3/5] 检查系统时间（只报告，不负责同步）
    step 5 "检查系统时间"
    _i=1; _okt=0
    while [ "$_i" -le 30 ]; do
        _now=$(date +%s 2>/dev/null)
        if [ -n "$_now" ] && [ "$_now" -gt "$CLOCK_FLOOR_EPOCH" ]; then _okt=1; break; fi
        sleep 2; _i=$((_i+1))
    done
    if [ "$_okt" = "1" ]; then ok "$(date)"
    else
        fail "系统时间不可用（可能影响 TLS）：路由器 NTP 同步后重启服务即可"; say "===== 启动失败（第 3 步）====="; return 1
    fi

    # [4/5] 启动 mini-cloud
    step 5 "启动 mini-cloud"
    stop_requested && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
    if mc_running; then
        ok "已在运行"
    else
        mkdir -p "$(dirname "$MC_LOG")"
        ( cd "$MC_DIR" || exit 1; trap "" HUP; exec "$MC_BIN" -config "$MC_CFG" ) \
            >> "$MC_LOG" 2>&1 < /dev/null &
        ok "已启动（PID $!）"
    fi

    # [5/5] 等待 HTTP 就绪
    step 5 "等待 mini-cloud 就绪"
    load_auth
    if [ -z "$MC_USER" ] || [ -z "$MC_PASS" ]; then
        fail "配置问题：读不到 server.username/password"; say "===== 启动失败（第 5 步）====="; return 1
    fi
    _i=1; _rdy=0
    while [ "$_i" -le 30 ]; do
        stop_requested && { ok "收到停止请求，中止"; FAILED="stopped"; say "===== 已中止 ====="; return 0; }
        if wget -q -T 2 -O /dev/null "http://${MC_USER}:${MC_PASS}@${MC_HOST}:${MC_PORT}/" 2>/dev/null; then _rdy=1; break; fi
        sleep 2; _i=$((_i+1))
    done
    if [ "$_rdy" = "1" ]; then ok "HTTP 已响应（第 $_i 次尝试）"
    else
        fail "HTTP 未响应：请检查 $MC_LOG"; tail -n 5 "$MC_LOG" 2>/dev/null | sed 's/^/  mc: /' >> "$LOG"
        say "===== 启动失败（第 5 步）====="; return 1
    fi

    say "===== 启动完成 ====="
    return 0
}

# ============================================================
# rc.common 入口
# ============================================================
# OpenWrt 18.06 的 rc.common 默认 boot() 不把参数传给 start()（boot() { start "$@" }），
# 必须显式定义 boot() 转发，否则开机也会走人工前台模式（审核 P0，2026-11-27）
boot() { start boot; }

start() {
    if [ "${1:-}" = "boot" ]; then   # ${1:-}：手工调用时无参数，set -u 下也不会中止
        # 开机：后台执行，只写日志（boot 阶段主脚本会被 TERM，重活必须放子进程）
        MC_MODE=boot
        acquire_lock || return 1
        rm -f "$STOP_FLAG"
        _run=$(date +%s).$$
        ( MC_MODE=boot; work ) >> "$LOG" 2>&1 < /dev/null &
        echo "$_run $! $SELF" > "$WORK_PID"
        log "开机流程已放入后台（PID $!）"
        release_lock
    else
        # 手工：前台执行，每步实时打到屏幕
        MC_MODE=cli
        acquire_lock || return 1
        rm -f "$STOP_FLAG"
        work
        _rc=$?
        release_lock
        return $_rc
    fi
}

stop() {
    MC_MODE=cli
    acquire_lock || { say "另一个 start/stop 正在进行，本次 stop 失败"; return 1; }
    touch "$STOP_FLAG"

    say "===== mini-cloud 停止 ====="
    _rc=0

    # [1/3] 停止启动 worker（仅开机模式有；记录无效就跳过并提示）
    step 3 "停止启动 worker"
    _w=$(work_pid); _m=$(work_mark)
    if [ -n "$_w" ] && pid_is_worker "$_w" "$_m"; then
        kill "$_w" 2>/dev/null
        _i=0
        while [ "$_i" -lt "$STOP_WAIT" ] && pid_is_worker "$_w" "$_m"; do sleep 1; _i=$((_i+1)); done
        if pid_is_worker "$_w" "$_m"; then
            fail "worker 未确认退出：请 ps 确认后手工处理"; _rc=1
        else
            ok "worker 已退出"
        fi
    else
        ok "无正在运行的 worker（记录缺失/无效；如服务未停请手工确认）"
    fi

    # [2/3] 停止 mini-cloud
    step 3 "停止 mini-cloud"
    _p=$(pgrep -f "$MC_BIN"); [ -n "$_p" ] && kill $_p 2>/dev/null
    if wait_gone "$MC_BIN" "$STOP_WAIT"; then
        ok "mini-cloud 已退出"
    else
        fail "mini-cloud 未确认退出：请 ps 确认后手工处理"; _rc=1
    fi

    # [3/3] 清理状态（只有全部确认退出才清理，否则保留停止旗标防 worker 继续拉服务）
    step 3 "清理状态"
    if [ "$_rc" = "0" ]; then
        rm -f "$WORK_PID" "$STOP_FLAG"
        ok "已清理"; say "===== 停止完成 ====="
    else
        fail "存在未确认退出的进程，保留停止旗标与 PID 记录，请人工检查后重试"; say "===== 停止失败 ====="
    fi

    release_lock
    return $_rc
}
