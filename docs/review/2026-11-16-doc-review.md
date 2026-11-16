# 文档评审报告：v0.10.0 设计草案与 Vault 可行性报告

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-16-01 |
| 评审类型 | 文档评审（闸门① 设计评审之前置评审）；不含代码审核、不含性能实测 |
| 评审对象 | `README.md`、`CLAUDE.md`、`PROGRESS.md`、`TODO.md`、`docs/design.md`、`docs/process.md`、`个人文件夹功能-测试总结.md` |
| 基线 | `feature/file-encryption` @ `aeb928e1f41a12712aeea4a12cb1e6a76f589f7b` |
| 评审日期 | 2026-11-16 |
| 评审方式 | 七份文档全文通读；文档中引用的代码片段与当前仓库代码交叉核对（静态阅读，未编译、未执行、未使用真实凭证） |
| 结论 | **有条件通过**：文档结构、证据链与流程框架成立，闸门① 具备可评审性；须先完成 D1–D5 修订，方可提交 PM 批准并进入实现 |
| 意见分级 | **D** = 阻断（文档与代码契约冲突，不修订将导致返工）／**I** = 重要（安全与实现细节缺口）／**P** = 流程与文档体系 |

> 处置要求：按 `docs/process.md` §2.3，程序员须将每条意见归入「阻断／补充／可选／待拍板／驳回」五类，用交易语言向 PM 汇报后再动代码；处置结果回填 §8 表格并同步 `PROGRESS.md`。

---

## 1. 结论

1. **文档体系本身是合格的**：`docs/process.md` 的角色边界、9 条铁律、6 道闸门与审核闭环相互闭合；`docs/design.md` 具备目标、接口、流程、安全、验收标准与「不改动范围」，满足其自身定义的闸门① 四项检查。
2. **可行性报告的证据质量高于同类文档**：明确区分官方文档／抓包／第三方源码／httptest 四层证据，并主动标注「未实测」项（天翼个人 Go UA、百度 Range），不制造全绿结论。
3. **主要问题集中在「文档对现有代码行为的假设」**：5 处阻断级意见全部属于「文档写了一个现有架构做不到或会做错的契约」，与产品目标无关，修订文档即可，不涉及方案否决。
4. **Vault 的密码学格式有三处需要在写代码前定稿**（nonce/fileID 不变量、空文件与头部认证、manifest 与对象绑定），否则 Phase 0 的「SHA-256 一致」验收通过也不等于格式安全。

---

## 2. 评审范围与边界

### 2.1 已核对项

- 文档间的交叉引用编号（`design.md` §4.2 引用报告 §9、§5.4 引用报告 §5.4 等）——**编号存在且内容对得上**。
- 文档引用的代码断言（10 分钟链接缓存、HEAD 对 URL 类型文件返回 200、`conf.Save` 的 0644、`Handler.Close()` 的类型断言、`main.go` 的包装顺序与 `auth.StartWatcher` 传原始驱动）——**均与代码一致**，详见附录 A。
- 数据的算术自洽性（136,306.69 kB/s ≈ 136 MB/s ≈ 1.09 Gbps；55 GB／3 小时 ≈ 41 Mbps；55 GB 分块开销 ≈ 824 KiB < 0.002%；文件头 64 B 字段偏移累加 = 64）——**计算无误**。

### 2.2 未覆盖项（不作为本次结论依据）

- 未编译、未运行 `go vet` / `go test -race`；未在 AX6 或开发机复测任何性能数字。
- 未验证报告引用的**具体行号**（`webdav.go#L80`、`cache.go#L135`、`driver.go#L252` 等）。本轮拉取的是网页视图，不含行号，建议按 P6 统一改为「文件 + 符号」引用。
- 未评审 `drivers/*` 的实现细节（属闸门③ 代码审核范围）。

---

## 3. 阻断级意见（D1–D5）

> 判定标准：文档所写的契约，现有代码不支持、或按文档实现会产生错误行为。**不修订不得开工。**

