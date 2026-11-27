#!/bin/sh /etc/rc.common
#
# initd-mini-cloud-mihomo.sh —— 开机：启动 mini-cloud，再从网盘加密单元取 mihomo 并启动（范本 B）
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
#   cp initd-mini-cloud-mihomo.sh /etc/init.d/alist_init
#   chmod +x /etc/init.d/alist_init
#   /etc/init.d/alist_init enable
#   reboot
#
# 【二选一】本脚本自己管理 mini-cloud；不要再安装范本 A（会抢 8088 端口）。
#
# 【真机硬约束（都是实测踩出来的）】
#   1) mini-cloud 必须在 /data/mini-cloud 下启动（state/ 相对工作目录）。
#   2) 开机流程放在后台子进程里跑：boot 阶段 rc.common 会对主脚本发 TERM（实测约 40 秒），
#      不会波及它 fork 出来的子进程。
#   3) mihomo 包必须放在「加密单元」里（默认 backup/AX6）：普通目录会 302 跳到 https，
#      BusyBox wget 没有 TLS；加密单元由 mini-cloud 解密后以 HTTP 直出。
#   4) 所有等待都有上限；不用固定 sleep 等网络/就绪。
#
# 【凭据传递（已知取舍）】就绪探测与取包用 URL 内嵌凭据（AX6 的 BusyBox wget 无
#   --user/--password）。代价：密码出现在 wget 命令行（ps 可见；路由器整机 root 单用户，
#   风险有限）；密码含 @ : / 等字符时 URL 解析会出错——那种情况需改用 Basic 头。
#
# 【占位符】下面带 <...> 的值必须按你的实际部署替换。

START=99

# ---- mini-cloud ----
MC_DIR=/data/mini-cloud
MC_BIN="$MC_DIR/mini-cloud-arm64-upx"
MC_CFG="$MC_DIR/config/config.json"
MC_LOG="$MC_DIR/log/mini-cloud.log"
MC_HOST="<router-ip>"
MC_PORT=8088
MC_USER=""
MC_PASS=""

# ---- 云盘 / 包 ----
CLOUD_NAME_1="<云盘名-1>"      # = config.json 里 clouds[].name（主用云盘）
CLOUD_NAME_2="<云盘名-2>"      # = config.json 里 clouds[].name（备用云盘）
CLOUD_SUBDIR=backup/AX6         # 加密单元路径（容器名/单元名）
MIHOMO_TGZ=mihomo.tar.gz

# ---- mihomo ----
MIHOMO_DIR=/tmp/mihomo
DATA_DIR=/data/mihomo
MIHOMO_BIN="$MIHOMO_DIR/mihomo-linux-arm64"
MIHOMO_API="http://<router-ip>:9092/version"
MIHOMO_TOKEN=123456             # ★示例值：必须改成与 mihomo 配置一致的 secret
CLIENT_IP="<client-ip>"
MIHOMO_PORT=7892

# ---- 等待参数 ----
NET_WAIT_MAX=120              # 120 × 5 秒 = 10 分钟
NET_PROBE=http://cloud.189.cn
CLOCK_FLOOR_EPOCH=1767225600  # 时钟下限 2026-01-01 UTC（只做「明显错误」过滤）
FETCH_TRIES=3
WGET_TIMEOUT=15
STOP_WAIT=5                   # 等进程退出的上限（秒）

# ---- 内部文件 ----
SELF="${initscript:-$0}"
LOG=/tmp/mini-cloud-mihomo.log
WORK_PID=/tmp/mini-cloud-mihomo-work.pid
LOCK=/tmp/mini-cloud-mihomo.lock
STOP_FLAG=/tmp/mini-cloud-mihomo.stop
MC_MODE=boot

# ============================================================
# 输出：ts 前缀 + 日志必写；cli 模式同时写屏幕
# ============================================================
ts() { date '+%m-%d %H:%M:%S'; }
say() {
    _m="$(ts) $*"
    echo "$_m" >> "$LOG"
    [ "$MC_MODE" = "cli" ] && echo "$_m"
}
log() { echo "$(ts) $*" >> "$LOG"; }

STEP_N=0; STEP_TOTAL=0
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

