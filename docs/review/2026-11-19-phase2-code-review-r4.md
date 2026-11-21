# 真机二轮反馈处置复核（RV-2026-11-19-07）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-19-07 |
| 评审类型 | 真机反馈修复 + UX 优化 + 回归事故留痕复核（5 提交：9032722/206bcba/f1d67fb/9aeba42/200a5a2） |
| 评审对象 | `feature/file-encryption` @ `200a5a25aedcf68634c6b783399d4d9b11f7f3ee` |
| 对比基线 | `1c50a2b`（RV-06 放行点） |
| 评审方式 | `vault.go`/`main.go`/`admin.go`/`mobile/*`/`process.md`/`design.md`/测试 diff 静态阅读；**未编译、未执行** |
| 结论 | **通过**：净态正确；告警粘滞清除、铁律 2 装配门（终态）、管理页动态标签、文档恒同步规则均落地正确；仅 2 条建议 |

---

## 1. 处置与回归复核

| 项 | 结论 | 证据 |
|---|---|---|
| 告警粘滞清除（9032722） | **正确** | 容器级告警改为「本轮状态」：`scan` 用 `cwarns` + `cwMu` 累积本轮 `refreshRoot`/`checkNestedMc` 返回的告警，`defer` 在 `wg.Wait()` 后一次性 `c.warning = join(cwarns)`——问题修复后下轮自动清空；`TestContainerWarningClears` 覆盖。root 级告警靠 `buildRoot` 重建时 `warning=""` 自然清除 ✓ |
| 铁律 2 装配门（9032722 → f1d67fb 终态） | **正确（含一次回归，已闭环）** | `vaultSupported(driver)` 按 `Name()` 允许列表 `tianyi/mobile/local`；百度不装配、不扫描、无告警噪声。中间 9032722 曾误用日志 type 字段（189Personal/…）导致全云盘不装配，f1d67fb 修复 + `TestVaultSupported` + 9aeba42 留痕 + design §5.1 注记 |
| 管理页动态标签（206bcba） | **正确** | `admin.Mount.FieldsFunc`（渲染/提交同一 `fieldList()` 来源）；`CredentialFields` 按状态动态：过期→获取指引、<24h→不足 1 天、否则 ceil 剩余天数、解析失败回退「authorization」；`remainingDays` 毫秒运算正确、ceil 正确 |
| 过期文案 / 根页显示名 | **正确** | `HealthWarning` →「令牌已过期，请更新令牌」；`displayName` →「挂载名：告警」组合（健康挂载 `w==""` 无多余冒号） |
| 文档恒同步规则（200a5a2） | **正确** | `process.md` 沟通协议第 6 条；design §3.3/§5.1 补账；PROGRESS 留痕 |

---

## 2. 建议（2 条）

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | `TestVaultSupported` 用 `nameOnlyDriver` 硬编码 name，未对真实驱动断言 | 本次回归正是「手写允许列表名 ≠ `Name()` 返回值」。建议测试改为对**真实驱动实例**断言（`tianyi.New(...)`/`mobile.New(...)`/`baidu.New(...)`/`local.New(...)` 的 `Name()`），把「`Name()` ↔ 允许列表」不变量锁死，防止驱动改名后再次静默失效 |
| P2 | `scan` 的容器 `raw.List` 失败分支仍用 `setContainerWarning`（粘滞），与「本轮状态」机制不同 | 语义合理（List 失败无法验证修复，保留旧告警是**有意**的），但建议在该分支加一行注释说明，避免后续维护者误判为"漏了重置" |

---

## 3. 正面确认

1. **回归处置链完整**：真机发现「全云盘加密失效」→ 一小时内 f1d67fb 修复 → 9aeba42 留痕 → 200a5a2 把「文档恒同步」升级为 process.md 铁律级规则。这正是 `docs/process.md` 设计这套闭环想要的行为。
2. **告警粘滞清除的双层正确性**：容器级每轮重置（`cwarns`），root 级靠重建清除，两条路径都不再残留过期告警。
3. **动态标签的边界齐全**：过期 / <24h / ≥24h / 解析失败四分支，`TestCredentialFieldsDynamic` + `TestRemainingDaysCeil` 覆盖；毫秒→Duration 换算正确。
4. **并发正确**：`cwarn` 闭包 + `cwMu` 保护并发 append；`defer` 在 `wg.Wait()` 后读 `cwarns`（happens-before 成立，无锁读安全）。

---

## 4. 放行

| 项 | 结论 |
|---|---|
| 本轮处置 | **通过**：P1/P2 为加固项，不阻断 |
| 闸门⑤ | 继续真机验收（§5.1 8 项）；vaultSupported 修复后请复验一次「天翼/移动挂载的加密目录正常可见可播」，确认装配门未再误伤 |

---

## 5. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 状态 |
|---|---|---|---|---|
| P1 | 建议 | `TestVaultSupported` 改真实驱动断言 | | 待处理 |
| P2 | 建议 | `scan` List 失败分支注释说明粘滞语义 | | 待处理 |

---

*本报告基于 `200a5a2` 的静态阅读，未编译、未执行。*
