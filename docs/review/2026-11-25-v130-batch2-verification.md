# v1.3.0 批 2 外部意见核实与合并裁决（RV-2026-11-25-02）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-25-02（承 RV-2026-11-25-01；对另一审核员 4 条意见的独立核实 + 合并裁决） |
| 评审对象 | `feature/file-encryption` @ `6e8bd3d9d313e3a5a12f708a6870cf44e1cdb710`（v1.3.0 **批 2**：13ae500 + 6e8bd3d） |
| 核实方式 | 对照 `docs/design-v1.3.0.md` 原文（§6.4–§6.10）逐条核对 `drivers/baidu/*`、`internal/conf` 实现；**未编译、未执行** |
| 结论 | **同意 P1-2 为阻断项（闸门⑤ 前必闭）；P1-1 降为 P2 增强；P2-1/P2-2 收口；另补我方的 I1** |

---

## 1. 逐条核实

| 编号 | 外部意见 | 设计原文依据 | 代码事实 | 我的定级 |
|---|---|---|---|---|
| P1-2 | state 仍存 refresh_token + SaveToken 非原子 | §6.5「state 降级为**纯缓存（access_token/expires_at）**」；§6.6.3「**原子写**：temp+rename、文件 0600、目录 0700（现 SaveToken 为 MkdirAll 0755 + os.WriteFile 非原子，实现时重构）」；§6.7「批 2 实现层重构：先原子落盘（先 config 后 state）再提交内存」 | `TokenState{RefreshToken, AccessToken, ExpiresAt}`；`refreshSingleFlight`/`CommitCredential` 仍写 RefreshToken；`Init` 用 `token.RefreshToken == d.getRefreshToken()` 做缓存匹配；`SaveToken` 仍 `os.WriteFile`；注释自认「批 2 将改为原子写」——**这批就是批 2，属应做未做** | **P1 阻断** |
| P1-1 | 互斥串行 ≠ 真 SingleFlight | §6.6.1 原文「同挂载刷新互斥（**单飞**）：401 回调与后台 watcher 的并发刷新进入**同一把锁**」 | `refreshMu` 全程持锁 + 失败 60s 限频——**与设计的字面定义一致**；但 N 个并发 401 会做 N 次兑换 + N 次轮换写回（正确但浪费） | **降为 P2**（建议收口，非契约违背） |
| P2-1 | 落盘失败分类为黄 | §6.10 表「refresh_token 不可恢复失效 / **落盘失败** → **红**（需要重新授权 / **存储异常**）」 | `recordRefreshFailure("轮换写回配置失败…")` 不命中 `classifyBaiduRefreshErr` 关键词 → `authDead=false` → 黄「当前会话暂可用」 | **P2 收口**（与设计不符，一行可修） |
| P2-2 | 重启提示路径 | §6.12「兜底：`/etc/init.d/mini-cloud stop/start`」 | 设计假设 README 部署；实际设备由 `alist_init` 统一管理（README 小抄明确不装） | **P2 收口**（口径问题） |

## 2. 我方补充（上一轮已报，并入本批收口）

### I1（重要）　百度 `Link` 链路 access_token 泄露残留

`drivers/baidu/driver.go` `Link()`：`dlink = ... + "&access_token=" + getAccessToken()` 后直接 `httpClient.Do`，网络失败时 `url.Error` 携带完整 dlink → webdav `slog.Error("WebDAV error", …, "err", err)` 进日志。RV-24-06 已修探测/API/端点三处，此处是同类模式的第四处、未被覆盖。

## 3. 撤回上一轮的我方误判（诚实记录）

RV-2026-11-25-01 的 **I2（口径矛盾）撤回**：当时按"批 1"评审，误以为一键授权/重启按钮属提前发布。**事实：13ae500 + 6e8bd3d 即批 2 交付**（设计 §6.4/§6.12），与 TODO「顺延至批 2 一键授权落地」一致。仅需同步 PROGRESS 中"待：重启按钮设计决策"等滞后条目。

