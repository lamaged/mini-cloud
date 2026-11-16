# 第四轮复核（RV-2026-11-16-05）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-16-05 |
| 评审类型 | RV-04 处置复核（I7–I10、P11–P14）+ P10 驳回裁决复核 |
| 评审对象 | `feature/file-encryption` @ `41ce36bea6fada7fc19e6f6ef1d8e5d04f68b3ca` |
| 对比基线 | `ee62196`（RV-04 审核对象） |
| 评审方式 | GitHub 逐文件静态阅读（diff + 关键文件全文 + git tree 模式字段）；**未编译、未执行** |
| 结论 | **Go 代码侧通过（闸门③ 可收尾）**；**部署脚本新增 1 条重要（I11）须在闸门⑤ 前修**；另 4 条建议 |

---

## 0. 先更正：RV-04 的 P10 是误报，驳回成立

- RV-04 我写"两个脚本在 git 中为 `100644`"，依据是 diff 文本里的 `new file mode`——但那条属于 `docs/review/` 的 Markdown，**不是脚本**；我没有查 `scripts/` 的模式字段就下了结论。
- 本轮查 `git trees` API（`/git/trees/{sha}?recursive=1`）：`ee62196` 与 `41ce36b` 下两个脚本均为 **`100755`**，与程序员/PM 的驳回依据一致。
- **P10 撤回**，记为评审误报。改进：权限/模式类断言一律查 tree API 或 `ls -l`，不从 diff 片段推断。
- 建议：在归档件 `docs/review/2026-11-16-code-review-r2.md` 的 P10 行下补一行勘误（"RV-05 复核：tree 显示 100755，本条撤回"），避免归档误导后续读者。

---

## 1. RV-04 逐条复核

| 编号 | 意见 | 结论 | 证据 |
|---|---|---|---|
| I7 | `updateMu` 在 `ParseForm` 前获取 | **关闭** | 锁移到 CSRF 校验与 `findMount` 之后、`values`/`ValidateCredential` 之前；`http.Server` 增 `ReadHeaderTimeout: 10s`（超出要求，全站受益） |
| I8 | 回滚失败仍显示"已还原" | **关闭** | `PersistFunc` 的 rollback 改 `func() error`；失败文案"……且配置回滚失败，请手工核对 config.json（.bak 仍保留）"+ `slog.Error`；`TestCommitPlainErrorRollbackFails` 断言"不得出现『配置已还原』" |
| I9 | 脚本未脱离会话 / sleep 3 / 健康检查分不清进程 / 日志丢弃 | **基本关闭** | `start-stop-daemon -b` → `setsid` → 兜底三级；`OLD_PID`/`NEW_PID` 校验（含"NEW==OLD"判定）；健康检查 15×1s 轮询并在新进程死亡时提前失败；日志落 `mini-cloud.log`。**残留问题见 I11/P15** |
| I10 | 验收清单两处与脚本不一致 | **关闭** | §1 改为 `grep v0.10.0 $INSTALL_DIR/mini-cloud.log` + 新增"断开 SSH 后服务仍在"；§5 改为"能执行但启动即退出的假产物"，并注明空文件会被拒；§7 补范围口径 |
| P10 | 脚本模式 644 | **驳回成立，评审撤回** | tree API：`100755`（见 §0） |
| P11 | 兜底分支误报成功 | **关闭** | 提交结果三分支显式化：生效（含告警 200）/未生效+无回滚 500/未生效+回滚（成功 500"已还原"、失败 500"回滚失败"） |
| P12 | 并发测试未断言最终一致 | **关闭** | `TestUpdateSerialized` 断言 `calls` 末项为 `persist:移动云盘:auth-B` 且 `lastCommitVal == auth-B` |
| P13 | prepare 脚本固定 /tmp、未查 upx | **关闭** | `mktemp -d` + `trap` 清理；`[ -x "$UPX_BIN" ]` 预检 |
| P14 | 整机重启不自启的口径 | **关闭** | 验收清单 §7 明确"不含整机重启开机自启"；TODO 技术债记 procd |

---

## 2. 新增重要（I11）

### I11　`rollback_old` 在定义之前被调用，启动失败路径会"既不回滚也不退出"

- **位置**：`scripts/deploy-router.sh`——`NEW_PID` 校验分支里调用 `rollback_old`，而 `rollback_old() { … }` 的定义在**后面**（健康检查段之后）。
- **shell 语义**：POSIX sh 的函数定义是"执行到该行才生效"的语句。调用点先于定义时，`rollback_old` 是未知命令 → 退出码 127 → `set -e` 立即中止脚本（不会再执行其后的 `exit 1`）。
- **后果**：触发条件是"新进程没起来"——`start-stop-daemon` 参数不被接受、二进制不可执行、`setsid` 缺失且 `-b` 行为差异等，都是真机上的常见情况。此时脚本已经 `pkill` 掉旧进程、`mv` 覆盖了旧二进制，然后**直接退出、不回滚、不重启**，服务处于停止状态；而 `.prev` 还在，需要人工介入。
- **要求**：把 `start_daemon()` 与 `rollback_old()` 两个函数定义整体移到脚本前部（`set -e` 之后、第一次使用之前），逻辑不动。这是 2 行位置的调整。
- **附带**：`rollback_old` 内部 `mv` 回 `.prev` 后只 `start_daemon` 就打印"已恢复旧版本"，没有再健康检查——建议同样轮询确认（见 P16）。

