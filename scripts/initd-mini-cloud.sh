#!/bin/sh /etc/rc.common
#
# ==============================================================================
#  initd-mini-cloud.sh —— mini-cloud 极简开机自启（范本 A）
# ==============================================================================
#
# 【用途】只把 mini-cloud 拉起来，等它的 HTTP 服务能响应后收工。不下载、不启动其他程序。
# 【适合】只需要 WebDAV（浏览 / 播放 / 下载）的用户。
# 【定位】正常路径可靠 + 异常明确告警；不做自动故障恢复（操作约定见 scripts/README.md）。
#
# 【安装】
#   cp initd-mini-cloud.sh /etc/init.d/mini-cloud
#   chmod +x /etc/init.d/mini-cloud
#   /etc/init.d/mini-cloud enable
#   reboot
#
# 【二选一（重要）】
#   若你用的是 initd-mini-cloud-mihomo.sh（开机从网盘取包并启动 mihomo），
#   本脚本不要再装——那个脚本自己管理 mini-cloud，两套同装会抢 8088 端口。
#
# 【适配环境（实测）】
#   OpenWrt ARM64（红米 AX6，BusyBox v1.25.1）：无 nohup / 无 setsid；
#   需要 curl、pgrep，AX6 自带。对应版本：mini-cloud v1.3.0-beta
#
# 【五条硬约束（改脚本前请先读，都是真机踩出来的）】
#   1) mini-cloud 必须在 /data/mini-cloud 下启动。state/ 相对「当前工作目录」，
#      从别处启动会找不到 state/<挂载名>_token.json，表现为云盘失效 / 反复重新登录。
#   2) 「重活」必须放到后台子进程里。boot 阶段 rc.common 会对主脚本发 TERM
#      （实测约 40 秒后），但不会波及其 fork 出来的子进程（实测存活 20 分钟）。
#   3) 不要用固定 sleep 等网络：改成有上限的探测。
#   4) start / stop 的并发必须互斥（mkdir 原子锁）：
#        - 两个 start 同时跑 → 只有一个 mkdir 成功；
#        - stop 进行中 → start 拿不到锁，不会提前清停止旗标；
#        - 锁残留且持锁进程已退出时：**不自动回收**，报错并要求人工确认后删除。
#          原因：自动回收（rm 或 mv）在「A 回收 → C 新建 → B 回收」的时序下会误删
#          新锁，反而破坏互斥。锁在 /tmp（重启即清空），开机流程不会遇到残留锁；
#          只有手工 start/stop 被异常杀死时才会残留，此时人工处理更安全。
#   5) 停止流程以「确认 worker 已退出」为完成条件：
#        - 发 TERM / KILL 之前都用 /proc/<pid>/cmdline 校验身份（防 PID 复用误杀）；
#        - 只对「PID 记录可验证」的 worker 发信号；记录缺失/损坏时只告警 + 返回失败，
#          不扫描、不杀未知进程（脚本负责不误操作，异常恢复由用户按 README 处理）；
#        - worker 未确认退出时保留停止旗标并返回失败，不把「状态未知」当成「已停好」。
#
# 【PID 记录 / 身份标记】
#   WORK_PID 内容为「运行号 PID 身份标记」。它只由 start() 写、stop() 删，
#   worker 自己不碰（避免「worker 先退出、父进程后写」的竞态）。
#   身份标记取 rc.common 提供的 initscript 变量（即 /etc/init.d/xxx 或
#   /etc/rc.d/S99xxx）——不能用 $0：shebang 是 #!/bin/sh /etc/rc.common 时，
#   $0 是 /etc/rc.common，会导致「任何 rc.common 进程都算自己的 worker」。
#
# 【端口 / 地址】MC_HOST + MC_PORT 必须与 config.json 的 server.listen 一致。
# ==============================================================================

START=99                      # 开机顺序：尽量靠后（等网络/存储就绪）

# ---- mini-cloud 路径与访问地址（按你的实际部署改；<...> 占位符必须替换）----
MC_DIR=/data/mini-cloud
MC_BIN="$MC_DIR/mini-cloud-arm64-upx"
MC_CFG="$MC_DIR/config/config.json"
MC_LOG="$MC_DIR/log/mini-cloud.log"
MC_HOST="<router-ip>"         # 路由器局域网地址（就绪探测用）
MC_PORT=8088                  # 必须与 config.json 的 server.listen 端口一致
MC_USER=""                    # 运行时从 config.json 的 server 段读取
MC_PASS=""

# ---- 等待参数 ----
NET_WAIT_MAX=120              # 网络等待上限：120 × 5 秒 = 10 分钟
NET_PROBE=http://cloud.189.cn # 探测目标；用 http 不用 https（BusyBox 不一定带 TLS）
CLOCK_FLOOR_EPOCH=1767225600  # 时钟下限 2026-01-01 UTC（只是「明显错误」过滤，见第 1.5 层）
STOP_WAIT=10                  # 等 worker 退出的上限（秒）

