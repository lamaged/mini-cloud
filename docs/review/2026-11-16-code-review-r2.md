# 闸门③ 复核（RV-2026-11-16-04）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-16-04 |
| 评审类型 | 代码审核复核（RV-03 的 15 条处置验证）+ 部署脚本首轮审核 |
| 评审对象 | `feature/file-encryption` @ `ee621963c150e4dc36339ed04fea6739dfaf5014`（含 `58ff6b0` 修复、`a09ac12` 脚本、`ee62196` 验收清单） |
| 对比基线 | `b12ff78`（RV-03 审核对象） |
| 评审方式 | GitHub 上逐文件静态阅读 diff 与关键文件全文；**未编译、未执行** |
| 结论 | **通过（闸门③ 复核）**：RV-03 的 6 条重要 + 9 条建议全部关闭且有测试佐证；新增 4 条重要（I7–I10，均为部署脚本与管理页边缘路径）、5 条建议；不阻断闸门④ |

---

## 1. 复核结论

- **RV-03 的 I1–I6 全部实质修复**，不是"改文档了事"：每一条都能在新代码里找到对应机制，并有针对性用例（见 §2）。
- **P1–P9 全部落实**，其中 P4（Stop 幂等）、P6（编码去重）、P7（24h 门槛）属于"顺手把该做的做了"，没有引入多余抽象。
- **`conf` 的重构方向正确**：`LoadStrict`（绝不造文件）与 `writeFileAtomic`（tmp→chmod→fsync→rename→目录 fsync）把"写坏配置"和"断电丢 rename"两类风险从"文档承诺"变成了"代码保证"。
- **新增的 4 条重要意见集中在两个地方**：管理页更新锁的获取时机、回滚失败时的文案；以及部署脚本的进程生命周期与日志。都不影响闸门④（测试门禁），但**部署脚本的问题建议在闸门⑤ 之前处理**，否则真机验收会踩。

---

## 2. RV-03 逐条复核

| 编号 | 意见 | 结论 | 证据（ee62196） |
|---|---|---|---|
| I1 | 双标签并发致 config 与运行时错位 | **关闭** | `admin.Handler.updateMu` 覆盖 validate→persist→commit；`TestUpdateSerialized` 断言 A 持锁时 B 被挡（100ms 窗口） |
| I2 | 写回路径重建默认配置（admin/admin） | **关闭** | `conf.LoadStrict` 缺失/损坏直接报错；`Load` 仅在 `IsNotExist` 时造默认值；`TestWriteCredentialMissingFile` 断言"不重建" |
| I3 | 未 fsync 父目录，断电丢 rename | **关闭** | `writeFileAtomic` 在 rename 后 `syncDir`（失败仅 WARN）；`Backup` 同样；design §3.3 写明"尽力而为"取舍 |
| I4 | 最高风险写路径无测试 | **关闭** | `conf.WriteCredential` 抽出，5 个用例覆盖：正常（含其它条目保留）/挂载不存在（文件不动、无 .bak）/legacy `cloud` 迁移/文件缺失/无效 JSON；另有 `TestRestoreBackup` |
| I5 | 401 刷新 × 提交竞态无测试 | **关闭（以测试为据）** | 回调提取为 `handleAuthFailed`，`Client.refreshURL` 可注入；`TestAuthRefreshRaceWithCommit` 用阻塞式 httptest 端点断言"刷新结果被丢弃、内存与 token state 均为已提交凭证" |
| I6 | 提交失败时配置已改 | **关闭** | `PersistFunc` 返回 rollback；admin 在普通提交错误时调用 `.bak` 还原并渲染"配置已还原"；`TestCommitPlainErrorRollsBack`；design §3.3/§3.7-8 同步 |
| P1 | 错误分支统一 200 | **关闭** | 400（表单/CSRF/挂载）、502（验证）、500（写盘/提交）、200（成功），测试断言齐 |
| P2 | 缺安全响应头 | **关闭** | `no-store` / `X-Frame-Options: DENY` / CSP（`default-src 'none'; style-src 'unsafe-inline'; form-action 'self'`）+ `TestPageSecurityHeaders` |
| P3 | version 仍旧 | **关闭** | `const version = "v0.10.0"`；README 变更记录按流程留闸门⑥ |
| P4 | Stop 非幂等 | **关闭** | `stopOnce sync.Once` + `TestStopIdempotent`（提前于 Vault Phase 0 落地，已说明） |
| P5 | `d.authorization` 不同步 | **关闭** | `CommitCredential` 在 `commitMu` 内同步该字段 |
| P6 | 路径编码两份实现 | **关闭** | `webdav.EncodePathSegments` 导出，main 复用，`encodeURLPath` 删除，`net/url` 导入清理 |
| P7 | 即将过期 token 判为通过 | **关闭** | 24h 门槛 + `TestValidateAuthorizationTooShort`；原有 mock 用例同步改为 30 天（否则会被门槛误杀，改得对） |
| P8 | design §3.4 与实现不符 | **关闭** | §3.4-2 改为"应用 Validate 暂存凭证 → saveTokenState；不联网" |
| P9 | 自查 15 条无留痕 | **关闭** | PROGRESS 增 15 行表（问题/处置/涉及文件/测试），其中 2 行标注"代码结构保证"，属诚实留痕 |

