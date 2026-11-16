# 闸门③ 代码审核（RV-2026-11-16-03）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-16-03 |
| 评审类型 | 代码审核（闸门③）——v0.10.0 凭证网页管理 + 3 处转义修复 |
| 评审对象 | `feature/file-encryption` @ `b12ff78741a0708b008f1a5b40a206f04f545c5e`（含前置文档提交 `d7df2d3`） |
| 对比基线 | `8d8a7db`（设计 v0.2 / 报告 v1.1） |
| 评审方式 | GitHub 上逐文件静态阅读（`main.go`、`internal/admin`、`drivers/mobile`、`internal/conf`、`internal/fs`、`internal/webdav`、`internal/driver`）；**未编译、未执行测试**，测试结论以代码与用例内容为准 |
| 结论 | **有条件通过**：无阻断级缺陷；6 条重要（I1–I6）须修或留痕，8 条建议（P1–P8）+ 1 条流程（P9）随本轮处置；修复后可进闸门④ |

---

## 1. 总体评价

本次实现与 `docs/design.md` v0.3 的两阶段契约基本一致，几处关键难点处理得比预期扎实：

1. **两阶段确实做到了"验证零状态"**：`ValidateCredential` 用一次性 `NewClient()` 做网络验证，正式 client、token state、config 均不动；admin 侧三条失败路径（验证失败 / 持久化失败 / 提交异常）都有短路测试。
2. **CSRF 与转义到位**：一次性令牌、消费即失效、`html/template` 全量转义（含注入 `<script>` 的用例）、根 PROPFIND 用 `xml.EscapeText`、href 走 `url.PathEscape` 逐段编码且只编码一次（无双重编码）。
3. **并发防护有设计而非堆 if**：`__req` 隔离待提交凭证、5 分钟过期清理、`credGen` 丢弃过期刷新、`cacheGen` 作废在途路径解析、提交后清 `pathCache` + `CachedReader.InvalidateAll`——这一组针对"401 刷新与手工提交打架""更新后 30 秒旧目录"两个具体症状，方向正确。
4. **写入路径的错误处理不糊弄**：`conf.Save` 的 tmp→chmod→fsync→rename 每一步失败都 `Remove(tmp)` 并返回错误，没有 `_ =`；`SaveToken` 改为返回 error 并全链路检查。

以下意见按"是否会导致用户可见错误/数据与配置不一致"排序，**没有需要回退重做的设计问题**。

---

## 2. 重要（I1–I6）

### I1　双标签并发提交可导致"配置与运行时不一致"

- **位置**：`internal/admin/admin.go` / `Handler.Update`（validate → persist → commit 三段之间无串行化）；`Handler.mu` 只保护 `tokens`。
- **场景**：标签 A 提交凭证 A、标签 B 同时提交凭证 B。交错为 A.validate → B.validate → A.persist → B.persist → B.commit → A.commit 时，**磁盘是 B、运行时是 A**；用户看到两次"更新成功"，重启后生效的是 B。`__req` 只隔离了待提交凭证，没有解决"最后写入者 vs 最后提交者"的倒置。
- **要求**：`Handler` 增加一把更新锁（建议 per-mount：`map[string]*sync.Mutex`，或简单全局），覆盖 `ValidateCredential → persist → CommitCredential` 全流程；并补一个并发用例（两个 goroutine 交错提交，断言"最终 config 条目的凭证 == 运行时 client 的凭证"）。现有 `admin_test.go` 没有并发用例。

### I2　config.json 缺失时，写回路径会重建默认配置（admin/admin）

