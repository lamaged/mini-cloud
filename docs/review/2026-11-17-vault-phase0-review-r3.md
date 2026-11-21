# 第六轮复核：Vault Phase 0 接线与验收（RV-2026-11-17-04）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-17-04 |
| 评审类型 | P7–P10 处置复核 + Vault 包装层/接线首轮审核（含安全与契约一致性） |
| 评审对象 | `feature/file-encryption` @ `4d18118692c66e43c717bdd3a5c10c235ccb305d`（`e381223` 文档/健壮性、`d324c305` 接线、`5862a89`/`4d18118` 记录） |
| 对比基线 | `160ed35`（RV-03 审核对象） |
| 评审方式 | 逐文件静态阅读（`internal/vault/vault.go`、`vault_test.go`、`internal/conf`、`internal/model`、`webdav/prop.go`、`webdav.go`、`main.go`、`drivers/local/driver.go`，对照 `e381223`/`d324c305` 全量 diff）；**未编译、未执行**；AX6 性能与端到端结论以提交说明为准 |
| 结论 | **通过（有条件）**：P7–P10 全部闭环，Phase 0 代码链路完整、核心契约（`_mc` 屏蔽、路径分流、断言边界、Stop 所有权）均已落地；新增 4 条重要（I1–I4）与 8 条建议，其中 **I1 是启用 Vault 后会误伤天翼/移动挂载的操作性风险**，须在 Phase 1 前处置或明确告知 |

---

## 1. P7–P10 处置复核

| 编号 | 意见 | 结论 | 证据（e381223） |
|---|---|---|---|
| P7 | 报告版本号四处不一致 | **基本关闭，但引入一处新缺陷（见 I4）** | 头部改 `v1.3`；版本表补 v1.3 行；审阅表补 RV-17-02/03 两行；页脚改「v1.3 修订」；§7.3 示例 `"h": ""` 并补废弃注记 |
| P8 | PROGRESS 进行中/当前版本滞后 | **关闭** | 当前版本收敛为「代码／文档」两行（v0.10.0 已发布 + Phase 0 库层完成）；进行中改为「库层完成，待接线」，与事实一致 |
| P9 | Phase 1 顺序流式契约缺细节 | **关闭** | `design §4.1` 增 4 条口径：缓冲 4–8 MiB、相邻偏移差 ≤ 窗口视为顺序、反向 Seek/跨块跳跃/403 重签即失效、缓冲计入 max_streams 同一账 |
| P10 | `meta.S` 失约束；失败残留孤儿对象 | **关闭** | `EncryptObject` 在 `SealMeta` 之前 `meta.S = size`；`cmdEncrypt` 加密失败 `os.Remove(objFull)` 并在错误信息中注明 |

---

## 2. Phase 0 接线首轮审核（`d324c305`）

### 2.1 已核实成立（无需改动）