# ---- 身份标记与内部文件 ----
# 身份标记：优先用 rc.common 的 initscript（真正被调用的脚本路径）；
# 直接 sh /etc/init.d/xxx start 时没有该变量，退回 $0。
# 留空或异常时可手工改为实际路径，例如 SELF=/etc/init.d/mini-cloud
SELF="${initscript:-$0}"

LOG=/tmp/mini-cloud-boot.log  # 开机流程日志（/tmp 是内存盘，重启即清空）
WORK_PID=/tmp/mini-cloud-work.pid   # 「运行号 PID 身份标记」
LOCK=/tmp/mini-cloud.lock           # mkdir 原子锁（start/stop 互斥）
STOP_FLAG=/tmp/mini-cloud.stop      # 停止旗标（worker 在各阶段边界检查）

# ==============================================================================
# 以下为脚本主体
# ==============================================================================

log() { echo "$(date '+%m-%d %H:%M:%S') $*" >> "$LOG"; }

# ------------------------------------------------------------------
# 原子锁：mkdir 成功即持锁。
# 残留锁不自动回收（自动回收在并发下会误删新锁，见文件头约束 4）。
# ------------------------------------------------------------------
acquire_lock() {
    _i=0
    while ! mkdir "$LOCK" 2>/dev/null; do
        _lp=$(cat "$LOCK/pid" 2>/dev/null)
        if [ -n "$_lp" ] && ! kill -0 "$_lp" 2>/dev/null; then
            log "启动锁残留（持锁进程 $_lp 已退出）：确认没有 start/stop 在跑后，手工 rm -rf $LOCK"
            return 1
        fi
        _i=$((_i+1))
        if [ "$_i" -gt 50 ]; then
            log "获取启动锁超时（另一个 start/stop 正在进行）"
            return 1
        fi
        sleep 1
    done
    echo $$ > "$LOCK/pid"
    return 0
}

# 释放锁前校验归属：只删自己创建的锁
release_lock() {
    _lp=$(cat "$LOCK/pid" 2>/dev/null)
    if [ "$_lp" = "$$" ]; then
        rm -rf "$LOCK"
    else
        log "警告：启动锁不属于本进程（记录 pid=${_lp:-无}），不删除"
    fi
}

work_pid()  { cut -d' ' -f2 "$WORK_PID" 2>/dev/null; }
work_mark() { cut -d' ' -f3 "$WORK_PID" 2>/dev/null; }

# 校验 PID 是否确实是本脚本的 worker（防止 PID 被系统复用后误判 / 误杀）。
# 判据：PID 存活 + /proc/<pid>/cmdline 含身份标记（initscript 路径）。
pid_is_worker() {
    _p="$1"; _m="$2"
    [ -n "$_p" ] || return 1
    [ -n "$_m" ] || return 1
    kill -0 "$_p" 2>/dev/null || return 1
    tr '\0' ' ' < "/proc/$_p/cmdline" 2>/dev/null | grep -qF -- "$_m"
}

worker_alive() { pid_is_worker "$(work_pid)" "$(work_mark)"; }

# 等某类进程消失（最多 N 秒）；0 = 已消失
wait_gone() {
    _pat="$1"; _lim="$2"; _i=0
    while [ "$_i" -lt "$_lim" ]; do
        pgrep -f "$_pat" >/dev/null 2>&1 || return 0
        sleep 1
        _i=$((_i+1))
    done
    pgrep -f "$_pat" >/dev/null 2>&1 && return 1
    return 0
}

# 启动前自检：缺文件时快速失败，不要白等 10 分钟网络
preflight() {
    [ -d "$MC_DIR" ]  || { log "错误：目录不存在 $MC_DIR"; return 1; }
    [ -x "$MC_BIN" ]  || { log "错误：二进制不存在或不可执行 $MC_BIN"; return 1; }
    [ -f "$MC_CFG" ]  || { log "错误：配置文件不存在 $MC_CFG"; return 1; }
    mkdir -p "$(dirname "$MC_LOG")" 2>/dev/null
    ( : >> "$MC_LOG" ) 2>/dev/null || { log "错误：日志不可写 $MC_LOG"; return 1; }
    return 0
}