- **位置**：`main.go` 的 persist 闭包调用 `conf.Load`；`internal/conf/config.go` / `Load` 在文件不存在时 `cfg.Save(path)` 写默认配置并返回。
- **后果**：运行中若 `config.json` 被误删/改名，一次管理页更新会先**落盘一份默认配置**（`server.username=admin`、`password=admin`、一个 local 云盘），随后因"未找到挂载"返回错误。当前进程仍用启动时的快照认证，**但重启后 Basic Auth 变成 admin/admin**，属认证降级，且用户看不到任何提示。
- **要求**：persist 路径不要复用会"造文件"的 `Load`。改为严格读取（`os.ReadFile` + `json.Unmarshal` + `normalize`，文件缺失或解析失败直接报错），或给 `conf` 增加 `LoadStrict` 供写回路径使用；`conf.Load` 的"自动创建"只保留给启动路径。

### I3　`Save` 只 fsync 文件，未 fsync 父目录

- **位置**：`internal/conf/config.go` / `Save`（`f.Sync()` 在 rename 前完成，但 rename 之后没有对目录做 fsync）。
- **后果**：路由器断电/重启后，rename 可能未持久化，config.json 仍是旧内容——表现为"网页更新成功、重启后又变回旧凭证"，正好命中闸门⑤ 验收第 2 条（重启后仍有效），排查时极易被误判为功能缺陷。
- **要求**：rename 成功后 `os.Open(dir)` + `Sync()`（Linux 允许对目录 fsync；UBIFS/ext4 均适用），失败记 WARN 不致命；`Backup` 同样处理。若担心某些文件系统不支持，按"尽力而为 + 日志"实现即可，但要在 design §3.3 写明这一取舍。

### I4　风险最高的"写回配置"逻辑没有测试

- **位置**：`main.go` 的 persist 闭包（字段映射 `values["authorization"]`、以磁盘为准重新 Load、只改匹配挂载、`conf.Backup` + `Save`、成功后替换 `cfg.Clouds`）。
- **问题**：`admin_test.go` 用的是 fake persist，只验证调用顺序；按 commit 说明，`main` 侧新增测试针对的是转义函数（`xmlText` / `encodeURLPath`），**未见 persist 闭包的用例**（若已有请指出，本条即撤销）。该闭包包含三处易错点：字段映射、其它 clouds 条目是否被保留、旧版单数 `cloud` 迁移后是否丢配置。
- **要求**：抽成可测函数（例如 `conf.WriteCredential(path, mount, values)` 或 `internal/admin` 注入的独立函数），补四个用例：① 正常写入且其它 clouds 不变；② mount 不存在时返回错误且文件未被修改；③ 旧版 `{"cloud":{...}}` 迁移为 `clouds` 后内容等价；④ 文件缺失时报错且不创建文件（与 I2 同一条）。

### I5　401 刷新 × 提交的代次防护没有测试

- **位置**：`drivers/mobile/driver.go` / `Init` 内的 401 回调（`credGen` 比较与恢复逻辑）、`credential.go` / `CommitCredential`（`credGen` / `cacheGen` 递增）。
- **问题**：这是本轮最难的并发代码，目前只覆盖了 happy path（`TestCommitCredentialAppliesAndPersists`）与状态写失败。没有用例证明"刷新进行中发生了提交 → 最终使用已提交凭证、token state 与之一致"。
- **要求**：补一个可阻塞的假刷新（httptest handler 里 sleep/阻塞到测试放行），在 refresh 未返回时调用 `CommitCredential`，放行后断言：`client` 的 authorization == 已提交值、`LoadToken` 的 Authorization == 已提交值、`credGen` 只增了一次。

### I6　提交失败时配置已改，但页面与汇报都说"什么都没动"

- **位置**：`Handler.Update`（persist 成功后 `CommitCredential` 失败 → 渲染"凭证生效失败"）；`docs/design.md` §3.7-8 只承诺"验证失败 / config 写失败零变化"。
- **问题**：`CommitCredential` 的普通错误路径（非 `ErrStateWriteFailed`）在当前流程里几乎不可达（pending 在同一请求内不会过期），但一旦发生，磁盘已写入新凭证而运行时仍是旧凭证——与 PM 汇报的"任何一步失败都什么都没动"不一致，也与页面文案"凭证生效失败"给人的印象冲突。
- **要求**：二选一并写进 design §3.3：① 失败时用 `.bak` 把 config 恢复（真正零变化，最符合验收 8）；② 保持现状但改文案为"配置已更新，运行时未生效，请重启服务"，并在验收 8 中补一句"Commit 失败属部分成功"。推荐 ①。

