#!/bin/sh
# test-initd-lifecycle.sh —— 启动脚本模板的生命周期回归（开发机 sh，无需路由器/root）
#
# 对应脚本定位（RV-2026-11-26-02）：正常路径可靠 + 异常明确告警、不误操作。
# 覆盖用例：
#   1) 未知 PID 状态（记录损坏/缺失）：不扫描、不杀无关进程，告警 + stop 返回失败
#   2) 服务拒绝退出（忽略 TERM）：stop 返回失败，不误报成功
#   3) 以「含脚本路径 + 末参 stop」的调用形态（模拟 rc.common）执行 stop：不杀自身、不空转
#   4) 两个 stop 并发：锁互斥，均能完成，且至少一个正常成功结束
#   5) B 取包进程：正常响应 TERM → 清理成功；忽略 TERM → 告警 + 返回失败（不强杀）
#
# 用法：sh scripts/test-initd-lifecycle.sh
# 说明：只在开发机跑；真机验证仍需单独执行（本脚本不覆盖开机流程/网络等待）。
set -u
DIR=$(cd "$(dirname "$0")" && pwd)
A="$DIR/initd-mini-cloud.sh"
B="$DIR/initd-mini-cloud-mihomo.sh"

FAILFILE=$(mktemp)
export FAILFILE
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; echo 1 > "$FAILFILE"; }

# 公共覆盖：独立临时环境（source 模板后调用）
common_env() {
    W=$(mktemp -d)
    MC_DIR="$W"; MC_BIN="$W/fake-mc"; MC_CFG="$W/c.json"; MC_LOG="$W/mc.log"
    LOG="$W/boot.log"; WORK_PID="$W/work.pid"; LOCK="$W/lock"; STOP_FLAG="$W/stop"
    SELF=/etc/init.d/t-mc
    SVC_WAIT=3      # B 用（缩短等待）；A 的服务等待为硬编码 5 秒
}

# 1) 未知 PID 状态：不扫描、不杀无关进程，告警 + 返回失败
case_stop_unknown() {
    _path=$1; _mode=$2      # garbage | missing
    . "$_path" || { bad "source $_path"; return; }
    common_env
    # 诱饵：形态与 worker 一致（含 SELF、末参 start）——但记录已损坏，脚本不得按它猜杀
    sh -c 'sleep 20' "$SELF" start & decoy=$!
    sleep 0.5
    if [ "$_mode" = "garbage" ]; then echo "garbage" > "$WORK_PID"; else rm -f "$WORK_PID"; fi
    t0=$(date +%s)
    stop; rc=$?
    t1=$(date +%s)
    alive=no; kill -0 $decoy 2>/dev/null && alive=yes
    kill -9 $decoy 2>/dev/null
    warned=no; grep -q "无法确认" "$LOG" 2>/dev/null && warned=yes
    rm -rf "$W"
    if [ "$rc" = "1" ] && [ "$alive" = "yes" ] && [ "$warned" = "yes" ] && [ $((t1-t0)) -lt 8 ]; then
        ok "未知 PID 状态：告警 + 返回失败、未杀无关进程（$_mode，$(basename "$_path")）"
    else
        bad "未知 PID 状态 rc=$rc 诱饵存活=$alive 告警=$warned 耗时=$((t1-t0))s（$_mode，$(basename "$_path")）"
    fi
}

# 2) 服务拒绝退出：stop 返回失败
case_stop_resilience() {
    _path=$1
    . "$_path" || { bad "source $_path"; return; }
    common_env
    sh -c 'trap "" TERM; while :; do sleep 1; done' "$MC_BIN" & svc=$!
    sleep 0.5
    echo "garbage" > "$WORK_PID"       # 记录不可验证 → 走「未知状态」分支，不影响服务停止确认
    stop; rc=$?
    alive=no; kill -0 $svc 2>/dev/null && alive=yes
    kill -9 $svc 2>/dev/null
    rm -rf "$W"
    if [ "$rc" = "1" ] && [ "$alive" = "yes" ]; then
        ok "服务未退出时 stop 返回失败（$(basename "$_path")）"
    else
        bad "stop rc=$rc 服务存活=$alive（$(basename "$_path")）"
    fi
}

# 3) rc.common 形态调用 + 损坏 PID：不杀自身、快速返回（失败而非空转）
case_selfsafe() {
    _path=$1
    out=$(sh -c '
        . "$1" || exit 9
        W=$(mktemp -d)
        MC_DIR="$W"; MC_BIN="$W/fake-mc"; MC_CFG="$W/c.json"; MC_LOG="$W/mc.log"
        LOG="$W/boot.log"; WORK_PID="$W/work.pid"; LOCK="$W/lock"; STOP_FLAG="$W/stop"
        SELF=/etc/init.d/t-mc
        SVC_WAIT=3
        echo garbage > "$WORK_PID"
        t0=$(date +%s)
        stop; rc=$?
        t1=$(date +%s)
        warned=no; grep -q "无法确认" "$LOG" 2>/dev/null && warned=yes
        rm -rf "$W"
        echo "rc=$rc elapsed=$((t1-t0)) warned=$warned"
    ' _ "$_path" stop)
    case "$out" in
        "rc=1 elapsed="*)
            _e=$(echo "$out" | sed 's/.*elapsed=\([0-9]*\).*/\1/')
            _w=$(echo "$out" | sed 's/.*warned=\([a-z]*\).*/\1/')
            if [ "$_e" -lt 8 ] && [ "$_w" = "yes" ]; then
                ok "rc.common 形态下 stop 快速失败且未自杀（$(basename "$_path")，${_e}s）"
            else
                bad "stop 空转或无告警：[$out]（$(basename "$_path")）"
            fi ;;
        *)
            bad "stop 自身被误杀或结果异常：[$out]（$(basename "$_path")）" ;;
    esac
}