# 从 config.json 读 Basic Auth 凭据（就绪探测要用）。
# 限定在 server 段内解析：截到 "clouds" 那一行为止，避免取到 clouds[] 里的同名字段。
# 注意：依赖「server 段始终在 clouds 之前」这一当前配置结构。
load_auth() {
    _srv=$(sed -n '1,/"clouds"/p' "$MC_CFG" 2>/dev/null)
    MC_USER=$(echo "$_srv" | grep -o '"username"[[:space:]]*:[[:space:]]*"[^"]*"' \
              | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
    MC_PASS=$(echo "$_srv" | grep -o '"password"[[:space:]]*:[[:space:]]*"[^"]*"' \
              | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
}

# 网络就绪探测：拿到任何 HTTP 响应（含 3xx/4xx）即认为链路通。
# -sS：静默进度但保留错误信息（日志里才能看到 DNS/连接/超时的具体原因）。
net_ready() {
    _r=$(curl -sS -m 5 -o /dev/null -w 'code=%{http_code}' "$NET_PROBE" 2>&1 | tr '\n' ' ')
    log "net probe: $_r"
    case "$_r" in
        code=[1-9]*) return 0 ;;
        *)           return 1 ;;
    esac
}

mc_running() { pgrep -f "$MC_BIN" >/dev/null 2>&1; }

stop_requested() { [ -f "$STOP_FLAG" ]; }

work() {
    preflight || return 1

    # ------------------------------------------------------------------
    # 第 1 层：等网络链路就绪
    # mini-cloud 启动时会登录天翼 / 校验百度 token，网络没通就失败；
    # v1.3.0 的「单挂载启动隔离」只保证服务能起来，失败挂载不会自动重试。
    # ------------------------------------------------------------------
    log "wait network (max $((NET_WAIT_MAX * 5))s, probe: $NET_PROBE)"
    i=1
    while [ $i -le $NET_WAIT_MAX ]; do
        stop_requested && { log "stop requested, abort (network wait)"; return 0; }
        if net_ready; then
            log "network ready (attempt $i)"
            break
        fi
        sleep 5
        i=$((i+1))
    done
    [ $i -gt $NET_WAIT_MAX ] && { log "network timeout, abort"; return 1; }

    # ------------------------------------------------------------------
    # 第 1.5 层：时钟下限检查（不阻塞）
    # 只是「明显错误」过滤（比如断电后回到 1970），不能证明 NTP 已同步；
    # 等 60 秒仍不过就告警并继续，由服务在 TLS 失败时降级。
    # ------------------------------------------------------------------
    i=1
    while [ $i -le 30 ]; do
        _now=$(date +%s 2>/dev/null)
        if [ -n "$_now" ] && [ "$_now" -gt "$CLOCK_FLOOR_EPOCH" ]; then
            log "clock above floor: $(date)"
            break
        fi
        log "clock still below floor: $(date)"
        sleep 2
        i=$((i+1))
    done
    [ $i -gt 30 ] && log "warning: clock may be wrong (TLS may fail), continue anyway"

    # ------------------------------------------------------------------
    # 第 2 层：启动 mini-cloud
    # - 子 shell 内 cd：state/ 相对工作目录（硬约束 1）
    # - trap "" HUP：忽略挂断，SSH 里手工 restart 时关窗口也不会被带走
    # - exec：替换子 shell 本身，PID 不变
    # ------------------------------------------------------------------
    stop_requested && { log "stop requested, abort (before launch)"; return 0; }
    mkdir -p "$(dirname "$MC_LOG")"
    if mc_running; then
        log "mini-cloud already running"
    else
        ( cd "$MC_DIR" || exit 1; trap "" HUP; exec "$MC_BIN" -config "$MC_CFG" ) \
            >> "$MC_LOG" 2>&1 < /dev/null &
        log "mini-cloud launched (pid $!)"
    fi

    # ------------------------------------------------------------------
    # 第 3 层：等 HTTP 服务就绪
    # 判据 = 带认证访问根目录返回 200，等价于「各挂载 Init 都已结束」；
    # 但它 **不等于** 「所有云盘都正常」——失败挂载是首页和 /admin 里的红卡，
    # 服务本身仍然可用（v1.3.0 单挂载隔离）。
    # 不要用固定 sleep 代替：真机实测启动 8~37 秒不等。
    # ------------------------------------------------------------------
    load_auth
    if [ -z "$MC_USER" ] || [ -z "$MC_PASS" ]; then
        log "错误：读不到 server.username / server.password，无法做就绪探测（abort）"
        return 1
    fi
    i=1
    while [ $i -le 30 ]; do
        stop_requested && { log "stop requested, abort (readiness)"; return 0; }
        if wget -q -T 2 -O /dev/null "http://${MC_USER}:${MC_PASS}@${MC_HOST}:${MC_PORT}/" 2>/dev/null; then
            log "mini-cloud HTTP ready (attempt $i)"
            break
        fi
        sleep 2
        i=$((i+1))
    done
    if [ $i -gt 30 ]; then
        log "mini-cloud HTTP not ready, abort (see $MC_LOG)"
        tail -n 5 "$MC_LOG" 2>/dev/null | sed 's/^/  mc: /' >> "$LOG"
        return 1
    fi

    log "===== started ====="
}

