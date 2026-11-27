#!/bin/sh
# test-initd-lifecycle.sh —— 启动脚本模板的生命周期回归（开发机 sh，无需路由器/root）
#
# 对应 v3 定位（PM 拍板 2026-11-27）：按顺序执行 + 每步如实报告 + 失败给原因建议。
# 覆盖用例：
#   1) stop 输出格式：cli 模式每步 [i/N] 标题........结果 上屏幕，且同样进日志
#   2) worker 记录缺失/损坏：跳过并提示，不杀无关进程，服务正常停止 → stop 成功
#   3) 服务拒绝退出（忽略 TERM）：该步失败 + stop 返回失败，不误报成功
#   4) 取包进程（B）：响应 TERM → 停止并清残留；忽略 TERM → 失败提示，不强杀
#   5) 两个 stop 并发：锁互斥，均能完成
#
# 用法：sh scripts/test-initd-lifecycle.sh
# 说明：只在开发机跑；开机 boot 模式与手工前台模式的真机行为需单独验收。
set -u
DIR=$(cd "$(dirname "$0")" && pwd)
A="$DIR/initd-mini-cloud.sh"
B="$DIR/initd-mini-cloud-mihello.sh"

FAILFILE=$(mktemp)
export FAILFILE
# 注意：测试助手不用 ok/bad 命名——模板脚本自带同名函数，sourcing 会覆盖
pass_case() { echo "PASS: $*"; }
fail_case() { echo "FAIL: $*"; echo 1 > "$FAILFILE"; }

common_env() {
    W=$(mktemp -d)
    MC_DIR="$W"; MC_BIN="$W/fake-mc"; MC_CFG="$W/c.json"; MC_LOG="$W/mc.log"
    LOG="$W/boot.log"; WORK_PID="$W/work.pid"; LOCK="$W/lock"; STOP_FLAG="$W/stop"
    SELF=/etc/init.d/t-mc
    WGET_TIMEOUT=3
}

# 1) stop 输出格式：屏幕 + 日志双写，含 [i/N] 步骤与总结行
case_output_format() {
    _path=$1; _steps=$2
    . "$_path" || { fail_case "source $_path"; return; }
    common_env
    out=$( ( stop ) 2>&1 )
    rc=$?
    in_out=yes; in_log=yes
    echo "$out" | grep -q "\[1/$_steps\]" || in_out=no
    grep -q "\[1/$_steps\]" "$LOG" 2>/dev/null || in_log=no
    rm -rf "$W"
    if [ "$rc" = "0" ] && [ "$in_out" = "yes" ] && [ "$in_log" = "yes" ]; then
        pass_case "stop 步骤输出双写（屏幕+日志，$(basename "$_path")）"
    else
        fail_case "stop 输出 rc=$rc 屏幕=$in_out 日志=$in_log（$(basename "$_path")）"
    fi
}

# 2) worker 记录缺失/损坏：跳过并提示，不杀无关进程，服务正常停止
case_worker_record_skip() {
    _path=$1; _mode=$2      # garbage | missing
    . "$_path" || { fail_case "source $_path"; return; }
    common_env
    sh -c 'sleep 20' "$SELF" start & decoy=$!   # 形似 worker 的诱饵——记录无效时不得被杀
    sleep 0.5
    if [ "$_mode" = "garbage" ]; then echo "garbage" > "$WORK_PID"; else rm -f "$WORK_PID"; fi
    stop; rc=$?
    alive=no; kill -0 $decoy 2>/dev/null && alive=yes
    kill -9 $decoy 2>/dev/null
    noted=no; grep -q "无正在运行的 worker" "$LOG" 2>/dev/null && noted=yes
    rm -rf "$W"
    if [ "$rc" = "0" ] && [ "$alive" = "yes" ] && [ "$noted" = "yes" ]; then
        pass_case "记录$_mode：跳过提示、不杀诱饵、正常停服（$(basename "$_path")）"
    else
        fail_case "记录$_mode rc=$rc 诱饵存活=$alive 提示=$noted（$(basename "$_path")）"
    fi
}

# 3) 服务拒绝退出：该步失败 + stop 返回失败
case_service_refuse() {
    _path=$1
    . "$_path" || { fail_case "source $_path"; return; }
    common_env
    sh -c 'trap "" TERM; while :; do sleep 1; done' "$MC_BIN" & svc=$!
    sleep 0.5
    stop; rc=$?
    alive=no; kill -0 $svc 2>/dev/null && alive=yes
    kill -9 $svc 2>/dev/null
    failed_line=no; grep -q "未确认退出" "$LOG" 2>/dev/null && failed_line=yes
    flag_kept=no; [ -f "$STOP_FLAG" ] && flag_kept=yes
    rm -rf "$W"
    if [ "$rc" = "1" ] && [ "$alive" = "yes" ] && [ "$failed_line" = "yes" ] && [ "$flag_kept" = "yes" ]; then
        pass_case "服务未退出 → 该步失败 + stop 失败 + 保留停止旗标（$(basename "$_path")）"
    else
        fail_case "rc=$rc 存活=$alive 失败行=$failed_line 旗标=$flag_kept（$(basename "$_path")）"
    fi
}