### D1　报告的 URL 缓存策略在现有架构下无法实现

- **文档位置**：报告 §5.4 第 3 条「URL 缓存 TTL：`min(10min, expires - now - 60s)`」、§7.1 `internal/vault/upstream.go`；`design.md` §4.1 包装顺序。
- **代码事实**：Vault 位于 `CachedReader` **之上**（`design.md` §4.1、报告 §8.2 的包装顺序一致）。`internal/fs/cache.go` 的 `CachedReader.Link` 固定写入 `expiresAt: time.Now().Add(c.linkTTL)`，`linkTTL` 为 10 分钟常量，**不解析也不感知签名 URL 的过期时间**，且没有「按条目指定 TTL」的扩展点。Vault 无法通过现有接口把缓存 TTL 压到 `expires-60s`。
- **要求**：文档二选一并写明职责归属——
  - **(a) 推荐**：Vault 直接调用 `raw.Link`，自建「URL + 绝对过期时间」表，缓存 TTL 由自身计算；`CachedReader` 在 Vault 路径上不承担 URL 缓存。
  - **(b)** 给 `CachedReader` 增加可选扩展点（如 `LinkWithTTL` 或让驱动返回过期时间），并在 `design.md` 中把该改动列为「修改清单」。
  - 无论选哪种，403 刷新路径必须指明失效手段：现有 `CachedReader.InvalidateLink(path)` 可用，但 Vault 需以可选接口（`LinkInvalidator`）断言，不得依赖具体类型。

### D2　「config.json 改为 0600」这一安全目标不成立

- **文档位置**：`design.md` §3.4 第 4 条、§3.5。
- **代码事实**：`internal/conf/config.go` 的 `Save` 调用 `os.WriteFile(path, data, 0644)`。**`os.WriteFile` 的 perm 参数只在创建文件时生效**；路由器上已存在的 `config.json` 仍保持 0644。仅把字面量改成 0600，对存量设备零效果。
- **要求**：设计明确写为「写入后显式 `os.Chmod(path, 0600)`（`config.json.bak` 同样处理）」，并在闸门⑤ 设备验收增加一条可观察项（真机 `ls -l` 核对 config.json、`.bak`、`vault.key`、`state/*` 四类文件权限）。
- **附带**：`state/*_token.json` 现状已是 0600（`drivers/baidu/auth.go` 的 `SaveToken`），文档不必再列，但可保留为回归项。

### D3　报告 §7.6「响应头重写」与 PROPFIND 的实际生成路径不一致

- **文档位置**：报告 §7.6 全表（ETag、`Last-Modified`、`Content-Type`）；`design.md` §4.2 Phase 1。
- **代码事实**：
  1. `internal/webdav/prop.go` 的 `buildPropsXML` **完全由 `model.Obj`** 生成 `getcontentlength`、`getcontenttype`（按扩展名 `mime.TypeByExtension`）、`getetag`（`ModTime + Size`）、`getlastmodified`，与 `model.Link.Header` 无关。
  2. GET 路径 `internal/webdav/webdav.go` 的 `serveContent` 先把 `link.Header` 逐条 `Add` 进响应，再交给 `http.ServeContent`；后者对同名头使用 `Set` 覆盖。
- **后果**：PROPFIND 的 ETag/MIME 与 GET 的 ETag/MIME 可能来自两个不同来源。严格客户端（Infuse、rclone、Windows 重定向器）缓存与 `If-Range` 行为会异常；`Content-Encoding`/`Accept-Ranges` 等头还会从上游残留。
- **要求**：
  - 文档定义 **ETag 与 MIME 的唯一来源**（建议：Vault 虚拟树提供，`model.Obj` 增可选字段或新增可选接口，与 `TokenManager`/`HealthReporter` 同模式），并明确 `prop.go` 的改造属于「修改清单」。
  - 明确 Vault **自行构造** `Link.Header`，**禁止**透传上游的 `Content-Length`、`Content-Range`、`Content-Encoding`、`ETag`。

