#!/bin/sh /etc/rc.common
#
# ==============================================================================
#  initd-mini-cloud-mihomo.sh —— 开机：启动 mini-cloud，再从网盘取 mihomo 并启动
#  （范本 B，完整版）
# ==============================================================================
#
# 【用途】开机后依次完成：
#   等网络 → 时钟下限检查 → 启动 mini-cloud → 等 HTTP 就绪 →
#   从网盘取 mihomo 包（天翼 / 移动 双云盘互备）→ 校验 → 解压 → 启动 mihomo →
#   开 ip_forward → 加 iptables 重定向规则。
#
# 【适合谁】除了 WebDAV，还要在开机时从云盘自动取回其他程序（默认示例是 mihomo）的用户。
#          只想要 WebDAV 的用户请用 initd-mini-cloud.sh（范本 A）。
# 【定位】正常路径可靠 + 异常明确告警；不做自动故障恢复（操作约定见 scripts/README.md）。
#
# 【安装】
#   cp initd-mini-cloud-mihomo.sh /etc/init.d/alist_init
#   chmod +x /etc/init.d/alist_init
#   /etc/init.d/alist_init enable
#   reboot
#
# 【二选一（重要）】
#   本脚本自己管理 mini-cloud，不要再安装范本 A（/etc/init.d/mini-cloud）——
#   两套同装会抢 8088 端口，表现为其中一个进程起不来。
#
# 【适配环境（实测）】
#   OpenWrt ARM64（红米 AX6，BusyBox v1.25.1）：无 nohup / 无 setsid；
#   需要 curl、pgrep、wget、tar、iptables，AX6 自带。
#   对应版本：mini-cloud v1.3.0-beta
#
# 【六条硬约束（改脚本前请先读，都是真机踩出来的）】
#   1) mini-cloud 必须在 /data/mini-cloud 下启动。state/ 相对「当前工作目录」，
#      从别处启动会找不到 state/<挂载名>_token.json，表现为云盘失效 / 反复重新登录。
#   2) 「重活」必须放到后台子进程里。boot 阶段 rc.common 会对主脚本发 TERM
#      （实测约 40 秒后），但不会波及其 fork 出来的子进程（实测存活 20 分钟）。
#   3) 包必须放在「加密单元」里（默认 backup/AX6），不能放普通目录。
#      普通目录会 302 跳到云盘 CDN 的 https 地址，而 BusyBox wget 没有 TLS，
#      会直接报 "not an http or ftp url"；加密单元由 mini-cloud 解密后用 HTTP 直出。
#   4) 不要用固定 sleep 等网络 / 等就绪，全部改成有上限的探测。
#   5) start / stop 的并发必须互斥（mkdir 原子锁）：
#        - 两个 start 同时跑 → 只有一个 mkdir 成功；
#        - stop 进行中 → start 拿不到锁，不会提前清停止旗标；
#        - 锁残留且持锁进程已退出时：**不自动回收**，报错并要求人工确认后删除。
#          原因：自动回收（rm 或 mv）在「A 回收 → C 新建 → B 回收」的时序下会误删
#          新锁，反而破坏互斥。锁在 /tmp（重启即清空），开机流程不会遇到残留锁。
#   6) 停止流程以「确认 worker 与全部受管服务都已退出」为完成条件：
#        - 发 TERM / KILL 之前都用 /proc/<pid>/cmdline 校验身份（防 PID 复用误杀）；
#        - 只对「PID 记录可验证」的 worker 与「wget+输出文件双特征可识别」的取包 wget 发信号；
#          记录缺失/损坏时只告警 + 返回失败，不扫描、不杀未知进程
#          （脚本负责不误操作，异常恢复由用户按 README 处理）；
#        - 任一受管进程未确认退出时保留停止旗标并返回失败，不把「状态未知」当成「已停好」。
#
# 【PID 记录 / 身份标记】
#   WORK_PID 内容为「运行号 PID 身份标记」。它只由 start() 写、stop() 删，
#   worker 自己不碰（避免「worker 先退出、父进程后写」的竞态）。
#   身份标记取 rc.common 提供的 initscript 变量（即 /etc/init.d/xxx 或
#   /etc/rc.d/S99xxx）——不能用 $0：shebang 是 #!/bin/sh /etc/rc.common 时，
#   $0 是 /etc/rc.common，会导致「任何 rc.common 进程都算自己的 worker」。
#
# 【凭据传递方式（已知取舍）】
#   当前用「URL 内嵌凭据」(http://user:pass@host/...)——AX6 实测可用的方式，
#   因为 BusyBox wget 1.25.1 没有 --user/--password 选项。
#   代价与限制：
#     1) 密码会出现在 wget 命令行里，`ps` 可见（路由器整机 root、单用户，风险有限）；
#     2) 密码含 @ : / 等字符时 URL 解析会出错，此时应改用 Basic 头：
#          AUTH=$(printf '%s:%s' "$MC_USER" "$MC_PASS" | base64 | tr -d '\n')
#          wget -q -T 15 -O /tmp/mihomo.tar.gz \
#              --header="Authorization: Basic $AUTH" \
#              "http://${MC_HOST}:${MC_PORT}/${cloud}/${CLOUD_SUBDIR}/${MIHOMO_TGZ}"
#        改用前请先确认设备有 base64，且 wget 支持 --header（AX6 支持）。
#
# 【日志】
#   开机流程日志：/tmp/mini-cloud-mihomo.log（/tmp 是内存盘，重启清空）
#   mini-cloud 日志：/data/mini-cloud/log/mini-cloud.log
#
# 【排查锚点】
#   卡在 "network ready"     → 网络没通，看 net probe 行（code=000 + curl 报错）
#   卡在 "HTTP ready"        → 看 mc: 前缀的 mini-cloud 日志尾部（缺 key / 配置错）
#   卡在 "got mihomo.tar.gz" → 两个云盘都取不到包（看 [云端名] failed 行）
#   卡在 "mihomo OK"         → 看 mihomo 自身日志与 9092 端口
# ==============================================================================