# 4) 两个 stop 并发：锁互斥，均能完成，且至少一个成功
case_concurrent_stop() {
    _path=$1
    . "$_path" || { bad "source $_path"; return; }
    common_env
    sh -c 'sleep 20' "$SELF" start & w=$!      # 可识别的假 worker（有记录、响应 TERM）
    sleep 0.5
    echo "run $w $SELF" > "$WORK_PID"
    t0=$(date +%s)
    ( stop; echo $? > "$W/rc1" ) &
    ( stop; echo $? > "$W/rc2" ) &
    wait
    t1=$(date +%s)
    r1=$(cat "$W/rc1" 2>/dev/null); r2=$(cat "$W/rc2" 2>/dev/null)
    kill -9 $w 2>/dev/null
    rm -rf "$W"
    if { [ "$r1" = "0" ] || [ "$r2" = "0" ]; } && [ $((t1-t0)) -lt 15 ]; then
        ok "两个 stop 并发均完成且至少一个成功（$(basename "$_path")，$((t1-t0))s）"
    else
        bad "并发 stop rc1=$r1 rc2=$r2 耗时=$((t1-t0))s（$(basename "$_path")）"
    fi
}

# 5a) B：取包进程正常响应 TERM → 清理成功、残留文件被清
case_wget_term() {
    _path=$1
    . "$_path" || { bad "source $_path"; return; }
    common_env
    WGET_TIMEOUT=3
    sh -c 'sleep 20' "$SELF" start & w=$!        # 有效 worker 记录（可识别、响应 TERM）
    sleep 0.3
    echo "run $w $SELF" > "$WORK_PID"
    sh -c 'sleep 20' wget -q -O /tmp/mihomo.tar.gz http://x & wp=$!  # 取包进程（响应 TERM）
    : > /tmp/mihomo.tar.gz 2>/dev/null
    sleep 0.5
    stop; rc=$?
    kill -9 $w 2>/dev/null
    alive=no; kill -0 $wp 2>/dev/null && alive=yes; kill -9 $wp 2>/dev/null
    exists=no; [ -e /tmp/mihomo.tar.gz ] && exists=yes
    rm -f /tmp/mihomo.tar.gz; rm -rf "$W"
    if [ "$rc" = "0" ] && [ "$alive" = "no" ] && [ "$exists" = "no" ]; then
        ok "取包进程响应 TERM → 停止并清理（$(basename "$_path")）"
    else
        bad "取包清理 rc=$rc 存活=$alive 残留=$exists（$(basename "$_path")）"
    fi
}

# 5b) B：取包进程忽略 TERM → 不强杀，告警 + 返回失败
case_wget_stubborn() {
    _path=$1
    . "$_path" || { bad "source $_path"; return; }
    common_env
    WGET_TIMEOUT=3
    sh -c 'sleep 20' "$SELF" start & w=$!        # 有效 worker 记录（可识别、响应 TERM）
    sleep 0.3
    echo "run $w $SELF" > "$WORK_PID"
    sh -c 'trap "" TERM; while :; do sleep 1; done' wget -q -O /tmp/mihomo.tar.gz http://x & wp=$!
    sleep 0.5
    stop; rc=$?
    kill -9 $w 2>/dev/null
    alive=no; kill -0 $wp 2>/dev/null && alive=yes
    kill -9 $wp 2>/dev/null
    warned=no; grep -q "取包 wget 可能仍在运行" "$LOG" 2>/dev/null && warned=yes
    rm -f /tmp/mihomo.tar.gz; rm -rf "$W"
    if [ "$rc" = "1" ] && [ "$alive" = "yes" ] && [ "$warned" = "yes" ]; then
        ok "取包进程忽略 TERM → 告警 + 返回失败、不强杀（$(basename "$_path")）"
    else
        bad "顽固取包 rc=$rc 存活=$alive 告警=$warned（$(basename "$_path")）"
    fi
}

for s in "$A" "$B"; do
    ( case_stop_unknown "$s" garbage )
    ( case_stop_unknown "$s" missing )
    ( case_stop_resilience "$s" )
    ( case_selfsafe "$s" )
    ( case_concurrent_stop "$s" )
done
( case_wget_term "$B" )
( case_wget_stubborn "$B" )

echo
if [ -s "$FAILFILE" ]; then
    echo "结果：存在失败项"
    rm -f "$FAILFILE"
    exit 1
fi
rm -f "$FAILFILE"
echo "结果：全部通过"
exit 0