### D4　`vault.New(..., raw.Name())` 传的是驱动类型名，不是挂载名

- **文档位置**：报告 §8.2 包装顺序；`design.md` §4.1 示例。
- **代码事实**：各驱动 `Name()` 返回**类型名**（`drivers/baidu/driver.go` 为 `"baidu"`），而挂载名来自 `conf.CloudConfig.MountName()`（`main.go` 中 `tianyi.New(cc.MountName(), ...)`）。同类型多实例（天翼个人 + 天翼家庭）会拿到相同名字。
- **要求**：
  - 三处一并修正：报告 §8.2、`design.md` §4.1 的示例，统一传 `cc.MountName()`（或其等价物）。
  - `design.md` §4.1 示例另有 3 处缺陷：`drv := driver.Driver = cached` **语法错误**；`v.Init(ctx)` 的 error 未处理；`vault.New` 参数个数与报告 §8.2 **不一致**（2 个 vs 3 个）。示例必须与最终签名逐字一致，或直接改为「签名见报告 §8.2」的唯一出处。
  - 示例需补 `defer`/`Close` 归属（与 I7 合并考虑）。

### D5　凭证写回的提交顺序与并发模型未定义

- **文档位置**：`design.md` §3.3 步骤 3（main 写 config）、§3.4 第 1 条（驱动 `saveTokenState`）、§3.5。
- **事实与风险**：同一份凭证存在三处状态——驱动内存、`state/<挂载名>_token.json`、`config.json`。按现文档顺序，可能出现「驱动验证通过 → token state 已落盘 → config 写入失败」的窗口：重启后读旧 config + 新 token，只能靠 v0.9.1 的「取较新者」兜底；若新 token 已过期则回退无效。此外 `main.go` 对每个请求调用 `checkAuth(r, cfg.Server.Username, cfg.Server.Password)`，管理页若原地修改 `cfg`，`-race` 会报数据竞争。
- **要求**：文档写明——
  1. **提交顺序**：验证（带超时）→ 写 config（原子写 + fsync + `.bak`，含 D2 的 chmod）→ 原子切换驱动内存凭证 → 写 token state；任一步失败的回滚点与对外错误码。
  2. **并发模型**：`cfg` 由 mutex 保护或 copy-on-write；管理页写入不得与请求期读取共享可写状态；`checkAuth` 的读取路径要说明是否受锁。
  3. **超时**：验证请求必须带 `context.WithTimeout`（建议 10s），避免管理页 POST 挂死。
  4. **配置写回的副作用**：`conf.Save` 会输出 `Config.Cloud`（旧版单数字段，无 `omitempty`）的空对象，首次经管理页写回后 config.json 会多出一个空的 `"cloud": {…}` 块；同时旧版单数配置会被归一化为 `clouds`。文档需说明该行为，或顺手加 `omitempty`，避免用户误判配置损坏。

---

## 4. 重要意见（I1–I7）

### I1　加密格式的 nonce 前提必须写成不变量

- **位置**：报告 §7.3「分块」、§7.5「密钥层级」。
- **分析**：`nonce = 4 B 随机前缀 + 8 B 块序号`，文件密钥为 `HKDF(salt=fileID)`。只要每次加密重新生成 `fileID`，键随之改变，跨文件的 32 bit 前缀碰撞无害（对 1 万文件约 1% 碰撞率，但键不同）。风险在「同一 `fileID` 下重新加密或截断重写」：键不变而前缀可能重复，形成 GCM **(key, nonce) 重用**，安全性直接崩塌。
- **要求**：格式规范写入不变量——**每次加密必须新生成 `fileID`；`repair`/重加密/续写工具不得复用 `fileID`**；工具层增加「同一 fileID 重复加密」的拒绝逻辑与测试用例。

### I2　空文件与头部缺少认证边界