START=99                       # 开机顺序：尽量靠后（等网络/存储就绪）

# ---- mini-cloud 路径与访问地址（按你的实际部署改；<...> 占位符必须替换）----
MC_DIR=/data/mini-cloud
MC_BIN="$MC_DIR/mini-cloud-arm64-upx"
MC_CFG="$MC_DIR/config/config.json"
MC_LOG="$MC_DIR/log/mini-cloud.log"
MC_HOST="<router-ip>"          # 路由器局域网地址（就绪探测与取包都用它）
MC_PORT=8088                   # 必须与 config.json 的 server.listen 端口一致
MC_USER=""                     # 运行时从 config.json 的 server 段读取
MC_PASS=""

# ---- 云盘 / 包位置 ----
# CLOUD_NAME_x 是 config.json 里 clouds[].name（不是驱动类型名！）
CLOUD_NAME_1="<云盘名-1>"      # 主用云盘（= config.json 里 clouds[].name，如 天翼家庭）
CLOUD_NAME_2="<云盘名-2>"      # 备用云盘（= config.json 里 clouds[].name，如 移动云盘）
CLOUD_SUBDIR=backup/AX6         # 加密单元路径（容器名/单元名），必须在加密单元内
MIHOMO_TGZ=mihomo.tar.gz        # 包文件名（tar.gz 内须含 mihomo/ 目录）

# ---- mihomo ----
MIHOMO_DIR=/tmp/mihomo          # 解压目录（/tmp 内存盘）
DATA_DIR=/data/mihomo           # mihomo 数据目录（配置/缓存，需持久化）
MIHOMO_BIN="$MIHOMO_DIR/mihomo-linux-arm64"
MIHOMO_API="http://<router-ip>:9092/version"  # mihomo 外部控制接口（健康检查）
MIHOMO_TOKEN=123456             # ★示例值：必须改成与 mihomo 配置一致的 secret
CLIENT_IP="<client-ip>"        # 要走代理的客户端 IP（按需改；多条规则就复制多段）
MIHOMO_PORT=7892                # 代理端口（重定向目标）