| # | 项 | 结论 |
|---|---|---|
| 1 | 路径分流（I11/D1） | 命中 vault 根 → `List` 读内存虚拟树、`Link` 走 `raw`；未命中 → 全部委托 `cached`（302 行为不变）。`findRoot` 按首段名匹配、大小写敏感，`inner` 归一化正确（`/backup` → `/`） |
| 2 | `_mc` 屏蔽（I4） | `List`/`Link` 均先判 `isMcPath` 返回 `driver.ErrForbidden`；`webdav` 两处入口（`handleGetHead`、`handlePropfind` 的 `getObj`）映射 403；`getObj` 在父目录列表中找不到 `_mc` 时返回 404（「隐藏」语义成立），不回落 302 |
| 3 | 断言边界（铁律 7） | `Vault` 仅实现 `Driver`（`Name`/`Init`/`List`/`Link`）+ `Stop` + 自定义 `Warning()`；**未**实现 `TokenManager`/`HealthReporter`/`CredentialUpdater`；`main` 对 raw 驱动做凭证断言、对 Vault 单独取 `Warning` |
| 4 | Stop 所有权 | `Vault.Stop` 以 `sync.Once` 下传 `cached.Stop()`；`main` 只经 `Handler.Close()` 触发，无双入口 |
| 5 | 降级语义 | manifest 缺失/解密失败 → 该根为空目录 + `Warning`，`Init` 不返回错误、服务不崩溃（design §4.3） |
| 6 | 信号量 | 全局 `streamSem`，超时轮询后拒绝；`semDecryptReader.Close` 释放；测试覆盖「第二路被拒 + 关闭后可重开」 |
| 7 | ETag/MIME 唯一来源（D3，PROPFIND 侧） | `model.Obj` 增 `ETag`/`ContentType` 可选字段，`buildPropsXML` 非空优先，零值回退原规则（非 vault 路径零回归） |
| 8 | 上游 Header 不透传（D3） | `Vault.Link` 自建 `Header`（仅 `Content-Type`），丢弃 local 驱动自带的 `Content-Type`，符合「禁止透传」 |
| 9 | local 驱动可满足 `io.ReaderAt` | `local.Link` 返回 `*os.File`，`ReadAt`/`Close` 均满足，Phase 0 的「解密 Reader 接本地文件」链路成立 |
| 10 | 测试覆盖 | 虚拟列表（原始名/ETag/MIME）、明文解密、`_mc` 403（含对象与 manifest 两条路径）、非 vault 委托、manifest 缺失空目录+告警、信号量限流，共 6 组 |

### 2.2 新增重要（I1–I4）

#### I1　Vault 对所有挂载无条件装配，Phase 0 仅 local 可用 → 会误伤天翼/移动的同名目录

- **位置**：`main.go` 装配循环（`if vaultCfg != nil` 对每个 `mount` 统一包装）；`vault.loadRoot` 的 `link.Reader == nil` 分支。
- **事实**：
  1. 天翼/移动驱动的 `Link` 返回 **URL**（302 模式），`link.Reader == nil`；`loadRoot` 因此判定「manifest 缺失或不可读」，把该根建成空树并置 `r.warning`。
  2. 该挂载上真实存在的 `backup` 目录会被虚拟树接管——**目录内容变为空**（`List` 命中 vault 根后不再委托 `cached`）。
  3. `main.displayName` 用 `VaultWarning` 覆盖挂载显示名，根导航页上「天翼个人」会变成一段告警长文本。
- **影响**：PM 在真实 config（含天翼/移动挂载）里把 `vault.enabled` 置 true，天翼/移动的 `backup` 目录立刻「消失」且挂载名变形。虽然 design §4.3 已拍板「同名普通文件夹显示为空目录、不受保护」，但**告警原因写错**（实为驱动不支持流式，而非 manifest 缺失），会把用户引向错误的排查方向。
- **要求**（三选一，建议 a+c）：
  - **(a)** 装配前探测能力：仅当 `raw.Link(任意对象)` 能返回 `io.ReaderAt`（或驱动声明支持 `OpenRange`）时装配 Vault；否则跳过并记 INFO 日志。
  - **(b)** 配置层白名单：`vault.paths` 之外增加按驱动/挂载的启用列表（需 PM 拍板，涉及配置语义变更）。
  - **(c)** 至少区分告警文案与呈现位置：`link.Reader == nil` → 「该驱动不支持流式读取（Phase 1 支持）」；且 **Vault 告警不要覆盖挂载显示名**（进 `/admin` 或日志），避免根页面变形。

#### I2　`max_streams` 超限返回 500，与 design 的 503 不符；注释与实现互相矛盾