- **位置**：报告 §7.3（`AAD = SHA256(header) + uint64(index)`、`cipher_size` 公式）。
- **分析**：① `plain_size = 0` 时 `chunk_count = 0`，没有任何块的 tag 覆盖 header，头部可被任意篡改而不被发现；② 头部字段 `chunk_size`、`meta_len`、`data_offset` 无取值校验，恶意文件可诱导超大内存分配（路由器上即 OOM/DoS）。
- **要求**：空文件用 meta 密钥对头部做一次 GCM 封装（或由 manifest 记录 header 哈希）；明确校验规则：`chunk_size ∈ [64 KiB, 1 MiB]`、`data_offset == 64 + meta_len`、`meta_len` 上限、`plain_size >= 0`，校验失败一律拒绝而非夹取。

### I3　manifest 只保证自身完整，不能防「跨对象替换」

- **位置**：报告 §6.3 威胁表（仅列「云端回滚旧版本」）、§7.3「manifest」。
- **分析**：manifest 的 GCM 只覆盖自身。云盘可将 a.mkv 的 manifest 项指向 b 的对象文件——解密时使用的键来自 b 对象头内的 `fileID`，解密成功，用户看到的是 b 的内容顶着 a 的名字。这是**内容替换**，与回滚是两类攻击。
- **要求**：格式中写入两条绑定校验：manifest 记录的 `fileID` 与对象 header 内 `fileID` 必须一致；chunk AAD 或加密 metadata 中绑定虚拟路径，服务端读取时比对 `metadata.p` 与请求路径。可同时保留「本地 manifest 哈希固定」作为可选加固。

### I4　`_mc` 目录必须显式屏蔽

- **位置**：报告 §7.3「对象命名」「manifest」、§7.2 请求流程。
- **要求**：Vault 根（如 `/backup`）为 100% 虚拟树后：`PROPFIND` 不得出现 `_mc` 及其子项；`GET /backup/_mc/...` 必须返回 403，**不得回落到 302** 把密文对象直接交给客户端（否则对象大小、对象名可被枚举，且与「Vault 路径只暴露明文视图」的语义冲突）。此条应列为 Phase 0 的最小可用验收项。

### I5　`Link.StatusCode` 的表述与代码不符

- **位置**：`CLAUDE.md`「`Link.StatusCode` 透传上游状态码（如 206）」。
- **代码事实**：`StatusCode` 仅在 `webdav.go` 的 `streamReader`（非 seekable 分支）使用；可 seek 分支由 `http.ServeContent` 自行决定 200/206/416。
- **要求**：改为「`Link.StatusCode` 仅用于非 seekable 数据流；seekable Reader 的状态码由 `ServeContent` 依据 Range 计算」。该表述同时影响 Vault Reader 的实现预期，须在 `design.md` §4.1 呼应。

### I6　上游 HTTP 异常分支未进设计

- **位置**：报告 §7.4、§7.6、§7.7。
- **缺口**：Range 请求返回 **200（CDN 忽略 Range）** 时如何降级；**416**；206 的 `Content-Range` 与请求区间不一致；客户端断开时 `ctx` 是否传导到上游并取消；`Seek` 切换流时旧 `resp.Body` 是否必然关闭；`max_streams` 是全局还是按挂载，超限返回 503 后播放器是否可恢复。
- **要求**：在 §7.4/§7.6 增加「异常分支 → 动作 → 对外状态码」表；明确 `max_streams` 的作用域与计数点（建议全局信号量 + 获取超时，避免 seek 风暴下自锁）；明确并发刷新锁需用 mutex/`Once` 自实现（铁律 6 禁止引入 `golang.org/x/sync/singleflight`）。

### I7　`Stop` 的所有权与幂等性

- **位置**：报告 §8.3「`Vault.Stop` 要向下调用 `CachedReader.Stop`」。
- **代码事实**：`webdav.Handler.Close()` 通过类型断言调用 `Stop()`；`CachedReader.Stop()` 是 `close(c.stopCleanup)`，**二次调用 panic**。
- **要求**：文档写明「Stop 必须 `sync.Once` 幂等；所有权链唯一：main → Handler.Close → Vault.Stop → CachedReader.Stop，任何中间层不得被 main 单独 Stop」。并补一条单元测试要求（重复 Stop 不 panic）。

