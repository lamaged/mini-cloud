# v1.3.0 批 2 复审意见核实（RV-2026-11-25-04）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-25-04（对另一审核员"三项 P1"的独立核实 + 合并裁决） |
| 评审对象 | `feature/file-encryption` @ `44a9b4db1a3898a06a2de3dc9cf17b8744601a13`（RV-25-02 五项处置后的复审） |
| 核实方式 | 对照 `drivers/baidu/driver.go`（refreshSingleFlight/doRefresh/inFlightResult/CommitCredential）与 `auth.go`（writeFileAtomic/SaveToken）逐条推演；**未编译、未执行** |
| 结论 | **三条 P1 全部成立；同意闸门③ 暂不通过，修复后进闸门⑤**。其中 P1-C 的"目录 fsync 必须报错"半条偏严，建议口径统一而非阻断 |

---

## 1. 逐条核实

### P1-A　刷新结果读取竞态 + 旧失败残留 ✅ 成立（我方上一轮已报问题 2，问题 1 漏报）

- **问题 1（等待者读全局而非本轮结果）**：等待者 `case <-ch: return d.inFlightResult()` —— `inFlightResult()` 重新锁 `refreshMu` 并查 `d.refreshInFlight`。窗口：本轮 leader `close(done)` 后、等待者尚未取得锁时，新调用者可已启动下一轮（`refreshInFlight` 指向新轮）→ 等待者返回「刷新仍在进行」，**丢掉了自己那轮已完成的结果**。窄窗口但真实。
- **问题 2（成功后不清 `lastRefreshErr`）**：leader 收尾只有 `if err != nil { lastRefreshErr = err }`，无 else 清零（旧代码有 `= nil`，重构丢失）。一次瞬时失败后该值**永不消失**：恢复后的成功刷新，等待者与 60s 内新调用都被陈旧错误误伤（限频本只该对失败生效），且每次 watcher tick 白打一次上游。
- **修法**：等待者捕获本轮 `rf := d.refreshInFlight`，`<-rf.done` 后**直接读 `rf.err`**（`rf.err` 在 close 前、锁内写入，happens-before 成立），不再查全局；leader 成功分支补 `lastRefreshErr = nil`。
- **测试**：失败→成功→并发等待者全得 nil 且 60s 内不被误限频；上一轮结束、下一轮开始后，旧等待者仍取本轮结果。

### P1-B　旧刷新可覆盖新授权 ✅ 成立（我方漏报，确为 C06 要防的竞态）

- `doRefresh` 全程**不持 `refreshMu`**；其"代次复检 → `setRefreshToken` → `SetAccessToken`"不是一个临界区。而 `CommitCredential` 恰好以 `refreshMu` 保护内存提交。交错：旧刷新复检通过（代次 3==3）→ 用户提交（代次→4、内存换新票）→ 旧刷新把旧票写回内存 → **运行内存与 config 不一致**（config 是新授权，内存是旧票）。重启前一直用旧授权。
- **修法**：把"代次复检 + 内存提交"段包进 `refreshMu`（网络兑换与 `rotatePersist` 仍在锁外，不破坏结果共享）；锁序 `refreshMu → d.mu` 与 CommitCredential 一致，`cfgMu` 仅在锁外的 persist 段出现，无环。
- **测试**：确定性交错——阻塞旧刷新在兑换完成后、内存提交前，插入新凭据提交，断言 config/内存/state 代次全部属于新授权。

### P1-C　state 固定临时文件名 + 目录 fsync 被忽略 ⚠️ 前半成立、后半偏严

- **前半（固定 `.tmp`）✅ 成立**：`tmp := path + ".tmp"` 固定名；`doRefresh`（锁外）与 `CommitCredential`（解锁后）都可能并发 `SaveToken` → 双写同名 tmp → 一方 rename 成功、另一方 ENOENT；`CommitCredential` 失败路径还 `os.Remove(正式文件)`——**可能误删对方刚写好的新缓存**。修法：`os.CreateTemp(stateDir, base+".tmp-*")` 唯一临时名（chmod 0600 + write + sync + close + rename，失败清理）；或加专用写锁。
- **后半（目录 fsync 失败必须按失败返回）⚠️ 偏严**：与 `conf.writeFileAtomic` 既有口径一致（syncDir 失败仅 WARN）；且对"可丢弃缓存"而言，"不会出半截文件"已由**文件 fsync + rename** 保证——目录 fsync 只影响 rename 的断电持久性，最坏回退到**旧的完整缓存**，不产生半截。建议：临时名改唯一（必做）；目录 fsync 保持 WARN 并加注释说明回退语义，与 conf 口径统一，**不阻断**。

---

## 2. 我方自省

- RV-25-03 我方只报了 P1-A 问题 2（一行修法）；**问题 1 与 P1-B 确系漏报**——外部审核对同一次重构的并发推演更完整。问题 2 的"一行修法"本身仍必要，但不充分。
- 双方一致确认无误的部分：P1-2（state 纯缓存/代次匹配/原子写主体）、Link 脱敏、StorageFailed 转红、重启文案中性、上一轮 I2 撤回。

## 3. 合并裁决

| 项 | 定级 | 状态 |
|---|---|---|
| P1-A 问题 1（等待者读本轮结果） | P1 | 待修 |
| P1-A 问题 2（成功清 lastRefreshErr） | P1 | 待修 |
| P1-B（代次复检+内存提交原子化） | P1 | 待修 |
| P1-C 前半（唯一临时文件名） | P1 | 待修 |
| P1-C 后半（目录 fsync 报错） | 口径 | 建议 WARN + 注释（与 conf 一致），不阻断 |

**结论**：同意闸门③ 暂不通过；上述四项代码层闭合（含外部审核要求的 4 组并发/交错测试）后，批 2 可进 AX6 闸门⑤。不部署当前版本到真机。

---

## 4. 处置表

| 编号 | 级别 | 意见摘要 | 来源 | 状态 |
|---|---|---|---|---|
| P1-A | P1 | 等待者读本轮 rf.err；成功后清 lastRefreshErr | 外部 | 待处理 |
| P1-B | P1 | 代次复检与内存提交纳入 refreshMu 临界区 | 外部 | 待处理 |
| P1-C | P1 | SaveToken 唯一临时文件名（并发安全） | 外部 | 待处理 |
| P1-C' | 口径 | 目录 fsync 失败：WARN + 回退语义注释（与 conf 统一） | 我方校准 | 待定 |

---

*本报告基于 `44a9b4d` 静态推演，未编译、未执行。*