## 4. 修法建议

### P1-2（阻断，两项一体）

1. **state 纯缓存化**：`TokenState` 删 `RefreshToken`，改为 `{AccessToken, ExpiresAt, CredentialGeneration}`——用**现有代次字段**做缓存匹配键：`state.Gen == config.CredentialGeneration` 才可复用缓存（用户提交代次 +1 → 自动失配 → 重新兑换，天然满足 D2「新授权重启即生效」，无需再存 refresh_token）。`NeedRefresh` 同步改用 `ExpiresAt`（现状读 state 已可用）。
2. **原子写**：`SaveToken` 复用 `conf.writeFileAtomic` 同款（temp + chmod 0600 + fsync + rename + 父目录 fsync），目录 0700 保持。
3. 测试：缓存损坏/写失败/断电恢复/旧格式（含 refresh_token 字段）迁移——旧 state 读到多余字段按 JSON 忽略即可，但 `Gen=0` 与旧配置 `CredentialGeneration=0` 匹配时的语义要明确（旧 state 视为过期缓存、重新兑换）。

### P1-1（降 P2，采纳增强）

在 `refreshMu` 之外加"进行中结果缓存"：首个进入者执行兑换并把结果（access/newRT/err）存入 `inFlight`，后续等待者在锁外等待该结果、复用后返回；兑换完成清空。补测试「N 并发刷新 → token 端点仅 1 次请求」。

### P2-1（一行）

`recordRefreshFailure` 增加"存储异常"分类：轮换写回失败时置 `authDead=false` + `warnText="本地配置写入失败（存储异常）"`，或给 `DriverHealth` 加独立事实位；合成规则按 §6.10 落红（或至少与"暂败黄"区分）。

### P2-2（口径）

`restartTmpl` 与设计 §6.12 的兜底命令改为中性文案（如"重启路由器上的 mini-cloud 进程（本机为 `/etc/init.d/alist_init restart`）"），README/设计稿同步。

### I1（一行）

`Link` 错误返回前 `redactSecrets(err.Error(), d.client.getAccessToken())`，补"CDN 失败日志不含 token"断言。

## 5. 合并裁决

| 项 | 定级 | 要求 |
|---|---|---|
| P1-2 state 纯缓存 + 原子写 | **阻断** | 闸门⑤ 前必闭（设计 §6.5/§6.6.3/§6.7 明确要求，批 2 应交付） |
| I1（我方）Link 脱敏 | 重要 | 同上，一行收口 |
| P1-1 真单飞（结果共享） | 建议 | 随批 2 修复一并（成本低）；若不采纳需在设计稿注明"单飞=互斥"口径 |
| P2-1 落盘失败分类 | 建议收口 | 一行 |
| P2-2 重启提示路径 | 建议收口 | 文案 |

**结论**：批 2 主体（一键授权、代次机制、无副作用验证、启动隔离、状态红绿灯）经双方独立审核确认正确；**闸门③ 以"修复 P1-2 + I1 后通过"为最终结论**，随后可进闸门⑤。

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 来源 | 状态 |
|---|---|---|---|---|
| P1-2 | 阻断 | state 仍存 refresh_token 且非原子写（设计 §6.5/§6.6.3/§6.7） | 外部 | 待处理 |
| I1 | 重要 | 百度 Link 网络错误未脱敏（access_token 进日志） | 我方 | 待处理 |
| P1-1 | 建议 | 刷新单飞升级为结果共享（设计原文口径为互斥） | 外部 | 待处理 |
| P2-1 | 建议 | 轮换落盘失败应按 §6.10 报存储异常（红） | 外部 | 待处理 |
| P2-2 | 建议 | 重启失败提示路径与实际部署（alist_init）不符 | 外部 | 待处理 |

---

*本报告基于 `6e8bd3d` 代码与 design-v1.3.0.md 原文的静态核对，未编译、未执行。*
