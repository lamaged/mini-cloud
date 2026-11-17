# 闸门③ 代码审核：Phase 1 接线与取数层（RV-2026-11-17-08）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-17-08 |
| 评审类型 | 代码审核（RV-07 处置复核 + Phase 1 接线 + 配置自愈 + mcrypt Windows） |
| 评审对象 | `feature/file-encryption` @ `f8a5cc0e83bbbeeace1ec61d4748460107979061`（6 个提交：`091d6a7`…`f8a5cc0`） |
| 对比基线 | `b342202`（RV-2026-11-17-07 审核点） |
| 评审方式 | 逐文件静态阅读（`upstream.go`、`vault.go`、`reader.go`、`conf/config.go`、测试与 diff）；**未编译、未执行**；真机证据以 PM 记录为准 |
| 结论 | **有条件通过**：RV-07 全部闭环，接线与绑定校验正确，资源释放无泄漏；新增 5 条重要（2 条属 EOF 边界、2 条属配置自愈数据安全、1 条属功能范围），无需要回退的阻断项 |

---

## 1. RV-07 处置复核

| 编号 | 意见 | 结论 | 证据 |
|---|---|---|---|
| U1 | 缓冲命中短读不补齐 | **关闭** | `ReadAt` 改「循环 readOnce 补齐 + 短读必带 io.EOF」；命中判定改「完整覆盖」（`off+len(p) <= bufEnd`）；`TestUpstreamChunkCrossingWindow` 以 1 MiB+16 连读 8 块，断言每块满长、内容一致、请求 3 次 |
| U2 | 随机 Seek 后预读永久失效 | **关闭** | 206 路径不再区分顺序/随机，一律写入缓冲（替换旧窗口）；`TestUpstreamJumpThenSequential` 断言跳跃后 3 块仅 1 次请求 |
| U3 | 信号量未前置 | **关闭** | `Vault.Link` 先 `sem.acquire` 再 `raw.Link`；`TestVaultSemaphoreBeforeLink` 断言拒绝时 `linkCalls` 不增 |
| U4 | 锁外写状态 / 单飞矛盾 | **关闭（方案 a）** | 删单飞；`refreshURL/ensureFreshURL/Close` 全部在 `mu` 内；文件头注释明确「串行契约，ServeContent 单 goroutine」 |
| P1 | 200 不重试、注释矛盾 | **关闭** | 200 → `errIgnoredRange` → 第 1 次换签名重试；第 2 次按 I10 分级（≤2 MiB 丢弃 / `ErrRangeNotSupported`） |
| P2 | 416 触发换签名 | **关闭** | 416 → `errRangeInvalid` 哨兵，直接返回 |
| P3 | Header 全量透传 | **关闭** | 白名单 `User-Agent`/`Referer` |
| P4 | 测试缺口 | **关闭** | 补 `TestUpstream403MidBuffer`（缓冲中途 403 恢复）、`TestUpstreamContentRangeMismatch`（错位重试） |
| P5 | 200 放弃 keep-alive | **关闭** | `discardServe` 注释说明 |

**结论：9 条全部实质闭环，测试与契约一一对应。**

---

## 2. 新增重要

### I1　206 短 body 未区分「正常 EOF」与「传输截断」——截断不重试，且错误信息误导

- **位置**：`upstream.go` / `doRange` 206 分支 + `fetchRange`。
- **事实**：206 分支用 `io.ReadAll(LimitReader(body, length+1))` 取数据，随后
  `n := copy(p, data); if n < len(p) { return n, io.EOF }`。当 CDN 中途断连（body 提前结束）时，`ReadAll` 返回**短数据且 nil 错误**，于是被当成「正常到达末尾」：`fetchRange` 视为成功直接返回，不触发换签名重试；`DecryptReader.loadChunk` 见 `n != cLen`，报 `"密文块不完整（文件被截断）"`。
- **影响**：弱网/长连接中断时，播放表现为「文件损坏」而非「自动重试一次后恢复」；用户与日志都会误判为数据损坏（实际是传输问题）。
- **要求**：解析 `Content-Range: bytes start-end/total`，区分两种短读——
  - `end+1 >= total`（确实到文件尾）→ 返回 `(n, io.EOF)`；
  - `end+1 < total` 且 `end < off+length-1`（传输截断）→ 返回错误，走换签名重试；
  并补一条「206 短 body 且未到文件尾 → 重试成功」的用例。

### I2　416 与 EOF 语义不统一：`off` 恰等于文件长度时报错而非 EOF