---

## 5. 流程与文档体系意见（P1–P6）

### P1　流程里没有「文档评审」这一环

- **位置**：`docs/process.md` §2.2（外部审核「每个功能一次」，功能完成后）与 §3 闸门①。
- **问题**：本次评审发生在闸门① 之前，流程无对应入口，审核意见没有法定归档位置。
- **要求**：§3 闸门① 增加「外部文档评审（按 PM 判断是否触发）」，并在 §2.3 五类分类中补「文档缺陷」一类的处理说明；本次评审编号 RV-2026-11-16-01 记入 `PROGRESS.md`。

### P2　`PROGRESS.md` 缺结构化留痕格式

- **问题**：现有自由段落无法回答「第 N 条意见改到哪了／谁复核」。
- **要求**：采用 §8 表格模板（编号｜级别｜摘要｜处置类别｜处置说明｜状态｜复核｜日期），并在「已完成」区改为按闸门组织的表。

### P3　`TODO.md` 缺本次评审衍生的待拍板项

- **要求**：至少补入以下 4 条（需 PM 拍板，程序员不得单方决定）：
  1. v0.10.0 是否包含 HTTPS／反向代理指引。管理页会在局域网以明文提交 `authorization`（等价于账号访问权），当前文档只列了风险未给结论。
  2. manifest 的替换／回滚防护放 Phase 1 还是 Phase 2（I3）。
  3. 既有 `href` 未转义、目录页未 HTML 转义、根 PROPFIND 未 XML 转义（见 P4）是否**单独立项先修**，而不是并入 Vault Phase 0。
  4. `vault.paths`、`max_streams` 的作用域：全局一套还是按挂载各一套（多挂载时是共享 `_mc` 还是各自独立）。

### P4　三处既有转义缺陷被埋进了 Vault 范围，建议拆出

- **代码事实**：
  1. `internal/webdav/prop.go` 的 `buildHref` 直接 `path.Join`，**无百分号编码**——含中文、空格、`#`、`?` 的文件名在严格客户端上会 404（报告 §8.3 已识别，但归入 Vault 阶段）。
  2. `internal/webdav/webdav.go` 的 `serveDirHTML` 把 `obj.Name` **未转义**写入 HTML——文件名 XSS，属现存安全问题。
  3. `main.go` 的 `serveRootPropfind` 把挂载名**未转义**写入 XML——挂载名含 `&` 时根 PROPFIND 直接产出非法 XML。
- **要求**：三条都是**今天就在影响存量用户**的缺陷，与 Vault 无耦合。文档应把它们列为独立修复项（单独提交、单独验收、纳入 v0.10.0 或更早的补丁版本），Vault 文档只保留「依赖已修复」的说明。

### P5　`CLAUDE.md` 的可复现性问题

- **问题**：要求 `source env.sh`，但仓库中无 `env.sh` 且未说明生成方式，外部审核员无法复现构建；同文件的代理 `http://192.168.2.1:7890` 属开发机内网环境，不宜进产品仓库。
- **要求**：提交 `env.example.sh`（或写明创建步骤与 GOROOT/GOPATH 取值）；代理说明移到不入库的本地笔记，仓库内只保留「如需访问外部网络，按开发机本地笔记配置」一句。

### P6　文档布局、命名与引用方式

- **问题**：32.9 KB 的中文名报告置于仓库根目录，与 `docs/` 割裂；`design.md` 引用报告 §5.4／§9 正确，但行号引用（`webdav.go#L80` 等 12 处）会随代码漂移。
- **要求**：报告改名为 `docs/vault-feasibility.md` 并同步更新三处相对链接（`design.md`、`CLAUDE.md`、`README.md` 若有）；代码引用统一为「文件 + 符号（函数/常量/结构体）」；`design.md` 与报告补「版本变更记录」与「审阅记录」表头。

