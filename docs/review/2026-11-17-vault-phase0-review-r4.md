# 第七轮复核：Vault Phase 0 处置确认（RV-2026-11-17-05）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-17-05 |
| 评审类型 | RV-04（I1–I4、P11–P18）处置复核 + 回归检查 |
| 评审对象 | `feature/file-encryption` @ `2a91d7f50a1e4c2a996432cae9c8ff1890ce1bf5` |
| 对比基线 | `4d18118`（RV-04 审核对象） |
| 评审方式 | 逐文件静态阅读（`vault.go`/`vault_test.go`/`conf`/`driver`/`webdav`/`main.go` 全量 diff，文档 diff）；**未编译、未执行** |
| 结论 | **通过（有条件）**：I1–I4、P11–P18 全部实质关闭且有测试佐证；但 **I3 的修复引入一处空 MIME 触发的 nil-map panic（I5）**，属回归，建议立即修（一行）；另 4 条文档/防御性建议 |

---

## 1. 处置复核

| 编号 | 意见 | 结论 | 证据 |
|---|---|---|---|
| I1 | Vault 无条件装配误伤天翼/移动 | **关闭** | `loadRoot` 区分两种失败：`err != nil`（manifest 缺失）→ 空树+日志；`link.Reader == nil`（URL 型驱动）→ **不注册该根、不接管同名目录**，仅 INFO 日志；`VaultWarning` 字段与 `displayName` 分支均已删除（告警不再覆盖挂载名）；`TestVaultURLOnlyDriverSkipped` 断言物理视图（含 `_mc` 目录）与零告警 |
| I2 | `max_streams` 超限 500（应 503） | **关闭** | 新增 `driver.ErrTooManyStreams`；`webdav.handleGetHead` 映射 503；`acquire(ctx, timeout)` 响应 ctx；注释改为「最多等待 acquireTimeout 后拒绝」 |
| I3 | GET 无 ETag | **关闭（但引回归，见 I5）** | `etagFor(e)` 统一生成，`buildChildren` 与 `Link` 共用；`TestVaultETagSameSource` 断言两端字符串相等 |
| I4 | 报告孤立代码围栏 | **关闭** | 多余围栏已删除；文档 diff 显示 §7.3 恢复「示例→注记→正文」的正常结构 |
| P11 | `VaultConfig` struct 值 omitempty 无效 | **关闭** | 改 `*VaultConfig`；`main` 判 `cfg.Vault != nil && cfg.Vault.Enabled`；空配置写回不再插入 vault 块 |
| P12 | manifest 无重载 | **关闭** | 报告 §6.4 与 design §4.3 均改为「启动加载、重启生效；定时重载 Phase 2」 |
| P13 | `vault.New` 签名不一致 | **关闭（残留示例问题见 P19）** | design §4.1 与报告 §8.2 统一为 `New(cached, raw, vaultKey, vaultCfg)`，并注明「以代码为准」 |
| P14 | 配置示例含未实现字段 | **关闭（格式问题见 P20）** | §7.8 逐字段标注「已实现 / Phase 2 预留」，并说明 manifest 实际固定路径 |
| P15 | `List` 全表扫描 | **关闭** | `loadRoot` 一次性 `buildChildren()` 建目录→子项索引并排序；`List` 变 O(子项)。**附带收益**：`webdav.isDir` 每请求调用的 `List(parent)` 也从 O(N) 降为 O(子项) |
| P16 | 信号量三处细节 | **基本关闭** | `semDecryptReader.Close` 用 `sync.Once`（`TestVaultStreamLimitDoubleClose` 覆盖）；`acquire` 响应 ctx。**③ 获取时机前置未落入 design §4.1**（见 P21） |
| P17 | 验收口径 | **关闭** | 报告 §9 注明 55 GB 用例执行环境为开发机、真机 Phase 1；PROGRESS 补 bench 口径（单线程 / 1 MiB 分块 / 纯内存无 I/O） |
| P18 | `e.Obj` 前缀校验 | **关闭** | `path.Clean(e.Obj)` 后强制 `_mc/o/` 前缀，非法条目跳过并 WARN |

---

## 2. 新增重要（I5）

### I5　空的 MIME 会让 `Link` 对 nil map 赋值而 panic（I3 修复引入）

- **位置**：`internal/vault/vault.go` / `Link`：
  ```go
  hdr := httpHeader(e.MIME)      // mime 为空时返回 nil
  hdr.Set("ETag", etagFor(e))    // nil map 赋值 → panic
  ```
  `httpHeader` 的实现未变（`if mime == "" { return nil }`）。