# ---- 等待参数 ----
NET_WAIT_MAX=120                # 网络等待上限：120 × 5 秒 = 10 分钟
NET_PROBE=http://cloud.189.cn   # 探测目标；用 http 不用 https（BusyBox 不一定带 TLS）
CLOCK_FLOOR_EPOCH=1767225600    # 时钟下限 2026-01-01 UTC（只是「明显错误」过滤，见第 1.5 层）
FETCH_TRIES=3                   # 取包轮数（每轮两个云盘都试）
WGET_TIMEOUT=15                 # 单次 wget 超时（秒）
STOP_WAIT=10                    # 等 worker 退出的上限（秒）
SVC_WAIT=5                      # 等 mini-cloud / mihomo 退出的上限（秒）

# ---- 身份标记与内部文件 ----
# 身份标记：优先用 rc.common 的 initscript（真正被调用的脚本路径）；
# 直接 sh /etc/init.d/xxx start 时没有该变量，退回 $0。
# 留空或异常时可手工改为实际路径，例如 SELF=/etc/init.d/alist_init
SELF="${initscript:-$0}"

LOG=/tmp/mini-cloud-mihomo.log
WORK_PID=/tmp/mini-cloud-mihomo-work.pid   # 「运行号 PID 身份标记」
LOCK=/tmp/mini-cloud-mihomo.lock           # mkdir 原子锁（start/stop 互斥）
STOP_FLAG=/tmp/mini-cloud-mihomo.stop      # 停止旗标（worker 在各阶段边界检查）

# ==============================================================================
# 以下为脚本主体
# ==============================================================================

log() { echo "$(date '+%m-%d %H:%M:%S') $*" >> "$LOG"; }

# ------------------------------------------------------------------
# 原子锁：mkdir 成功即持锁。
# 残留锁不自动回收（自动回收在并发下会误删新锁，见文件头约束 5）。
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

preflight() {
    [ -d "$MC_DIR" ]    || { log "错误：目录不存在 $MC_DIR"; return 1; }
    [ -x "$MC_BIN" ]    || { log "错误：二进制不存在或不可执行 $MC_BIN"; return 1; }
    [ -f "$MC_CFG" ]    || { log "错误：配置文件不存在 $MC_CFG"; return 1; }
    mkdir -p "$(dirname "$MC_LOG")" 2>/dev/null
    ( : >> "$MC_LOG" ) 2>/dev/null || { log "错误：日志不可写 $MC_LOG"; return 1; }
    return 0
}

# 从 config.json 读 Basic Auth（限定 server 段：截到 "clouds" 行为止，避免取到 clouds[] 同名字段）
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