---

## 6. 已核实成立的部分（建议沿用，不必重写）

| # | 内容 | 核实结论 |
|---|---|---|
| 1 | 报告 §5.2「为什么现有 302 播放没有遇到过期问题」的五点论证 | **全部成立**：每次 GET 重新 `Link`；链接缓存 10 分钟；驱动重新签名；连接建立后不校验 `Expires`；302 无显式缓存头 |
| 2 | 「不能用 `curl -I` 取 302」 | 成立：`handleGetHead` 对 URL 类型文件的 HEAD 直接返回 200，不发 `Location` |
| 3 | `auth.StartWatcher` 必须传原始驱动 | 成立：`main.go` 现传 `drv`；`TokenManager` 为类型断言，包装层会丢 |
| 4 | 链接缓存 10 分钟、目录 30 秒、容量 500/1000、5 分钟清理、`Handler.Close()` 停止 | 成立 |
| 5 | 文件头 64 B 的字段偏移与长度 | 累加为 64，无空洞无重叠 |
| 6 | 性能与容量算术（136 MB/s ≈ 1.09 Gbps；41 Mbps；824 KiB < 0.002%） | 计算正确 |
| 7 | 证据分层（官方文档／抓包／第三方源码／httptest）与「未实测」标注 | 应作为后续所有报告的强制列（建议增加「证据等级」列） |
| 8 | `CredentialUpdater` 以可选接口 + 类型断言扩展 | 与 `internal/driver/driver.go` 的 `TokenManager`/`HealthReporter` 模式一致 |

---

## 7. 放行条件

1. **D1–D5** 全部修订进 `docs/design.md` 与报告对应章节（纯文档，不需要写代码）。
2. **I1–I4** 写入格式规范、`_mc` 语义与验收标准（Phase 0 开工前提）。
3. **P1–P2** 落为流程条目与 `PROGRESS.md` 表格模板，本次意见按 §8 回填。
4. **P3** 的 4 条待拍板项进 `TODO.md`，等 PM 逐条裁决后再开工。
5. **I2、I3 与 P4** 的处置结论须在 `PROGRESS.md` 留痕，不得只改文档不记录。

完成以上 5 条后，`design.md` 即满足其自身定义的闸门① 检查项，报告可支撑 Phase 0 直接开工。

---

## 8. 意见处置表（待程序员回填）

> 由程序员填写「处置类别／处置说明／状态」，PM 确认后并入 `PROGRESS.md`「审核意见处置记录」。
> 处置类别取值：`已改` / `补充` / `可选（暂不处理）` / `待拍板` / `驳回（附依据）`。

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| D1 | 阻断 | URL 缓存 TTL 在 Vault 上层不可实现 | | | 待处理 | | |
| D2 | 阻断 | `os.WriteFile` 改 0600 对存量文件无效 | | | 待处理 | | |
| D3 | 阻断 | PROPFIND 属性不经过 `Link.Header` | | | 待处理 | | |
| D4 | 阻断 | `raw.Name()` 是类型名，非挂载名；示例有语法错与签名不一致 | | | 待处理 | | |
| D5 | 阻断 | 凭证三处状态的提交顺序与并发模型未定义 | | | 待处理 | | |
| I1 | 重要 | fileID/nonce 不变量未写死（GCM 重用） | | | 待处理 | | |
| I2 | 重要 | 空文件与头部无认证、字段无校验 | | | 待处理 | | |
| I3 | 重要 | manifest 未与对象绑定，可跨对象替换 | | | 待处理 | | |
| I4 | 重要 | `_mc` 未屏蔽（PROPFIND 可见／GET 可直取） | | | 待处理 | | |
| I5 | 重要 | `Link.StatusCode` 表述与代码不符 | | | 待处理 | | |
| I6 | 重要 | 上游异常分支与 `max_streams` 语义缺失 | | | 待处理 | | |
| I7 | 重要 | `Stop` 需幂等且所有权唯一 | | | 待处理 | | |
| P1 | 流程 | 缺文档评审环节与归档位置 | | | 待处理 | | |
| P2 | 流程 | `PROGRESS.md` 缺结构化留痕格式 | | | 待处理 | | |
| P3 | 流程 | `TODO.md` 缺 4 条待拍板项 | | | 待处理 | | |
| P4 | 流程 | 三处既有转义缺陷应拆出独立修复 | | | 待处理 | | |
| P5 | 流程 | `env.sh` 未入库、内网代理不宜入库 | | | 待处理 | | |
| P6 | 流程 | 文档布局、命名与引用方式 | | | 待处理 | | |