- **位置**：`upstream.go` / `doRange` 的 416 分支（直接返回 `errRangeInvalid`）。
- **事实**：按 HTTP 语义，`Range: bytes=N-` 且 `N == 文件长度` 时服务端返回 **416**（`Content-Range: bytes */N`）。上游代码把它当异常，而 `io.ReaderAt` 语义要求 `off >= size` 返回 **EOF**。
- **可触发路径**：`loadRoot` 的 manifest 读取是 `rangeReadCloser` + `io.ReadAll`。当 manifest 密文大小恰为 512×2^k（512/1024/2048/4096…，即恰好读满 ReadAll 的当前缓冲）时，下一次 Read 的起点正好等于文件长度 → 416 → `LoadManifest` 失败 → **该 vault 根退化为空目录 + 告警**。播放路径（按块读、块长恰好覆盖）不受影响。
- **要求**：416 分支解析 `Content-Range: bytes */N`：若 `off >= N` → 返回 `(0, io.EOF)`；无法解析或 `off < N` 才报错。同步调整 `TestUpstream416` 的预期（改为「越界读到 EOF」）。
- **说明**：这是本次「EOF 语义」重点里唯一能造成**启动级**故障的点，虽然触发条件是特定文件大小，但失败面是「整棵树变空」，建议修。

### I3　百度挂载的 Vault 播放必然失败——是「未实现」，不是「未验证」

- **位置**：`vault.go` / `Link` 的 `link.Reader != nil` 分支（要求 `io.ReaderAt`，否则报 `"上游数据流不支持随机读取"`）。
- **事实**：
  - 百度驱动 `Link` 返回 **代理数据流**（`resp.Body`，不可 seek、非 `ReaderAt`），既无 URL 也无 `ReaderAt`；
  - 因此 **manifest 能加载**（顺序读 body 成功），**虚拟树可见**，但**任何文件 GET 都 500**。
- **影响**：PM 记录「百度站未真机验证」——实际上该项不是「待验证」而是「未实现」：百度挂载上 Vault 呈现「目录能看、点了播不了」的半可用状态，容易被误判为 bug。
- **要求**：二选一并在文档中写明——
  - **(a)** Phase 1 范围明确排除百度（README/验收清单注明「百度 Vault 播放 Phase 2 实现 dlink 取数」），且错误文案改为「该驱动暂不支持 Vault 随机读取（Phase 2）」并把错误映射为 502 而非 500；
  - **(b)** 实现百度 `dlink` 取数（本属 Phase 1 计划中的「百度 OpenRange/dlink 缓存」），使 `link.Reader == nil && link.URL != ""` 成立。

### I4　配置自愈会静默覆盖用户正在编辑的 config.json

- **位置**：`internal/conf/config.go` / `Load` 的自愈分支（`RestoreBackup` → `LoadStrict` 重读）。
- **事实**：`RestoreBackup` 直接把 `.bak` 写回 `config.json`，**损坏的原文件没有留副本**，仅一条 `slog.Warn`。用户（PM）手工加 vault 段写坏逗号后重启，编辑内容被上一版覆盖，界面上看不出任何异常，只在日志里有一行 WARN。
- **更坏的边界**：若 `.bak` 也损坏，流程是「先覆盖 config.json → 再 LoadStrict 失败 → 返回**原始错误**」。此时：① 用户原文件已消失；② config.json 变成 `.bak` 的垃圾内容；③ 报错信息指向**已不存在的内容**，排查会彻底跑偏。
- **要求**：
  1. 恢复前把损坏文件另存为 `config.json.broken-<时间戳>`（并保留 0600），日志给 **ERROR** + 该路径；
  2. **先校验 `.bak` 可解析**（读入内存 `Unmarshal` 到临时结构），通过后再原子替换，避免「覆盖后才发现备份也坏」；
  3. 恢复成功的日志附 `.bak` 的 mtime 与恢复后的挂载数量，便于判断回退到了哪个版本。

### I5　自愈会「复活」旧凭证，需在日志与文档中明示

- **事实**：`.bak` 是管理页每次写入前的副本，可能包含**已过期的移动 authorization** 或旧密码。自愈后服务可用、但挂载可能立刻显示「已过期」，用户会以为是「昨天更新没生效」。
- **要求**：① 自愈日志明确「已回退到 <mtime> 的备份，其中凭证可能已过期，请在 /admin 核对或更新」；② `design §3.3` / README 写明「自愈仅针对 **JSON 语法错误**；语义错误（空 clouds、key_file 不存在等）仍拒绝启动」，避免把自愈理解成「任何配置问题都能自己好」。

---