# 一轮取包：两个云盘依次试。
# 返回 0 = 成功；1 = 本轮失败；2 = 收到停止请求（调用方据此直接退出，不再继续）
fetch_once() {
    for cloud in "$CLOUD_NAME_1" "$CLOUD_NAME_2"; do
        # 切到下一个云盘之前也检查停止旗标（下载与重试期间都可能被 stop）
        stop_requested && { log "stop requested, abort (fetch/${cloud})"; return 2; }
        _url="http://${MC_USER}:${MC_PASS}@${MC_HOST}:${MC_PORT}/${cloud}/${CLOUD_SUBDIR}/${MIHOMO_TGZ}"
        _safe=$(echo "$_url" | sed 's/:[^:@/]*@/:***@/')     # 日志里密码打码（注意：ps 里仍可见）
        rm -f /tmp/mihomo.tar.gz
        log "fetch ${_safe}"
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

work() {
    preflight || return 1

    # ------------------------------------------------------------------
    # 第 1 层：等网络链路就绪
    # mini-cloud 启动时会登录天翼 / 校验百度 token，网络没通就失败；
    # v1.3.0 的启动隔离只保证服务能起来，失败挂载不会自动重试。
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
    # 子 shell 内 cd（state 相对 CWD）；trap "" HUP 脱离 SSH 会话；exec 保持 PID。
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
    # 不等于「所有云盘都正常」（失败挂载是 /admin 里的红卡，服务仍可用）。
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
        log "mini-cloud HTTP not ready, abort"
        tail -n 5 "$MC_LOG" 2>/dev/null | sed 's/^/  mc: /' >> "$LOG"
        return 1
    fi

    # ------------------------------------------------------------------
    # 第 4 层：取包（双云盘轮试；收到停止请求立即退出，不再重试）
    # ------------------------------------------------------------------
    ok=0
    t=1
    while [ $t -le "$FETCH_TRIES" ]; do
        stop_requested && { log "stop requested, abort (fetch)"; return 0; }
        fetch_once
        _rc=$?
        if [ "$_rc" = "0" ]; then
            ok=1
            break
        fi
        if [ "$_rc" = "2" ]; then
            log "stop requested, abort (fetch)"
            return 0
        fi
        log "fetch attempt ${t}/${FETCH_TRIES} failed"
        sleep 10
        t=$((t+1))
    done
    [ "$ok" != "1" ] && { log "fetch failed, abort"; return 1; }

    # ------------------------------------------------------------------
    # 解压：必须检查 tar 退出码——磁盘满等错误可能让二进制已写入、
    # 但其他文件没解完；失败时清掉不完整目录，绝不启动。
    # ------------------------------------------------------------------
    stop_requested && { log "stop requested, abort (before extract)"; return 0; }
    rm -rf "$MIHOMO_DIR"
    cd /tmp || { log "cd /tmp failed"; return 1; }
    if ! tar -zxvf mihomo.tar.gz >> "$LOG" 2>&1; then
        log "错误：解压失败，清理不完整目录"
        rm -rf "$MIHOMO_DIR"
        rm -f mihomo.tar.gz
        return 1
    fi
    rm -f mihomo.tar.gz
    if [ ! -s "$MIHOMO_BIN" ]; then
        log "错误：解压后 $MIHOMO_BIN 不存在"
        rm -rf "$MIHOMO_DIR"
        return 1
    fi

    # ------------------------------------------------------------------
    # 启动 mihomo + 有上限的健康轮询（不用固定 sleep 单次判断）：
    # 每秒探一次 API，进程提前退出则立即失败，最多等 15 秒。
    # ------------------------------------------------------------------
    stop_requested && { log "stop requested, abort (before mihomo)"; return 0; }
    "$MIHOMO_BIN" -d "$DATA_DIR" >> /dev/null 2>&1 &
    _mp=$!
    _ok=0
    i=1
    while [ $i -le 15 ]; do
        if ! kill -0 "$_mp" 2>/dev/null; then
            log "mihomo 进程已退出（启动失败）"
            break
        fi
        if wget -q -T 2 -O /tmp/mihomo_check \
             --header="Authorization: Bearer ${MIHOMO_TOKEN}" "$MIHOMO_API" 2>/dev/null; then
            rm -f /tmp/mihomo_check
            _ok=1
            break
        fi
        sleep 1
        i=$((i+1))
    done

    if [ "$_ok" != "1" ]; then
        rm -f /tmp/mihomo_check
        log "mihomo not ready, abort"
        # 优先停本次启动的进程（精确），而不是按名字全局杀
        kill "$_mp" 2>/dev/null
        sleep 1
        kill -0 "$_mp" 2>/dev/null && kill -9 "$_mp" 2>/dev/null
        return 1
    fi
    log "mihomo OK"

    # ------------------------------------------------------------------
    # 网络层配置：返回值必须检查，失败不能报成功
    # ------------------------------------------------------------------
    stop_requested && { log "stop requested, abort (before iptables)"; return 0; }

    if echo 1 > /proc/sys/net/ipv4/ip_forward 2>/dev/null; then
        log "ip_forward enabled"
    else
        log "warning: 无法开启 ip_forward（继续尝试加规则）"
    fi

    # 幂等：先循环删同类规则，再加一条。
    # 归属约定：这条规则由本脚本独占管理——清理时按「匹配条件」删除，
    # 因此相同匹配条件的其他规则也会被删除（简化设计的取舍）。
    while iptables -t nat -D PREROUTING -s "$CLIENT_IP" -p tcp \
             -j REDIRECT --to-port "$MIHOMO_PORT" 2>/dev/null; do :; done
    if iptables -t nat -A PREROUTING -s "$CLIENT_IP" -p tcp \
             -j REDIRECT --to-port "$MIHOMO_PORT" 2>>"$LOG"; then
        log "iptables rule added"
        log "===== started ====="
    else
        log "错误：iptables 规则添加失败（见上方输出）"
        return 1
    fi
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

    # 原子锁：保证「检查 + 启动」互斥（普通 [ -f PID ] 只是检查，不是互斥）
    acquire_lock || { log "另一个 start/stop 正在进行，跳过本次 start"; return 1; }

    if worker_alive; then
        log "worker already running (pid $(work_pid)), skip start"
        release_lock
        return 0
    fi

    rm -f "$STOP_FLAG"          # 持锁期间才清旗标：此时不可能有 stop 正在收尾

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

    # 3) 停 mini-cloud 与 mihomo
    # 归属约定：本脚本是该服务的唯一管理者——无论进程由本脚本还是人工启动，
    # stop 都按二进制完整路径停止（init 脚本的标准语义）。
    _p=$(pgrep -f "$MC_BIN"); [ -n "$_p" ] && kill $_p 2>/dev/null
    _p=$(pgrep -f mihomo-linux-arm64); [ -n "$_p" ] && kill $_p 2>/dev/null

    # 3b) 停取包 wget：worker 被 TERM 后，它正在等待的 wget 子进程不会跟着退出
    #     （自身超时 WGET_TIMEOUT 秒）。识别特征必须同时含「wget」与输出文件路径
    #     （仅按路径匹配会误杀碰巧命令行含该路径的其他进程，如 vi）。
    #     只发停止信号 + 有界等待；请不动就明确报错交用户处理
    #     （RV-2026-11-26-02：不做强杀升级、不扩大自动清理范围）。
    _fetch_pat="wget.*mihomo\.tar\.gz"
    _p=$(pgrep -f "$_fetch_pat"); [ -n "$_p" ] && kill $_p 2>/dev/null
    if ! wait_gone "$_fetch_pat" "$WGET_TIMEOUT"; then
        log "错误：取包 wget 可能仍在运行（最多 ${WGET_TIMEOUT} 秒后自行结束），请稍候再 start"
        _confirmed=0
    fi
    rm -f /tmp/mihomo.tar.gz

    # 3c) RV-2026-11-26-01（P2）：任一受管服务未确认退出 = 停止失败，不得报成功
    if ! wait_gone "$MC_BIN" "$SVC_WAIT"; then
        log "错误：mini-cloud ${SVC_WAIT} 秒内仍未退出（停止未确认）"
        _confirmed=0
    fi
    if ! wait_gone mihomo-linux-arm64 "$SVC_WAIT"; then
        log "错误：mihomo ${SVC_WAIT} 秒内仍未退出（停止未确认）"
        _confirmed=0
    fi

    # 4) 清理本脚本负责管理的代理规则（同匹配条件的其他规则也会被删除）。
    # 注意：不恢复 ip_forward（它可能是别的服务开的，恢复成 0 会误伤），
    # 所以 stop 不等于「完整恢复系统网络状态」。
    while iptables -t nat -D PREROUTING -s "$CLIENT_IP" -p tcp \
             -j REDIRECT --to-port "$MIHOMO_PORT" 2>/dev/null; do :; done

    # 5) 只有确认 worker 与全部受管服务都已退出才撤销旗标、删记录；否则保留并返回失败
    if [ "$_confirmed" = "1" ]; then
        rm -f "$WORK_PID"
        rm -f "$STOP_FLAG"
        log "stopped"
    else
        log "错误：停止未完全确认（worker / 服务进程 / 取包 wget 仍有残留），保留停止旗标与 PID 记录，请手工检查"
    fi

    release_lock
    [ "$_confirmed" = "1" ] && return 0
    return 1
}