---

## 3. 建议（P1–P9）

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | 所有错误分支都返回 200（CSRF 失败、验证失败、写盘失败） | 分别返回 400 / 502，便于脚本判断与日志聚合；错误页本身可保持同一模板 |
| P2 | 缺基础安全响应头 | 管理页加 `Cache-Control: no-store`、`X-Frame-Options: DENY`、最小 `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; form-action 'self'`（模板内联 style，需允许 inline style）——三条一行一个，成本极低 |
| P3 | `main.go` 的 `version` 仍是 `v0.9.2` | 真机验收日志会显示旧版本，容易误导排查；建议本轮或部署前升 `v0.10.0`，README 变更记录留到闸门⑥ |
| P4 | `CachedReader.Stop` 仍是 `close(chan)`，未 `sync.Once` | 当前无二次调用路径（每个 handler 一个 CachedReader），不构成 bug；但 design §4.1 与 PROGRESS 的 I7 行写"已改"，实际是 Phase 0 事项——请在 PROGRESS 注明"延后至 Vault Phase 0"，避免被当成已完成 |
| P5 | `CommitCredential` 只更新 `client`，未更新 `d.authorization` | 当前无副作用（`authorization` 字段只在 `Init` 读取），属潜在隐患（未来若重新 `Init` 会回退旧值）；建议提交后同步该字段或删除字段只留 client |
| P6 | 路径编码有两份实现 | `main.go` / `encodeURLPath` 与 `internal/webdav/webdav.go` / `encodePathSegments` 逻辑相同；建议抽到 `internal/webdav` 导出或新建 `internal/urlesc`，避免两处漂移 |
| P7 | 即将过期的 token 也算"验证通过" | `validateAuthorization` 只拒绝已过期；剩余 2 分钟的 token 会显示"更新成功"然后立刻过期。建议要求剩余有效期 ≥ 24h（与 `needRefresh` 口径一致），否则提示"该凭证即将过期，请获取新的" |
| P8 | `docs/design.md` §3.4 与实现不符 | §3.4-2 写"`CommitCredential`：应用新值 → `discoverCloudHost` → `saveTokenState`"，实际 `CommitCredential` **不联网**（host 由 Validate 阶段的临时 client 发现后带入），与 §3.2"同步、不联网"一致。改文档即可（三处口径统一） |
| P9 | 自查 15 条无逐条留痕 | commit message 只写"15 条全部修复"，PROGRESS 与 design §3.8 只汇总了机制。闸门③ 无法复核"是否真的都改了"。建议在 PROGRESS 补一张 15 行表（问题 / 处置 / 涉及文件 / 测试），或在 design §3.8 列条目 |

---

## 4. 已核对成立的部分（不必再改）