---

## 3. 新增重要（I7–I10）

### I7　`updateMu` 在 `ParseForm` 之前获取，慢请求可长期占锁

- **位置**：`internal/admin/admin.go` / `Handler.Update`（`h.updateMu.Lock()` 是所有逻辑的第一行）。
- **问题**：`r.ParseForm()` 会读取请求体；`main.go` 的 `http.Server` **未设置 `ReadTimeout`/`ReadHeaderTimeout`**，因此一个只发了半截 body 的连接可以让 `ParseForm` 挂很久，期间所有管理页更新（包括另一个标签页）全被挡住。虽然 Basic Auth 要求已认证，但"浏览器卡住/连接半开"是真实场景。
- **建议**：把 `updateMu.Lock()` 移到 `ParseForm` + CSRF 校验之后、`ValidateCredential` 之前。CSRF 令牌的消费不需要串行化，串行化只需要覆盖"验证→写盘→生效"。
- **附带**：建议给 `http.Server` 补 `ReadHeaderTimeout: 10 * time.Second`（与 `IdleTimeout`），这是对全站都有收益的一行配置，可另立小项。

### I8　回滚失败时仍显示"配置已还原"

- **位置**：`admin.Update` 的 `rollback()` 调用与文案；`main.go` 回滚闭包只 `slog.Error` 后 `return`。
- **问题**：`rollback` 无返回值。若 `.bak` 缺失/磁盘只读/权限不足，配置实际仍是新值，但页面固定显示"凭证生效失败，配置已还原"——把"没还原"说成"还原了"，比不说更糟（用户会以为无需处理）。
- **建议**：`PersistFunc` 的 rollback 改为 `func() error`，admin 按结果分两种文案：成功 →"配置已还原"；失败 →"回滚失败，请手工核对 config.json（.bak 仍保留）"，并记 WARN。

### I9　部署脚本的进程生命周期与日志（闸门⑤ 前必须处理）

`scripts/deploy-router.sh` 有三个会直接影响真机验收的问题：

1. **启动方式未脱离 SSH 会话**：`./$BINARY -config "$CONFIG" >> /dev/null 2>&1 &` 没有 `nohup`/`setsid`/`start-stop-daemon`。脚本由 SSH 交互执行，会话结束时子进程可能收到 SIGHUP 被杀——表现为"部署成功，断开 SSH 后服务没了"。
2. **`sleep 3` 可能不够**：`drv.Init` 里天翼/移动/百度都要联网（登录、发现云主机、刷新 token），弱网下启动 5–10 秒很常见。健康检查太早 → 判定失败 → 明明装好了却自动回滚。
3. **健康检查只证明"端口有人监听"**：`pkill` 后 `sleep 2`，若旧进程没死透，`mv` 后新进程起不来（端口占用），而旧进程仍在应答 → 健康检查 200/401 → 脚本报"部署成功"，实际跑的还是旧二进制。

- **建议**：
  - 用 OpenWrt 自带方式启动：`start-stop-daemon -S -b -x "$INSTALL_DIR/$BINARY" -- -config "$CONFIG"`，或直接落一个 `procd` init 脚本（更符合路由器惯例，且能开机自启、崩溃自动拉起）。
  - 健康检查改为**重试轮询**（如 1s × 15 次），而不是 `sleep 3` 一次定生死。
  - 健康检查前先确认旧进程已退出（`pgrep -f "$BINARY" | wc -l` 为 0，或按 pidfile 精确 kill），启动后再确认"进程是我刚起的那个"（记录 PID 并检查存活）。
  - 日志不要丢进 `/dev/null`：改为 `>> "$INSTALL_DIR/mini-cloud.log" 2>&1`，否则验收清单 §1 要求的"启动日志版本号为 v0.10.0"无法核对（见 I10）。

### I10　验收清单与脚本行为不一致（两处）

- **§1「启动日志版本号为 v0.10.0」**：脚本把 stdout/stderr 全部重定向到 `/dev/null`，该条**无法执行**。
- **§5 回滚演练「用空文件冒充产物部署」**：脚本在拉取后先做 `[ ! -s "$NEW" ]` 检查并直接 `exit 1`，**根本不会走到健康检查与回滚分支**。该演练实际验证的是"空文件被拒"，不是"回滚可用"。
- **建议**：① 日志落文件（见 I9）；② 回滚演练改为"放一个能执行但立即退出的假二进制（如 `#!/bin/sh\nexit 1` 并 chmod +x，或用 `false` 冒充）"，才能触发健康检查失败 → 自动回滚路径。

---

## 4. 建议（P10–P14）