pid_is_worker() {
    _p="$1"; _m="$2"
    [ -n "$_p" ] && [ -n "$_m" ] || return 1
    kill -0 "$_p" 2>/dev/null || return 1
    tr '\0' ' ' < "/proc/$_p/cmdline" 2>/dev/null | grep -qF -- "$_m"
}

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

preflight() {
    [ -d "$MC_DIR" ] || { log "配置问题：目录不存在 $MC_DIR"; return 1; }
    [ -x "$MC_BIN" ] || { log "配置问题：二进制不存在或不可执行 $MC_BIN"; return 1; }
    [ -f "$MC_CFG" ] || { log "配置问题：配置文件不存在 $MC_CFG"; return 1; }
    mkdir -p "$(dirname "$MC_LOG")" 2>/dev/null
    ( : >> "$MC_LOG" ) 2>/dev/null || { log "配置问题：日志不可写 $MC_LOG"; return 1; }
    return 0
}

net_ready() {
    _r=$(curl -sS -m 5 -o /dev/null -w 'code=%{http_code}' "$NET_PROBE" 2>&1 | tr '\n' ' ')
    log "net probe: $_r"
    case "$_r" in
        code=[1-9]*) return 0 ;;
        *)           return 1 ;;
    esac
}

load_auth() {
    _srv=$(sed -n '1,/"clouds"/p' "$MC_CFG" 2>/dev/null)
    MC_USER=$(echo "$_srv" | grep -o '"username"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
    MC_PASS=$(echo "$_srv" | grep -o '"password"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
}

mc_running() { pgrep -f "$MC_BIN" >/dev/null 2>&1; }

# 一轮取包：两个云盘依次试。0=成功 1=本轮失败 2=收到停止请求
fetch_once() {
    for cloud in "$CLOUD_NAME_1" "$CLOUD_NAME_2"; do
        stop_requested && { log "收到停止请求（取包 $cloud）"; return 2; }
        _url="http://${MC_USER}:${MC_PASS}@${MC_HOST}:${MC_PORT}/${cloud}/${CLOUD_SUBDIR}/${MIHOMO_TGZ}"
        _safe=$(echo "$_url" | sed 's/:[^:@/]*@/:***@/')
        rm -f /tmp/mihomo.tar.gz
        log "fetch $_safe"
        if wget -q -T "$WGET_TIMEOUT" -O /tmp/mihomo.tar.gz "$_url" \
           && tar -tzf /tmp/mihomo.tar.gz 2>/dev/null | grep -q 'mihomo/'; then
            log "got ${MIHOMO_TGZ} from [${cloud}] ($(wc -c < /tmp/mihomo.tar.gz) bytes)"
            return 0
        fi
        rm -f /tmp/mihomo.tar.gz
        log "  [${cloud}] failed"
    done
    return 1
}

# ============================================================
# 启动流程（开机与手工共用）
# ============================================================
work() {
    STEP_N=0
    say "===== mini-cloud + mihomo 启动 ====="

    # [1/7] 检测网络
    step 7 "检测网络"
    _i=1; _net=1
    while [ "$_i" -le "$NET_WAIT_MAX" ]; do
        stop_requested && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
        if net_ready; then _net=0; break; fi
        sleep 5; _i=$((_i+1))
    done
    if [ "$_net" = "0" ]; then ok "第 $_i 次尝试"
    else
        fail "环境问题：$((NET_WAIT_MAX*5)) 秒内未连上 $NET_PROBE，请检查宽带/网线后重试"; say "===== 启动失败（第 1 步）====="; return 1
    fi

    # [2/7] 检查系统时间（只报告，不负责同步）
    step 7 "检查系统时间"
    _i=1; _okt=0
    while [ "$_i" -le 30 ]; do
        _now=$(date +%s 2>/dev/null)
        if [ -n "$_now" ] && [ "$_now" -gt "$CLOCK_FLOOR_EPOCH" ]; then _okt=1; break; fi
        sleep 2; _i=$((_i+1))
    done
    if [ "$_okt" = "1" ]; then ok "$(date)"
    else
        fail "系统时间不可用（可能影响 TLS）：路由器 NTP 同步后重启服务即可"; say "===== 启动失败（第 2 步）====="; return 1
    fi

    # [3/7] 启动 mini-cloud
    step 7 "启动 mini-cloud"
    if ! preflight; then
        fail "配置问题：见上方日志行，修复后重试"; say "===== 启动失败（第 3 步）====="; return 1
    fi
    stop_requested && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
    if mc_running; then
        ok "已在运行"
    else
        mkdir -p "$(dirname "$MC_LOG")"
        ( cd "$MC_DIR" || exit 1; trap "" HUP; exec "$MC_BIN" -config "$MC_CFG" ) \
            >> "$MC_LOG" 2>&1 < /dev/null &
        ok "已启动（PID $!）"
    fi

    # [4/7] 等待 mini-cloud 就绪
    step 7 "等待 mini-cloud 就绪"
    load_auth
    if [ -z "$MC_USER" ] || [ -z "$MC_PASS" ]; then
        fail "配置问题：读不到 server.username/password"; say "===== 启动失败（第 4 步）====="; return 1
    fi
    _i=1; _rdy=0
    while [ "$_i" -le 30 ]; do
        stop_requested && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
        if wget -q -T 2 -O /dev/null "http://${MC_USER}:${MC_PASS}@${MC_HOST}:${MC_PORT}/" 2>/dev/null; then _rdy=1; break; fi
        sleep 2; _i=$((_i+1))
    done
    if [ "$_rdy" = "1" ]; then ok "HTTP 已响应（第 $_i 次尝试）"
    else
        fail "HTTP 未响应：请检查 $MC_LOG"; tail -n 5 "$MC_LOG" 2>/dev/null | sed 's/^/  mc: /' >> "$LOG"
        say "===== 启动失败（第 4 步）====="; return 1
    fi

    # [5/7] 下载 mihomo（双云盘互备，有限重试）
    step 7 "下载 mihomo"
    _ok=0; _t=1
    while [ "$_t" -le "$FETCH_TRIES" ]; do
        stop_requested && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
        fetch_once
        _rc=$?
        [ "$_rc" = "0" ] && { _ok=1; break; }
        [ "$_rc" = "2" ] && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
        log "第 $_t/$FETCH_TRIES 轮失败，10 秒后重试"
        sleep 10; _t=$((_t+1))
    done
    if [ "$_ok" = "1" ]; then ok "$(wc -c < /tmp/mihomo.tar.gz) 字节"
    else
        fail "两个云盘都取不到包：请检查加密单元内 $CLOUD_SUBDIR/$MIHOMO_TGZ 是否存在且完整"; say "===== 启动失败（第 5 步）====="; return 1
    fi

    # [6/7] 解压并启动 mihomo
    step 7 "解压并启动 mihomo"
    stop_requested && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
    rm -rf "$MIHOMO_DIR"
    cd /tmp || { fail "cd /tmp 失败"; say "===== 启动失败（第 6 步）====="; return 1; }
    if ! tar -zxvf mihomo.tar.gz >> "$LOG" 2>&1; then
        rm -rf "$MIHOMO_DIR"; rm -f mihomo.tar.gz
        fail "解压失败（包损坏或磁盘满）：请重新上传完整包后重试"; say "===== 启动失败（第 6 步）====="; return 1
    fi
    rm -f mihomo.tar.gz
    if [ ! -s "$MIHOMO_BIN" ]; then
        rm -rf "$MIHOMO_DIR"
        fail "解压后 $MIHOMO_BIN 不存在"; say "===== 启动失败（第 6 步）====="; return 1
    fi
    "$MIHOMO_BIN" -d "$DATA_DIR" >> /dev/null 2>&1 &
    _mp=$!
    _i=1; _ok=0
    while [ "$_i" -le 15 ]; do
        kill -0 "$_mp" 2>/dev/null || break
        if wget -q -T 2 -O /tmp/mihomo_check \
             --header="Authorization: Bearer ${MIHOMO_TOKEN}" "$MIHOMO_API" 2>/dev/null; then
            rm -f /tmp/mihomo_check; _ok=1; break
        fi
        sleep 1; _i=$((_i+1))
    done
    if [ "$_ok" = "1" ]; then ok "mihomo API 已响应（PID $_mp）"
    else
        rm -f /tmp/mihomo_check
        kill "$_mp" 2>/dev/null; sleep 1
        kill -0 "$_mp" 2>/dev/null && kill -9 "$_mp" 2>/dev/null
        fail "mihomo 未就绪（进程已退出或 API 无响应）：请检查 mihomo 配置与日志"; say "===== 启动失败（第 6 步）====="; return 1
    fi

    # [7/7] 配置网络转发
    step 7 "配置网络转发"
    stop_requested && { ok "收到停止请求，中止"; say "===== 已中止 ====="; return 0; }
    if echo 1 > /proc/sys/net/ipv4/ip_forward 2>/dev/null; then
        log "ip_forward enabled"
    else
        log "警告：无法开启 ip_forward（继续尝试加规则）"
    fi
    while iptables -t nat -D PREROUTING -s "$CLIENT_IP" -p tcp \
             -j REDIRECT --to-port "$MIHOMO_PORT" 2>/dev/null; do :; done
    if iptables -t nat -A PREROUTING -s "$CLIENT_IP" -p tcp \
             -j REDIRECT --to-port "$MIHOMO_PORT" 2>>"$LOG"; then
        ok "ip_forward 与 iptables 规则已就绪"
    else
        fail "iptables 规则添加失败（见上方日志）"; say "===== 启动失败（第 7 步）====="; return 1
    fi

    say "===== 启动完成 ====="
    return 0
}

# ============================================================
# rc.common 入口
# ============================================================
start() {
    if [ "$1" = "boot" ]; then
        MC_MODE=boot
        acquire_lock || return 1
        rm -f "$STOP_FLAG"
        _run=$(date +%s).$$
        ( MC_MODE=boot; work ) >> "$LOG" 2>&1 < /dev/null &
        echo "$_run $! $SELF" > "$WORK_PID"
        log "开机流程已放入后台（PID $!）"
        release_lock
    else
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

    say "===== mini-cloud + mihomo 停止 ====="
    _rc=0

    # [1/5] 停止启动 worker
    step 5 "停止启动 worker"
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

    # [2/5] 停止 mini-cloud
    step 5 "停止 mini-cloud"
    _p=$(pgrep -f "$MC_BIN"); [ -n "$_p" ] && kill $_p 2>/dev/null
    if wait_gone "$MC_BIN" "$STOP_WAIT"; then
        ok "mini-cloud 已退出"
    else
        fail "mini-cloud 未确认退出：请 ps 确认后手工处理"; _rc=1
    fi

    # [3/5] 停止取包进程（识别特征：wget + 输出文件；请不动就报错，不强杀）
    step 5 "停止取包进程"
    _fp="wget.*mihomo\\.tar\\.gz"
    _p=$(pgrep -f "$_fp"); [ -n "$_p" ] && kill $_p 2>/dev/null
    if wait_gone "$_fp" "$WGET_TIMEOUT"; then
        rm -f /tmp/mihomo.tar.gz
        ok "无取包进程（残留文件已清理）"
    else
        fail "取包 wget 可能仍在运行（最多 ${WGET_TIMEOUT} 秒自行结束），请稍候再 start"; _rc=1
    fi

    # [4/5] 停止 mihomo
    step 5 "停止 mihomo"
    _p=$(pgrep -f mihomo-linux-arm64); [ -n "$_p" ] && kill $_p 2>/dev/null
    if wait_gone mihomo-linux-arm64 "$STOP_WAIT"; then
        ok "mihomo 已退出"
    else
        fail "mihomo 未确认退出：请 ps 确认后手工处理"; _rc=1
    fi

    # [5/5] 清理 iptables 规则 + 状态
    step 5 "清理 iptables 规则"
    while iptables -t nat -D PREROUTING -s "$CLIENT_IP" -p tcp \
             -j REDIRECT --to-port "$MIHOMO_PORT" 2>/dev/null; do :; done
    rm -f "$WORK_PID" "$STOP_FLAG"
    if [ "$_rc" = "0" ]; then
        ok "规则已清理"; say "===== 停止完成 ====="
    else
        ok "规则已清理（但上方有未确认退出）"; say "===== 停止失败 ====="
    fi

    release_lock
    return $_rc
}