| # | 核对项 | 结论 |
|---|---|---|
| 1 | 两阶段语义：Validate 不改正式状态 | 成立（临时 `NewClient`，仅 `SetAuth` 到临时实例） |
| 2 | 失败短路：验证失败不写盘、写盘失败不提交 | 成立（`admin_test.go` 三个用例 + 代码顺序） |
| 3 | CSRF 一次性 + 过期 | 成立（`checkToken` 消费即删；30 分钟 TTL；重放有测试） |
| 4 | HTML 转义（挂载名、警告、字段标签、错误文本） | 成立（`html/template`；注入用例） |
| 5 | XML 转义 + href 百分号编码（根 PROPFIND / 目录页 / PROPFIND） | 成立（`xmlText`、`escPath` 逐段编码，单次编码） |
| 6 | 原子写顺序与错误处理（tmp→chmod→fsync→rename，失败清理） | 成立（`Save` 每步 `Remove(tmp)`；错误不吞） |
| 7 | 权限收紧（Save 0600、Backup 0600、Load 启动 chmod、`.bak/.tmp` 进 gitignore） | 成立 |
| 8 | 提交后缓存失效（driver `pathCache` + `CachedReader.InvalidateAll`） | 成立（`OnCommitted` 按挂载名匹配 handler） |
| 9 | Basic Auth 快照，避免 `cfg` 运行期竞争 | 成立（`serverUser/serverPass` 启动快照） |
| 10 | 驱动断言在原始驱动上（不是包装层） | 成立（`m.Drv.(driver.CredentialUpdater)`） |
| 11 | ctx 全链路（`personalRequest` / `discoverCloudHost` 带 ctx，10s 超时生效） | 成立 |
| 12 | 移动驱动"过期返回空目录"行为保留（302 路径不受影响） | 成立 |

---

## 5. 未验证项（不作为结论依据）

- 未运行 `go vet` / `go test -race` / ARM64 交叉编译；"全绿""编译通过"以程序员汇报为准。
- 未在真机/浏览器做冒烟；管理页实际渲染效果、Basic Auth 401 表现未实测。
- 未审计 `drivers/tianyi` / `drivers/baidu` 的改动（本轮无实质变更，仅上下文）。
- 未测 `conf` 包新增用例的具体断言内容（仅通过 commit 说明与文件清单确认存在）。

---

## 6. 放行条件

| 阶段 | 条件 |
|---|---|
| 进闸门④（测试门禁） | I1–I6 逐条处置（改动或明确留痕理由）；P1–P8 可随手修，P9 建议本轮补 |
| 进闸门⑤（真机验收） | 上文修复后重跑 `go vet` + `go test -race`；`version` 升 `v0.10.0`（P3）；准备"重启后仍有效"的断电/重启用例时注意 I3 的 fsync 语义 |
| 部署脚本 | 建议在 I1–I6 处置完成、审核复核后单独提交，并单独做一次脚本审核（见对话中的决策说明） |

---

## 7. 处置表（待回填）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 双标签并发提交可致 config 与运行时不一致 | | | 待处理 | | |
| I2 | 重要 | 写回路径在 config.json 缺失时重建默认配置（admin/admin） | | | 待处理 | | |
| I3 | 重要 | Save 未 fsync 父目录，断电后 rename 可能丢失 | | | 待处理 | | |
| I4 | 重要 | persist 闭包（最高风险写路径）无测试 | | | 待处理 | | |
| I5 | 重要 | 401 刷新 × 提交的代次防护无测试 | | | 待处理 | | |
| I6 | 重要 | 提交失败时 config 已改，与"零变化"承诺不符 | | | 待处理 | | |
| P1 | 建议 | 错误分支统一 200 | | | 待处理 | | |
| P2 | 建议 | 缺 no-store / X-Frame-Options / CSP | | | 待处理 | | |
| P3 | 建议 | version 仍 v0.9.2 | | | 待处理 | | |
| P4 | 建议 | Stop 未 sync.Once，文档称已改 | | | 待处理 | | |
| P5 | 建议 | d.authorization 提交后未同步 | | | 待处理 | | |
| P6 | 建议 | 路径编码两份实现 | | | 待处理 | | |
| P7 | 建议 | 即将过期 token 判为验证成功 | | | 待处理 | | |
| P8 | 建议 | design §3.4 与 Commit 实现不符 | | | 待处理 | | |
| P9 | 建议 | 自查 15 条无逐条留痕 | | | 待处理 | | |

---

*本报告基于 `b12ff78` 的静态代码阅读，未编译、未执行、未接触真实凭证。I1–I6 均属局部修改，不涉及架构调整。*