---

## 3. 建议（P15–P18）

| 编号 | 内容 | 建议 |
|---|---|---|
| P15 | `start-stop-daemon -b` 是否继承 `>> "$LOG"` 重定向不确定 | BusyBox 的 `-b` 可能把子进程 stdio 重定向到 `/dev/null`，导致 `mini-cloud.log` 为空、验收 §1 的 grep 项再次失效。建议统一走 `setsid sh -c 'exec "$BIN" -config "$CFG" >> "$LOG" 2>&1'`（行为可预期），或在闸门⑤ 先实测日志有内容再勾该项 |
| P16 | 回滚后未验证服务恢复 | `rollback_old` 只启动不检查；建议回滚后跑一次同样的 15×1s 健康轮询，把"已恢复旧版本"改为"已恢复旧版本（HTTP 401/200）" |
| P17 | `mv /tmp/... → /data/...` 跨文件系统非原子 | OpenWrt 的 `/tmp` 是 tmpfs、`/data` 在 flash 上，`mv` 实为 copy+unlink；断电/中断会留下半截二进制。建议 `cp` 到 `$INSTALL_DIR/$BINARY.new` 后同目录 `mv`（原子替换） |
| P18 | 归档件缺勘误 | 见 §0：在 `docs/review/2026-11-16-code-review-r2.md` 的 P10 行下补"RV-05 复核撤回"，保持归档可信 |

---

## 4. Go 代码侧复核（无新增问题）

| 项 | 结论 |
|---|---|
| `updateMu` 覆盖范围 | 正确：仅覆盖 validate→persist→commit；CSRF 与表单解析在锁外 |
| 提交结果分支 | 三分支互斥且都有 `return`，不再存在"失败却 200"的路径 |
| `OnCommitted` 调用时机 | 只在"已生效"（err==nil 或 `ErrStateWriteFailed`）时调用，回滚路径不触发 |
| 回滚与 `cfg` 一致性 | rollback 返回 `error`，`main` 在 `RestoreBackup` 或 `LoadStrict` 失败时如实上抛；`cfg.Clouds` 只在重读成功后更新 |
| `ReadHeaderTimeout` | 10s，配合管理页 10s 上下文合理；未影响长连接 GET（`ReadHeaderTimeout` 只限制头读取） |
| `TestUpdateSerialized` 断言 | 顺序与最终值双重断言，正是 I1 要防的错位 |
| 302 / 转义 / 缓存失效 | 未触碰，无回归 |

---

## 5. 放行意见

| 阶段 | 结论 |
|---|---|
| **闸门③ 收尾** | Go 代码侧通过；建议把 RV-04 的 I7–I10、P11–P14 状态改为"已关闭（RV-05）"，P10 记为"驳回成立 / 评审撤回" |
| **闸门④（测试门禁）** | 可进（代码侧无阻断项） |
| **闸门⑤（真机验收）前** | **I11 必修**（2 行位置调整）；P15/P16 强烈建议（否则"部署成功但日志为空""回滚后服务其实没起来"都会变成验收现场的假结论）；P17/P18 可随手 |
| **闸门⑥（发布）** | 按 process §3 ⑥：README 变更记录 + tag |

修完 I11 后，`scripts/deploy-router.sh` 的失败路径才算闭环：**装不上 → 回滚 → 旧版本重启 → 退出码非 0**，这条链现在是断在第一步的。

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I11 | 重要 | `rollback_old` 定义在使用之后，启动失败路径 `set -e` 直接退出 | | | 待处理 | | |
| P15 | 建议 | `start-stop-daemon -b` 是否继承日志重定向待验证 | | | 待处理 | | |
| P16 | 建议 | 回滚后未做健康检查 | | | 待处理 | | |
| P17 | 建议 | 跨文件系统 `mv` 非原子 | | | 待处理 | | |
| P18 | 建议 | 归档件补 P10 勘误 | | | 待处理 | | |
| P10 | 建议 | （RV-04 提出）脚本模式 644 | 驳回成立 | git tree 显示 100755；评审误报，RV-05 撤回 | 已关闭 | RV-05 | 2026-11-16 |

---

*本报告基于 `41ce36b` 的静态阅读（含 git trees 模式字段核对），未编译、未执行、未接触真实凭证。*