---

## 附录 A：证据索引

| 文档断言 | 代码位置 | 结论 |
|---|---|---|
| 每个 GET/HEAD 都重新取 Link | `internal/webdav/webdav.go` / `handleGetHead` | 成立 |
| URL 类型文件的 HEAD 返回 200、不发 Location | 同上 | 成立 |
| `link.Header` 逐条 `Add` 后交给 `ServeContent` | `webdav.go` / `serveContent` | 成立（见 D3/I5） |
| PROPFIND 属性由 `model.Obj` 生成 | `internal/webdav/prop.go` / `buildPropsXML` | 成立（见 D3） |
| `buildHref` 无百分号编码 | `internal/webdav/prop.go` / `buildHref` | 成立（见 P4） |
| 目录页 HTML 未转义文件名 | `webdav.go` / `serveDirHTML` | 成立（见 P4） |
| 根 PROPFIND 未转义挂载名 | `main.go` / `serveRootPropfind` | 成立（见 P4） |
| 链接缓存 10 分钟、容量 500/1000、5 分钟清理 | `internal/fs/cache.go` | 成立 |
| `CachedReader.Stop()` 为 `close(chan)`，非幂等 | `internal/fs/cache.go` / `Stop` | 成立（见 I7） |
| `Handler.Close()` 类型断言 `Stop()` | `webdav.go` / `Close` | 成立 |
| `conf.Save` 使用 0644 | `internal/conf/config.go` / `Save` | 成立（见 D2） |
| `conf.Save` 会输出空 `cloud` 旧字段 | 同上（`Cloud` 无 `omitempty`） | 成立（见 D5 第 4 条） |
| `main.go` 每请求读 `cfg.Server.*` 做 Basic Auth | `main.go` / `checkAuth` 调用处 | 成立（见 D5） |
| 驱动 `Name()` 返回类型名 | `drivers/baidu/driver.go` / `Name` | 成立（见 D4） |
| `TokenManager`/`HealthReporter` 为可选接口、类型断言 | `internal/driver/driver.go` | 成立（见第 6 节第 8 项） |
| `auth.StartWatcher` 对非 `TokenManager` 跳过 | `internal/auth/watcher.go` | 成立 |

## 附录 B：修订后的自查清单（供程序员提交前一键复核）

```bash
source ./env.sh
go vet ./...
go test ./... -race

# D2：确认不再以 WriteFile 的 perm 作为权限手段
grep -n "WriteFile\|Chmod" internal/conf/config.go

# D1 / I5：确认缓存与状态码语义
grep -n "linkTTL\|expiresAt" internal/fs/cache.go
grep -n "StatusCode" internal/webdav/webdav.go drivers/*/driver.go

# D3 / P4：确认 prop 层来源单一、href 已编码
grep -n "buildHref\|getetag\|getcontenttype" internal/webdav/prop.go

# D4：确认无 raw.Name() 传入包装层
grep -rn "vault.New" .
```

> 行号类引用（如 `webdav.go#L80`）建议同步改为「文件 + 符号」，避免每次改动后失效（P6）。

---

*本报告仅基于 `feature/file-encryption` 分支 `aeb928e` 的文档与代码静态阅读，未编译、未执行、未使用任何真实凭证。评审意见不构成对方案可行性的否定；D1–D5 均可通过修订文档解决。*