- **触发条件**：manifest 条目 `MIME` 为空。`mcrypt` 用 `mime.TypeByExtension(filepath.Ext(...))` 取值，**未知扩展名或无扩展名文件返回空串**（例如无扩展名文件、`.ts`/`.iso`/`.rar` 等在当前机器 mime 表中无映射的类型，取决于运行时是否加载 `/etc/mime.types`）。这类文件一旦被 GET，handler 内 panic。
- **后果**：`net/http` 按连接 recover 并关闭连接，进程不崩，但**该请求无响应**——播放器表现为「文件打不开」，且日志只有一行 panic 栈；对 PM 而言是「某些文件莫名播不了」，排查成本高。
- **修复**（一行）：`hdr := http.Header{}`，仅在 `e.MIME != ""` 时 `hdr.Set("Content-Type", e.MIME)`，再 `hdr.Set("ETag", …)`；或让 `httpHeader` 始终返回非 nil。**建议补一个 `MIME == ""` 的 Link 测试**（当前 6 组测试全部使用 `video/x-matroska`，未覆盖）。
- **说明**：该缺陷由 I3 的「Header 增 ETag」引入——此前 `Header: httpHeader(...)` 只是把 nil 赋给字段，遍历 nil 安全，故未暴露。

---

## 3. 建议（P19–P22）

| 编号 | 内容 | 建议 |
|---|---|---|
| P19 | design §4.1 示例仍含 `v.Start(ctx)` | `Vault` 无 `Start` 方法（只有 `Init`/`Stop`），示例照抄会编译失败。既然已声明「以代码为准」，应删掉该行或注明「示例仅示意装配顺序」 |
| P20 | §7.8 的 JSON 示例插入 `//` 注释 | JSON 不支持注释，读者复制该示例会解析失败。建议把「已实现/Phase 2 预留」标注移到代码块外的表格或列表，代码块内保持合法 JSON |
| P21 | P16③ 未落文档 | 处置说明称「获取时机前置列入 Phase 1 设计（P9 契约）」，但 design §4.1 未新增该条。建议补一行「信号量须在 `raw.Link` 之前获取」，否则 Phase 1 实现会重现「超限拒绝时已白开上游连接」 |
| P22 | `List` 返回共享切片 + `Warning()` 死代码 | ① `List` 直接返回 `r.children[inner]`，调用方若原地排序/追加会污染索引（当前调用方只读，属防御项；可返回 `append([]model.Obj(nil), …)`）；② `Warning()` 在 `main` 删除 `VaultWarning` 后已无调用方，建议接入 `/admin` 展示或删除，避免「看似有健康提示、实际只进日志」 |

---

## 4. 回归核对

| 项 | 结论 |
|---|---|
| 非 vault 路径 302/委托 | 未改动（`findRoot` 未命中仍委托 `cached`） |
| `_mc` 403/隐藏 | 未改动 |
| 断言边界（铁律 7） | 仍未实现 `TokenManager`/`HealthReporter`/`CredentialUpdater` |
| Stop 所有权 | `Vault.Stop` → `cached.Stop`，`main` 单入口 |
| manifest 缺失降级 | 空树 + 日志；`buildChildren` 保证 `List` 返回空列表而非 not found |
| URL 型驱动 | 不注册 vault 根 → 物理视图完整，`_mc` 可见（属普通目录，符合预期） |
| `conf.Save` 空 vault 块 | 指针 + omitempty 后不再出现 |
| 测试 | 新增 3 组（URL 型跳过、ETag 同源、双 Close 幂等）；缺 `MIME==""` 用例（见 I5） |

---

## 5. 放行意见

| 项 | 结论 |
|---|---|
| 闸门③（Vault 阶段） | **通过（有条件）**：I5 修复后即可视为完全闭环；其余均为文档/防御性项 |
| Phase 0 验收 | 可执行；建议在验收清单补一条「无扩展名/未知扩展名文件的 GET 不报错」（针对 I5） |
| Phase 1 开工前 | P19/P21 建议随设计文档一并修（示例可编译、信号量前置入契约）；P20/P22 随手 |

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I5 | 重要 | 空 MIME → `httpHeader` 返回 nil → `hdr.Set` panic（I3 回归） | | | 待处理 | | |
| P19 | 建议 | design §4.1 示例含不存在的 `v.Start` | | | 待处理 | | |
| P20 | 建议 | §7.8 JSON 示例含 `//` 注释（非法 JSON） | | | 待处理 | | |
| P21 | 建议 | 信号量前置未写入 design §4.1 | | | 待处理 | | |
| P22 | 建议 | `List` 返回共享切片；`Warning()` 已无调用方 | | | 待处理 | | |

---

*本报告基于 `2a91d7f` 的静态阅读，未编译、未执行、未接触真实凭证。*