- **位置**：`vault.Link`（`sem.acquire` 失败返回 `fmt.Errorf`）；`comment`「超限不排队，直接拒绝」；`streamSem.acquire`（轮询 `100ms` 至 `5s`）。
- **事实**：`webdav.handleGetHead` 对非 `ErrForbidden` 的 `Link` 错误统一返回 **500**；而 design §7.7 与报告 §7.7 明确「超出返回 **503** 防止 OOM」。同时注释写「不排队，直接拒绝」，实现却是「等 5 秒再拒」——二者必有一处错。
- **要求**：新增哨兵（如 `driver.ErrTooManyStreams`）→ `webdav` 映射 503；注释改为「最多等待 `acquireTimeout`，超时返回 503」。若 PM 认可「直接拒绝」（0 等待），则删掉轮询逻辑。

#### I3　ETag 只在 PROPFIND 侧落地，GET 响应无 ETag（D3 未完全闭环）

- **位置**：`Vault.Link` 的 `Header` 仅含 `Content-Type`；`prop.buildPropsXML` 的 `getetag` = `"<fileID>-<size>"`。
- **影响**：客户端在 PROPFIND 拿到 ETag 后若用 `If-Range`/`If-None-Match`（Infuse、rclone 等严格客户端常见），GET 侧没有对应 ETag → 条件请求退化为全量 200，且「ETag 唯一来源」只对 PROPFIND 成立。
- **要求**：`Vault.Link` 的 `Header` 增加 `ETag: "<fileID>-<size>"`（与虚拟树同一字符串），使 PROPFIND 与 GET 同源；`Last-Modified` 已由 `DecryptReader.Stat` 提供，无需重复。

#### I4　报告 §7.3 新增孤立代码围栏，导致后续章节被渲染为代码块

