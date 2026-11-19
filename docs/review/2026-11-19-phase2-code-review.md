# Phase 2 代码审核（闸门③）——RV-2026-11-19-04

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-19-04 |
| 评审类型 | 代码审核（§5.1 容器自动发现 + §5.2 提示接 /admin + §5.3 移动启动健壮性） |
| 评审对象 | `feature/file-encryption` @ `5031371d8c594dca0842cd437f7d48f389663d84` |
| 对比基线 | `4e6cf13`（design v0.6 基线） |
| 评审方式 | `vault.go`/`vault_test.go`/`manifest.go`/`mobile/driver.go`/`main.go` 全文 + diff 静态阅读；**未编译、未执行** |
| 结论 | **通过（有条件）**：无 I 级问题；两级结构、懒扫描、摘要比对、告警双通道、移动启动健壮性均按 design v0.6 正确落地；留 4 条 P 级 + 1 条需确认 |

---

## 1. 与设计对照（§5.1/§5.2/§5.3）

| 设计要点 | 实现核对 | 结论 |
|---|---|---|
| 两级结构（容器→子根，findRoot 两段） | `findRoot` 按 segs[0]/segs[1] 匹配；`root.objBase = path.Join(cont,dir)` 修正对象路径为 `backup/dir1/_mc/o/…` | ✓ 且修正了 Phase 1 的 `objPath` 只拼容器名的问题 |
| `isMcPath` 第二段判定 + 保留名 `_` 403 | `isReservedContainerPath`（容器直接子目录 `_` 前缀 → 403）+ `isMcPath`（子根内 `/_mc`） | ✓ 两层屏蔽都不回落 302 |
| 懒扫描 5min TTL + refreshMu 去重 + 后台 ctx | `maybeScan` TTL 门 + `scan` 的 `refreshMu.TryLock` + 懒刷新 `context.Background()`（P9） | ✓ |
| 每目录 ≤5s 短超时 + 失败隔离 | `refreshRoot` 的 `manifestReadTimeout=5s` 包住 manifest 读；单目录失败记告警跳过 | ✓（容器级 List 除外，见 P1） |
| I4 摘要比对 | `LoadManifestSum` 返回**解压后 JSON** 的 SHA-256；未变跳过重建 | ✓ 且免疫 gzip mtime/GCM nonce |
| 8MiB 热刷新守卫 / 32 子目录守卫 | `maxHotManifest`/`maxSubDirs` 常量 + 跳过告警 | ✓ |
| P7 深层 `_mc` 检测 | `checkNestedMc`（非单元子目录下一层探测，≤8 目录） | ✓ |
| 原子换树 + InvalidateAll | `setRootTree` 锁内交换 + `invalidateCache()` | ✓ |
| **「含 `_mc` 必有子根」不变量** | manifest 不可读/损坏 → `emptyRoot` 占位 + `_mc` 恒 403，不回落普通模式 | ✓ 安全细节到位 |
| 铁律 2（百度禁用整挂载） | `disableVault` 清空容器 + `disabled` 标记 | ✓ |
| §5.2 双通道告警 | main 对原始驱动断言 `HealthReporter` + `vaultWarn[m.Name]=v.Warning`，admin 合并展示；`vaultWarn` 在闭包内延迟读取 | ✓ 铁律 7 尊重（Vault 未实现 HealthReporter） |
| §5.3 移动启动健壮性 | `discoverCloudHost` 包 10s 超时、失败不阻断启动、`hostWarn` 提示、`HealthWarning` 过期优先 | ✓ 核心场景（过期）优雅 |

---

## 2. 需确认（1 条）

### P5　`CommitCredential` 是否清除 `hostWarn`（提交说明称「提交成功自动清除」）

- **事实**：`Driver` 新增 `hostWarn` 字段（`commitMu` 保护）；`Init` 失败时写入；`HealthWarning` 读取。但本轮 diff 未含 `drivers/mobile/credential.go`。
- **风险**：若 `CommitCredential` 未在成功时清 `hostWarn`，用户经 /admin 更新凭证后，`HealthWarning` 仍显示「云主机发现失败」，与"新凭证已生效"矛盾。
- **要求**：确认 `credential.go` 成功路径已 `commitMu.Lock(); d.hostWarn = ""; …`；并补一条断言（发现失败 → 提交成功 → `HealthWarning()` 不含 hostWarn）。