# worker 包装：worker 自己不碰 WORK_PID（记录只由 start/stop 管理）
worker() {
    work
    log "worker exit (run $1)"
}

# ------------------------------------------------------------------
# rc.common 入口
# ------------------------------------------------------------------
start() {
    touch "$LOG"
    echo "" >> "$LOG"
    echo "===== boot $(date) =====" >> "$LOG"

    # 原子锁：保证「检查 + 启动」是互斥的（普通 [ -f PID ] 只是检查，不是互斥）
    acquire_lock || { log "另一个 start/stop 正在进行，跳过本次 start"; return 1; }

    # 双重检查：已有 worker 活着（且身份匹配）就不再起第二个
    if worker_alive; then
        log "worker already running (pid $(work_pid)), skip start"
        release_lock
        return 0
    fi

    # 持锁期间才允许清停止旗标：此时不可能有 stop 正在收尾
    rm -f "$STOP_FLAG"

    _run=$(date +%s).$$
    ( worker "$_run" ) >> "$LOG" 2>&1 < /dev/null &
    _wpid=$!
    echo "$_run $_wpid $SELF" > "$WORK_PID"
    # 自检：身份标记必须出现在 worker 的命令行里，否则停止时无法校验身份
    if kill -0 "$_wpid" 2>/dev/null && ! pid_is_worker "$_wpid" "$SELF"; then
        log "警告：身份标记 $SELF 未出现在 worker 命令行中，停止时将无法校验身份（请检查 initscript 变量或手工设置 SELF）"
    fi
    log "worker launched (pid $_wpid, run $_run, mark $SELF)"

    release_lock
}

stop() {
    touch "$LOG"
    echo "" >> "$LOG"
    echo "===== stop $(date) =====" >> "$LOG"

    # 与 start 互斥：stop 收尾期间不会被新 start 清掉旗标、抢跑启动
    acquire_lock || { log "另一个 start/stop 正在进行，本次 stop 失败"; return 1; }

    # 1) 立停止旗标：worker 在每个阶段边界检查，避免 stop 之后又被拉起
    touch "$STOP_FLAG"

    # 2) 停 worker：TERM 和 KILL 之前都做身份校验（防 PID 复用误杀）
    _w=$(work_pid)
    _mark=$(work_mark)
    _confirmed=1

    if [ -n "$_w" ] && [ "$_w" -ge 1 ] 2>/dev/null && [ -n "$_mark" ]; then
        if pid_is_worker "$_w" "$_mark"; then
            kill "$_w" 2>/dev/null
            i=0
            while [ "$i" -lt "$STOP_WAIT" ] && pid_is_worker "$_w" "$_mark"; do
                sleep 1
                i=$((i+1))
            done
            if pid_is_worker "$_w" "$_mark"; then
                kill -9 "$_w" 2>/dev/null
                sleep 1
            fi
            pid_is_worker "$_w" "$_mark" && _confirmed=0
        else
            log "记录的 worker pid $_w 已不是本脚本进程（已退出或 PID 复用），清理过期记录"
        fi
    else
        # PID 记录缺失或损坏（RV-2026-11-26-02：脚本负责不误操作）：
        # 状态未知时不扫描、不杀任何未验证身份的进程——明确告警并返回失败，
        # 由用户按 scripts/README.md「异常处理」章节手工确认后再操作。
        log "错误：worker PID 记录缺失或格式异常，无法确认 worker 状态"
        log "       未做任何进程清理。请先手工确认没有残留 worker，再清理 $WORK_PID 后重试"
        _confirmed=0
    fi

    # 3) 停 mini-cloud（归属约定：本脚本是该服务的唯一管理者，按二进制完整路径停止）
    _p=$(pgrep -f "$MC_BIN")
    [ -n "$_p" ] && kill $_p 2>/dev/null
    # RV-2026-11-26-01（P2）：服务未确认退出 = 停止失败，不得报成功
    if ! wait_gone "$MC_BIN" 5; then
        log "错误：mini-cloud 5 秒内仍未退出（停止未确认）"
        _confirmed=0
    fi

    # 4) 只有确认 worker 与服务都已退出才撤销旗标、删记录；否则保留并返回失败
    if [ "$_confirmed" = "1" ]; then
        rm -f "$WORK_PID"
        rm -f "$STOP_FLAG"
        log "stopped"
    else
        log "错误：停止未完全确认（worker 或 mini-cloud 仍有残留），保留停止旗标与 PID 记录，请手工检查"
    fi

    release_lock
    [ "$_confirmed" = "1" ] && return 0
    return 1
}