- **位置**：`docs/vault-feasibility.md` §7.3「元数据」小节（`e381223` 的 P7 修订）。
- **事实**：修订在 JSON 示例的**闭合围栏之后**又插入一行 ```` ``` ````，形成「关闭→注记→再开启」的结构；其后「保存原始路径…」「v1.3 修订」注记、格式不变量等内容会被当作代码块渲染，直到下一个围栏才闭合。
- **影响**：§7.3 是格式规范章节，渲染错位会连带后续章节排版错乱；作为外部审核的基线文档，结构完整性应保证。
- **要求**：删除该多余围栏（或把注记移到围栏内的注释行），并在提交前用 Markdown 预览自查一次全部文档的围栏配对。

---

## 3. 建议（P11–P18）

| 编号 | 内容 | 建议 |
|---|---|---|
| P11 | `VaultConfig` 用 struct 值 + `omitempty`（无效） | 与 D5-4 同一类问题：`json:"vault,omitempty"` 对 struct 值不生效，管理页写回 config.json 时会插入 `"vault":{"enabled":false,"key_file":"","paths":null,"max_streams":0}`，正是当初要避免的「用户误判配置损坏」。建议改 `*VaultConfig` 指针（与 `Cloud` 一致），或为 `VaultConfig` 实现 `IsZero` 并在 `Save` 前置空 |
| P12 | manifest 无重载 | 报告 §6.4 仍写「服务端启动/定时加载」「新增文件最多延迟几分钟可见」，实现只有 `Init` 一次。建议 Phase 0 文档改为「需重启生效」，把定时重载列入 Phase 2（或补 `SIGHUP` 重载） |
| P13 | `vault.New` 签名与文档不一致 | 实现为 `New(cached, raw, master, cfg)`（4 参），`design §4.1` 与代码注释仍称 `vault.New(cached, raw, vaultCfg)`（3 参）。三者统一（建议以代码为准改文档） |
| P14 | 配置示例含未实现字段 | 报告 §7.8 示例的 `manifest`/`chunk_size`/`cache_mb` 未被 `VaultConfig` 解析（会被 JSON 静默忽略）；建议标注「Phase 2 预留」或删除；`paths` 为空数组时 `main` 已 `os.Exit(1)`，示例可保留非空 |
| P15 | `List` 全表扫描 + `isDir` 每请求一次 | `List` 遍历全部 `entries`+`dirs`；`webdav.handleGetHead` 每次 GET 先调 `isDir` → `List(parent)`。1 万文件下每次请求约万级遍历，Phase 0 可接受，Phase 1 建议在 `loadRoot` 时建 `dir → children` 索引，避免每次请求线性扫描 |
| P16 | 信号量细节 | ① `semDecryptReader.Close` 无 `sync.Once`，重复 Close 会多 release（当前调用链只 Close 一次，属防御项）；② `acquire` 不响应 `ctx` 取消，客户端断开后仍等满超时；③ 信号量在 `raw.Link` 之后获取，超限拒绝时已白开一次上游连接（Phase 1 应前置到 Link 之前） |
| P17 | 验收口径需写清 | PROGRESS 记「验收清单全部闭环」，但报告 §9 Phase 0 含「用 55 GB 文件走一遍代理，测 4K 播放/拖动/`top`/`free`」——AX6 可用存储仅数 MB，该项不可能在 AX6 本地完成；建议注明执行环境（开发机/AX6）与未覆盖项。另 AX6 `Seal 384.6 / Open 383.8 MB/s` 与 OpenSSL 1.0.2q 基线（136 MB/s）差 2.8 倍，建议在 PROGRESS 记录 bench 口径（单线程、1 MiB 块、无 I/O、是否含内存拷贝），以免后续引用歧义 |
| P18 | 防御性校验 | ① `e.Obj` 未校验前缀（`_mc/o/`），建议 `path.Clean` 后强制前缀，避免未来 manifest 异常时把任意路径交给驱动；② manifest 路径硬编码 `_mc/manifest.mcf`（配置示例却含 `manifest` 字段），二者取一并写清 |

---

## 4. 放行意见

| 项 | 结论 |
|---|---|
| 闸门③（Vault 阶段） | **通过（有条件）**：无阻断项；I1 需在启用 Vault 于真实 config 之前处置（或明确告知「Phase 0 只对 local 生效」），I2/I3 可与 Phase 1 开工前一并修，I4 建议立即修（文档结构） |
| Phase 0 验收 | 依赖 local 的链路（`_mc` 403、HTTP 206、随机 Seek、SHA-256、空文件/篡改拒绝、fileID 一致性）已具备执行条件；建议按 P17 补记执行环境与未覆盖项，避免「全部闭环」被误读 |
| Phase 1 开工前 | I1（能力探测/白名单）、I2（503）、I3（GET ETag）、P15（children 索引）、P16③（信号量前置）应一并纳入设计；P9 契约已就位，可对齐实现 |

---

## 5. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | Vault 无条件装配，天翼/移动同名目录被隐藏且告警文案有误 | | | 待处理 | | |
| I2 | 重要 | max_streams 超限返回 500（应为 503）；注释与实现矛盾 | | | 待处理 | | |
| I3 | 重要 | GET 无 ETag，与 PROPFIND 不同源（D3 未闭环） | | | 待处理 | | |
| I4 | 重要 | 报告 §7.3 孤立代码围栏导致渲染错位 | | | 待处理 | | |
| P11 | 建议 | `VaultConfig` struct 值 omitempty 无效，写回会插入空 vault 块 | | | 待处理 | | |
| P12 | 建议 | manifest 无重载，文档仍写「定时加载」 | | | 待处理 | | |
| P13 | 建议 | `vault.New` 签名与文档不一致 | | | 待处理 | | |
| P14 | 建议 | 配置示例含未实现字段 | | | 待处理 | | |
| P15 | 建议 | `List` 线性扫描 + `isDir` 每请求触发 | | | 待处理 | | |
| P16 | 建议 | 信号量 Close 幂等/ctx 取消/获取时机 | | | 待处理 | | |
| P17 | 建议 | 验收口径（55 GB 用例环境、bench 口径）需写清 | | | 待处理 | | |
| P18 | 建议 | `e.Obj` 前缀校验、manifest 路径硬编码 | | | 待处理 | | |

---

*本报告基于 `4d18118` 的静态阅读，未编译、未执行、未接触真实凭证；AX6 性能数字与端到端结论以提交说明为准，未独立复核。*