| 编号 | 内容 | 建议 |
|---|---|---|
| P10 | 两个脚本在 git 中为 `100644`（非可执行） | `deploy-prepare.sh` 注释写 `./scripts/deploy-prepare.sh`，实际会 Permission denied。要么 `chmod +x` 后提交（`git update-index --chmod=+x`），要么文档统一写 `sh scripts/...` |
| P11 | `admin.Update` 的兜底分支会误报成功 | `if err != nil && !applied && rollback != nil` 之后的 `if err != nil` 会把"提交失败且无 rollback"渲染成 200「更新成功」。当前 `PersistFunc` 契约保证 rollback 非 nil，属不可达；但仍建议改为显式 `if applied { …成功（警告）… } else { 500 }`，避免将来 Contract 变化时静默错误 |
| P12 | `TestUpdateSerialized` 只证明"被挡"，未证明"最终一致" | 建议 fake persist/updater 记录调用顺序与入参，断言"最后一次提交的值 == 最后写入 config 的值 == 运行时值"。这才是 I1 真正要防的错位 |
| P13 | `deploy-prepare.sh` 细节 | 固定 `/tmp/mini-cloud-arm64` 并发运行会互相覆盖；未检查 `$UPX_BIN` 是否存在（失败信息不直观）。建议 `mktemp -d` + `command -v "$UPX_BIN" || { echo …; exit 1; }` |
| P14 | 路由器重启后服务不自启 | 目前无 init 脚本，路由器重启后需手工启动。验收清单 §2 写的是"重启**服务**后"，口径正确；但建议在 README/验收清单明确"路由器整机重启不在 v0.10.0 验收范围，开机自启列入 v0.11 或单独技术债"，避免下次被当成回归 |

---

## 5. 已核对无回归

| # | 项 | 结论 |
|---|---|---|
| 1 | `conf.Load` 启动路径仍会在文件缺失时创建默认配置 | 成立（`Load` 包 `LoadStrict`，仅在 `IsNotExist` 时建） |
| 2 | 权限收紧路径（`LoadStrict` chmod 0600、`Save`/`Backup` 0600） | 保留 |
| 3 | `WriteCredential` 不改非目标挂载、先备份后写、失败不落盘 | 成立（3 个用例覆盖） |
| 4 | 回滚用 `.bak`，不触碰 driver 状态 | 成立（提交失败=未生效，回滚只动 config 与内存副本） |
| 5 | `main` 的 `cfg.Clouds` 只在写盘成功后更新，回滚时同步 | 成立（`cfgMu` 保护） |
| 6 | 根页面/根 PROPFIND 改用 `webdav.EncodePathSegments`，编码仍为单次 | 成立（`escPath` 内部同一函数） |
| 7 | 管理页失败仍返回完整表单（可重试），仅状态码变化 | 成立 |
| 8 | 移动驱动"过期返回空目录 + 健康提示"行为未变 | 成立 |
| 9 | 302 路径未触碰 | 成立 |

---

## 6. 放行条件

| 阶段 | 条件 |
|---|---|
| **闸门④（测试门禁）** | 直接可进：代码侧无阻断项；建议顺手做 P11（5 行）、P12（测试增强） |
| **闸门⑤（真机验收）前** | **I9/I10 必须处理**（否则"断开 SSH 服务消失""启动日志看不到""回滚演练走不到回滚分支"三条会在验收现场变成假失败或假通过）；P10（脚本可执行位）、P13（prepare 细节）建议一并 |
| **闸门⑥（发布）** | P14 口径写入 README/验收清单；README 变更记录 + tag 按 process §3 ⑥ |

---

## 7. 处置表（待回填）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I7 | 重要 | updateMu 在 ParseForm 前获取，慢请求占锁 | | | 待处理 | | |
| I8 | 重要 | 回滚失败仍显示"配置已还原" | | | 待处理 | | |
| I9 | 重要 | 部署脚本：未脱离会话 / sleep 3 太短 / 健康检查无法区分进程 / 日志丢弃 | | | 待处理 | | |
| I10 | 重要 | 验收清单两处与脚本行为不一致（启动日志、回滚演练） | | | 待处理 | | |
| P10 | 建议 | 脚本文件模式 644 | | | 待处理 | | |
| P11 | 建议 | admin 兜底分支会误报成功 | | | 待处理 | | |
| P12 | 建议 | 并发测试未断言最终一致性 | | | 待处理 | | |
| P13 | 建议 | prepare 脚本固定 /tmp、未检查 upx | | | 待处理 | | |
| P14 | 建议 | 路由器重启不自启，需明确口径 | | | 待处理 | | |

---

## 8. 未验证项

- 未运行 `go vet` / `go test -race` / ARM64 交叉编译；以程序员汇报为准。
- `handleAuthFailed` 方法体在 diff 中截断，I5 的结论以 `TestAuthRefreshRaceWithCommit` 的断言为准（测试本身逻辑自洽）。
- 未执行任何脚本（本地为 master 工作区，且禁止读本地工程文件）；脚本结论来自逐行阅读。
- 未核对 `docs/acceptance-v0.10.0.md` 与 design §3.7 的逐条映射完整性（仅核对了与脚本冲突的两处）。

---

*本报告基于 `ee62196` 的静态阅读，未编译、未执行、未接触真实凭证。I7–I10 均为局部修改；I9/I10 属部署与验收口径，不影响 v0.10.0 功能正确性。*