# 4a) B 取包进程响应 TERM → 停止并清残留
case_wget_term() {
    _path=$1
    . "$_path" || { fail_case "source $_path"; return; }
    common_env
    sh -c 'sleep 20' wget -q -O /tmp/mihello.tar.gz http://x & wp=$!
    : > /tmp/mihello.tar.gz 2>/dev/null
    sleep 0.5
    stop; rc=$?
    alive=no; kill -0 $wp 2>/dev/null && alive=yes; kill -9 $wp 2>/dev/null
    exists=no; [ -e /tmp/mihello.tar.gz ] && exists=yes
    rm -f /tmp/mihello.tar.gz; rm -rf "$W"
    if [ "$rc" = "0" ] && [ "$alive" = "no" ] && [ "$exists" = "no" ]; then
        pass_case "取包进程响应 TERM → 停止并清理（$(basename "$_path")）"
    else
        fail_case "rc=$rc 存活=$alive 残留=$exists（$(basename "$_path")）"
    fi
}

# 4b) B 取包进程忽略 TERM → 失败提示，不强杀
case_wget_stubborn() {
    _path=$1
    . "$_path" || { fail_case "source $_path"; return; }
    common_env
    sh -c 'trap "" TERM; while :; do sleep 1; done' wget -q -O /tmp/mihello.tar.gz http://x & wp=$!
    sleep 0.5
    stop; rc=$?
    alive=no; kill -0 $wp 2>/dev/null && alive=yes
    kill -9 $wp 2>/dev/null
    warned=no; grep -q "取包 wget 可能仍在运行" "$LOG" 2>/dev/null && warned=yes
    rm -f /tmp/mihello.tar.gz; rm -rf "$W"
    if [ "$rc" = "1" ] && [ "$alive" = "yes" ] && [ "$warned" = "yes" ]; then
        pass_case "取包进程忽略 TERM → 失败提示、不强杀（$(basename "$_path")）"
    else
        fail_case "rc=$rc 存活=$alive 提示=$warned（$(basename "$_path")）"
    fi
}

# 5) 两个 stop 并发：锁互斥，均能完成
case_concurrent_stop() {
    _path=$1
    . "$_path" || { fail_case "source $_path"; return; }
    common_env
    t0=$(date +%s)
    ( stop; echo $? > "$W/rc1" ) &
    ( stop; echo $? > "$W/rc2" ) &
    wait
    t1=$(date +%s)
    r1=$(cat "$W/rc1" 2>/dev/null); r2=$(cat "$W/rc2" 2>/dev/null)
    rm -rf "$W"
    if [ $((t1-t0)) -lt 15 ]; then
        pass_case "两个 stop 并发均完成（rc=$r1/$r2，$(basename "$_path")，$((t1-t0))s）"
    else
        fail_case "并发 stop 耗时 $((t1-t0))s（$(basename "$_path")）"
    fi
}

( case_output_format "$A" 3 )
( case_output_format "$B" 5 )
for s in "$A" "$B"; do
    ( case_worker_record_skip "$s" garbage )
    ( case_worker_record_skip "$s" missing )
    ( case_service_refuse "$s" )
    ( case_concurrent_stop "$s" )
done
# 6) P0 回归（RV-2026-11-27-01）：boot() 显式转发 start boot → 后台模式；
#    手工 start（无参）→ 前台 cli 模式
case_mode_dispatch() {
    _path=$1
    . "$_path" || { fail_case "source $_path"; return; }
    common_env
    work() { echo "MODE=$MC_MODE"; }   # 覆盖真实流程，只测模式分发
    boot
    sleep 0.5
    boot_hit=$(grep -c "MODE=boot" "$LOG" 2>/dev/null)
    cli_mode=$( start )
    rm -rf "$W"
    if [ "$boot_hit" -ge 1 ] && [ "$cli_mode" = "MODE=cli" ]; then
        pass_case "boot→后台(boot) / start→前台(cli)（$(basename "$_path")）"
    else
        fail_case "模式分发 boot 命中=$boot_hit cli=[$cli_mode]（$(basename "$_path")）"
    fi
}

( case_wget_term "$B" )
( case_wget_stubborn "$B" )
for s in "$A" "$B"; do
    ( case_mode_dispatch "$s" )
done

echo
if [ -s "$FAILFILE" ]; then
    echo "结果：存在失败项"
    rm -f "$FAILFILE"
    exit 1
fi
rm -f "$FAILFILE"
echo "结果：全部通过"
exit 0