## 3. 建议

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | `loadRoot` 对 URL 驱动调用两次 `Link`（先探测 Reader/URL，再 `NewUpstream` 内部再 Link） | 启动时每根多一次云盘 API 调用；可在 `NewUpstream` 增加「接受已获取的 link」重载，或先判驱动能力再决定探测方式 |
| P2 | `ErrUpstream` 的 HTTP 映射 | design §7.7 约定上游失败 → **502**；本轮 diff 未见 `webdav` 新增该映射（此前只有 403/503/500）。请核对：若未映射则上游失败返回 500，与契约不符 |
| P3 | 绑定校验放行空路径 | `if p := dr.OriginalPath(); p != "" && p != inner` —— `metadata.p` 为空时**跳过**校验。`mcrypt` 恒写 `p`，建议空路径一律拒绝（fail-closed），避免老对象/异常对象绕过 I3 |
| P4 | `httpClient` 全局 30s 超时 | 4 MiB 窗口在 1 Mbps 链路上需约 32 s，必然超时并触发无谓重试；建议按窗口大小给足余量（如 60–90 s）或改成「连接超时 + 读空闲超时」而非总超时 |
| P5 | 行为变更需记录 | Phase 0 时 URL 型驱动**跳过** vault 根（物理目录可见）；现在改为「读 manifest 成功即接管，失败则空目录 + 告警」。即天翼/移动上**同名普通目录会被隐藏**。这是 design §4.3 已拍板的边界语义，但触发面从 local 扩到云盘，建议写进 README「已知限制」与验收清单 |
| P6 | `rangeReadCloser` 无尺寸保护 | 依赖外层 `LimitReader(32 MiB+1)`；若将来换调用方，可能与 I2 的 416 行为叠加。建议在 `Read` 里对 `off` 做上限保护或注释说明依赖 |

---

## 4. 已核实成立（本轮重点）

| # | 项 | 结论 |
|---|---|---|
| 1 | 信号量与回滚的资源释放 | `Vault.Link` 用 `release` 标志 + `defer`：`raw.Link` 失败、`ReaderAt` 不满足、`NewUpstream` 失败、`OpenDecryptReader` 失败、绑定校验失败**五条错误路径各释放一次**；成功路径 `release=false` 交由 `semDecryptReader.Close`（`sync.Once`）释放，无双重释放 |
| 2 | 绑定校验的失败关闭 | 校验放在 `OpenDecryptReader` 成功之后、返回 `model.Link` 之前；不一致即 `dr.Close()` + 报错，调用方拿不到任何解密流 |
| 3 | EOF 在 DecryptReader 侧的语义 | `loadChunk` 允许 `io.EOF` 但要求 `n == cLen`；`Upstream` 在「数据不足以填满请求」时返回 `(n, io.EOF)`，二者一致；末块读取路径正确 |
| 4 | 缓冲与解密层的边界 | 窗口 4 MiB 覆盖 1 MiB+16 的整块；`readOnce` 完整覆盖判定 + `ReadAt` 循环补齐，块读不再跨窗口短读 |
| 5 | 部署与工具链 | `deploy-prepare.sh` 增 Windows `mcrypt.exe` 交叉编译；内置 27 类 MIME 表优先于系统表（解决 `.ts` 被系统表误判）——真机问题驱动的合理修复 |
| 6 | 版本与发布 | `version = v1.1.0-dev`，README 变更记录留闸门⑥，与发布策略一致 |

---

## 5. 放行意见

| 项 | 结论 |
|---|---|
| 闸门③ | **有条件通过**：RV-07 全部闭环，接线正确、无资源泄漏；I1/I2 属本次「EOF 语义」重点，建议 Phase 1 收尾前修（均为局部改动） |
| I3（百度） | 需 PM 拍板范围：明确排除并改文案，或排入 Phase 2；**不要**在验收清单里继续写「百度站待验证」——按当前代码它必然失败 |
| I4/I5（配置自愈） | 建议尽快（数据安全类）：补 broken 副本 + 先验后写 + 日志明示旧凭证风险；改完补两条用例（损坏+备份坏、损坏+备份好且原文件被保留） |
| 真机证据 | 天翼家庭站 10+ 次拖动无感知延迟、播放中 CPU 5.2% —— 支持「取数层 + 4 MiB 窗口」的判断；但未覆盖 55 GB 连续播放与弱网截断（I1 场景） |

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 206 短 body 未区分截断与 EOF，截断不重试 | | | 待处理 | | |
| I2 | 重要 | 416 语义与 EOF 不统一（manifest 恰为 512×2^k 时启动失败） | | | 待处理 | | |
| I3 | 重要 | 百度挂载 Vault 播放必然失败（未实现而非未验证） | | | 待处理 | | |
| I4 | 重要 | 配置自愈静默覆盖用户编辑、无损坏副本；备份坏时先覆盖后失败 | | | 待处理 | | |
| I5 | 重要 | 自愈复活旧凭证，日志与文档需明示 | | | 待处理 | | |
| P1 | 建议 | loadRoot 对 URL 驱动两次 Link | | | 待处理 | | |
| P2 | 建议 | ErrUpstream 未映射 502（需核对 webdav） | | | 待处理 | | |
| P3 | 建议 | 绑定校验放行空 metadata.p | | | 待处理 | | |
| P4 | 建议 | httpClient 30s 总超时对弱网偏紧 | | | 待处理 | | |
| P5 | 建议 | URL 驱动接管同名普通目录属行为变更，需记录 | | | 待处理 | | |
| P6 | 建议 | rangeReadCloser 依赖外层限长，建议注释/保护 | | | 待处理 | | |

---

*本报告基于 `f8a5cc0` 的静态阅读，未编译、未执行、未接触真实凭证；真机性能与播放结论以 PM 记录为准。*
