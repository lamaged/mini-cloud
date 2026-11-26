#!/bin/sh
# test-initd-lifecycle.sh —— 启动脚本模板的生命周期回归（开发机 sh，无需路由器/root）
#
# 覆盖 RV-2026-11-26-01 的回归要求：
#   1) stray_workers 身份过滤：只有「末参 start/boot/restart」的进程才算 worker；
#      stop 自身、其临时子 shell、vi 编辑器等含脚本路径的进程一律不误杀。
#   2) PID 记录损坏（garbage）与缺失（删文件）时执行 stop：不杀 stop 自身、不空转。
#   3) 服务拒绝退出（忽略 TERM）时 stop 返回失败，不误报成功。
#   4) 两个 stop 并发：锁互斥，两者都能在有限时间内完成。
#   5) B 在取包 wget 期间 stop：wget 子进程被杀、残留文件被清。
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
    SVC_WAIT=3      # B 用（缩短等待）；A 的 5 秒为硬编码
}

# 1) stray_workers 身份过滤
case_stray() {
    _path=$1
    . "$_path" || { bad "source $_path"; return; }
    common_env
    sh -c "sleep 20" "$SELF" stop  & d1=$!     # 模拟 stop 自身/临时子 shell（末参 stop）
    sh -c "sleep 20" vi "$SELF"    & d2=$!     # 模拟编辑器（末参不是命令字）
    sh -c "sleep 20" "$SELF" start & w1=$!     # 模拟 worker（末参 start）
    sleep 0.5
    r=$(stray_workers | tr '\n' ' ')
    kill $d1 $d2 $w1 2>/dev/null
    rm -rf "$W"
    if [ "$r" = "$w1 " ]; then
        ok "stray_workers 只命中 worker（$(basename "$_path")）"
    else
        bad "stray_workers=[$r] 期望=[$w1]（$(basename "$_path")）"
    fi
}

# 2) PID 记录损坏/缺失 + 服务拒绝退出 → stop 返回失败、不杀自身
case_stop_resilience() {
    _path=$1; _mode=$2
    . "$_path" || { bad "source $_path"; return; }
    common_env
    sh -c 'trap "" TERM; while :; do sleep 1; done' "$MC_BIN" & svc=$!   # 服务忽略 TERM（命令行保留 MC_BIN 特征）
    sleep 0.5
    if [ "$_mode" = "garbage" ]; then echo "garbage" > "$WORK_PID"; else rm -f "$WORK_PID"; fi
    stop; rc=$?
    alive=no; kill -0 $svc 2>/dev/null && alive=yes
    kill -9 $svc 2>/dev/null
    rm -rf "$W"
    if [ "$rc" = "1" ] && [ "$alive" = "yes" ]; then
        ok "stop 对未退出服务返回失败（$_mode，$(basename "$_path")）"
    else
        bad "stop rc=$rc 服务存活=$alive（$_mode，$(basename "$_path")）"
    fi
}

# 3) 以「含脚本路径 + 末参 stop」的调用形态（模拟 rc.common）执行 stop：
#    不杀调用者自身、不因临时子 shell 空转（新实现应快速返回 0）
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
        rm -rf "$W"
        echo "rc=$rc elapsed=$((t1-t0))"
    ' _ "$_path" stop)
    case "$out" in
        "rc=0 elapsed="*)
            _e=${out#rc=0 elapsed=}
            if [ "$_e" -lt 8 ]; then
                ok "损坏 PID 记录下 stop 快速成功且未自杀（$(basename "$_path")，${_e}s）"
            else
                bad "stop 空转 ${_e}s（$(basename "$_path")）"
            fi ;;
        *)
            bad "stop 自身被误杀或失败：[$out]（$(basename "$_path")）" ;;
    esac
}

# 4) 两个 stop 并发：锁互斥，均能完成
case_concurrent_stop() {
    _path=$1
    . "$_path" || { bad "source $_path"; return; }
    common_env
    t0=$(date +%s)
    ( stop; echo $? > "$W/rc1" ) &
    ( stop; echo $? > "$W/rc2" ) &
    wait
    t1=$(date +%s)
    r1=$(cat "$W/rc1" 2>/dev/null); r2=$(cat "$W/rc2" 2>/dev/null)
    rm -rf "$W"
    if [ "$r1" = "0" ] && [ "$r2" = "0" ]; then
        ok "两个 stop 并发均成功（$(basename "$_path")，$((t1-t0))s）"
    else
        bad "并发 stop rc1=$r1 rc2=$r2（$(basename "$_path")）"
    fi
}

# 5) B：取包 wget 期间 stop → wget 被杀、残留文件被清
case_wget_cleanup() {
    _path=$1
    . "$_path" || { bad "source $_path"; return; }
    common_env
    WGET_TIMEOUT=3
    sh -c 'trap "" TERM; sleep 20' /tmp/mihomo.tar.gz & wp=$!   # 模拟不随 worker 退出的 wget
    : > /tmp/mihomo.tar.gz 2>/dev/null
    sleep 0.5
    echo garbage > "$WORK_PID"
    stop; rc=$?
    alive=no; kill -0 $wp 2>/dev/null && alive=yes; kill -9 $wp 2>/dev/null
    exists=no; [ -e /tmp/mihomo.tar.gz ] && exists=yes
    rm -f /tmp/mihomo.tar.gz; rm -rf "$W"
    if [ "$rc" = "0" ] && [ "$alive" = "no" ] && [ "$exists" = "no" ]; then
        ok "stop 清理取包 wget 与残留文件（$(basename "$_path")）"
    else
        bad "wget 清理 rc=$rc wget存活=$alive 残留文件=$exists（$(basename "$_path")）"
    fi
}

for s in "$A" "$B"; do
    ( case_stray "$s" )
    ( case_stop_resilience "$s" garbage )
    ( case_stop_resilience "$s" missing )
    ( case_selfsafe "$s" )
    ( case_concurrent_stop "$s" )
done
( case_wget_cleanup "$B" )

echo
if [ -s "$FAILFILE" ]; then
    echo "结果：存在失败项"
    rm -f "$FAILFILE"
    exit 1
fi
rm -f "$FAILFILE"
echo "结果：全部通过"
exit 0