---

## 3. 建议（4 条 P 级，均不阻断）

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | 容器级 `raw.List("/"+name)` 无短超时 | `scan` 里 `v.raw.List(bg, "/"+name)` 未包 `manifestReadTimeout`，依赖驱动自身 client 超时（mobile/tianyi 30s），非设计承诺的 ≤5s。影响小（懒刷新在后台 goroutine、前台立即返回旧树），但建议同样包一个 5s 超时，与"每目录 ≤5s"口径一致 |
| P2 | 初始同步扫描串行 N 子目录 | `Init` 同步 `scan` 逐个 `refreshRoot`（各 5s 超时），最坏 N×5s 阻塞启动（10 目录 = 50s）。建议：初始扫描按目录并发（各 goroutine），或给整次启动扫描一个总预算（如 15s）后剩余转懒加载 |
| P3 | `disableVault` 后 `scan` 循环继续 | `disableVault` 清空 `containers` 后，当前 `scan` 的 `for dirs` 循环仍会继续调 `refreshRoot`（做无谓的 `raw.List/Link` 网络调用），`setRootTree`/`setContainerWarning` 因容器为空而 no-op。建议 `disableVault` 后中断该循环（返回哨兵或再查 `disabled`） |
| P4 | §5.3「有效 token + host 发现失败」场景 List 仍 500 | 过期场景已优雅（`List` 返回空目录）；但 token 有效而 host 为空时，`personalRequest` 返回「cloud host not set」→ 浏览 500。`hostWarn` 文案称"服务受限"，半优雅。建议：`List`/`Link` 在 `cloudHost == ""` 时也走「空目录 + 明确错误」降级，与过期场景对齐 |

---

## 4. 回归核对

| 项 | 结论 |
|---|---|
| Phase 1 单根语义 | 容器化后 `backup/_mc`（旧结构）被 `isReservedContainerPath` 403 屏蔽 + `scan` 保留名告警 ✓；不再被当普通目录 |
| 非 vault 路径 302 | 未命中容器/子根 → 委托 `cached` ✓ |
| `_mc` 密文恒 403 | 「含 `_mc` 必有子根」不变量 + `emptyRoot` 占位 ✓（损坏清单也不回落普通模式） |
| 信号量/绑定校验/ETag 同源 | 未改动，`objBase` 修正后 `Link` 路径正确 ✓ |
| 并发安全 | `root` 发布后不可变（`warning` 单独 RLock）；`findRoot` 返回的旧 `r` 快照在换树期间一致可读；`manifestSum`/`roots` 均在 `v.mu` 下读写 ✓ 无数据竞争 |
| 测试 | 新增 6 组容器测试（自动发现/无变化跳过/重建/摘除/保留名/深层 `_mc`/懒触发）+ 12 组迁移，覆盖 §5.1 关键路径 ✓ |

---

## 5. 放行意见

| 项 | 结论 |
|---|---|
| 闸门③ | **通过（有条件）**：P5 确认后即闭环；P1–P4 均属边界降级与效率，可与闸门⑤ 真机验收并行观察，不需回退 |
| 下一阶段 | 闸门④（`go vet`+`go test -race`，已自报全绿）→ 闸门⑤ 真机验收（§5.1 验收 8 项，含天翼个人站加密验收） |

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 状态 |
|---|---|---|---|---|
| P5 | 需确认 | `CommitCredential` 清除 `hostWarn` 需核实 + 补测试 | 待确认 | 待处理 |
| P1 | 建议 | 容器级 `raw.List` 无短超时 | | 待处理 |
| P2 | 建议 | 初始同步扫描串行 N×5s 阻塞启动 | | 待处理 |
| P3 | 建议 | `disableVault` 后 scan 循环继续 | | 待处理 |
| P4 | 建议 | 有效 token + host 失败时 List 仍 500 | | 待处理 |

---

*本报告基于 `5031371` 的静态阅读，未编译、未执行。*
