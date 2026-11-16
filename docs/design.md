# mini-cloud 设计文档

> 状态：草案 v0.3（已按外部文档评审 RV-2026-11-16-01/02 修订，待 PM 批准闸门①）
> 日期：2026-11-16（v0.1 发布于 2026-11-15）
> 配套文档：README.md（产品文档与变更记录）、docs/process.md（开发与审核流程：角色分工、铁律、审核闭环）、docs/vault-feasibility.md（Vault 可行性报告与实测数据）

## 版本与审阅记录

| 版本 | 日期 | 变更 |
|---|---|---|
| v0.1 | 2026-11-15 | 首版（v0.10.0 凭证网页管理 + Vault 分阶段设计） |
| v0.2 | 2026-11-16 | 按评审 RV-2026-11-16-01 修订 D1–D5 / I1–I7 / P1–P6；落 PM 五项拍板（HTTPS 不做、绑定加固 Phase 1、三处转义缺陷入 v0.10.0、Vault 全局配置与路径语义、证据分层压缩） |
| v0.3 | 2026-11-16 | 按评审 RV-2026-11-16-02 修订：D6（两阶段接口）、I8–I14、P7–P12；PM 追加拍板（fileID 校验提前 Phase 0、内网地址允许入库） |

| 审阅 | 结论 | 意见处置 |
|---|---|---|
| RV-2026-11-16-01（外部文档评审） | 有条件通过 | 见 PROGRESS.md「审核意见处置记录」 |
| RV-2026-11-16-02（复核评审） | 有条件通过（第二轮） | 见 PROGRESS.md「审核意见处置记录」 |

## 1. 背景与目标

当前版本 v0.9.2。两个开发方向：

| 编号 | 目标 | 版本 | 动机 |
|---|---|---|---|
| A | 凭证网页管理：通过 Web 页面更新云盘凭证（首期：移动 authorization）；同时独立修复 3 处现存转义缺陷 | v0.10.0 | 移动 authorization 约 30 天硬过期，目前需 SSH 登录路由器改 config.json，非常不便 |
| B | Vault 加密文件 WebDAV 还原播放 | v1.0+ | 可行性已验证（见 docs/vault-feasibility.md，结论：可行） |

**PM 拍板记录（2026-11-16）**：

1. 管理页**不上 HTTPS**，HTTP 明文即可（局域网使用，风险接受；如用户自行前置 HTTPS 反向代理亦可，不在本项目实现）。
2. 防「云端偷换 / 回滚文件」的强校验放 **Phase 1**（格式字段自 Phase 0 起就位）。
3. 三处现存转义缺陷（href 编码、目录页 HTML 转义、根 PROPFIND XML 转义）在 **v0.10.0 顺手修**，独立提交、独立验收。
4. Vault 配置**全局一套**；路径语义按 PM 设想：某云盘根目录下的 `backup` / `Archive`（可设多个）及以下全部视为加密文件，子目录继承（详见 §4.3）。
5. 「接口行为证据分层」规则**保留**，压缩为一段话（docs/process.md 铁律 4）。

**共同原则**：

- 未加密路径的 302 直连行为完全不变（回归测试保护）。
- 只读 WebDAV 不变；不引入外部依赖（纯标准库，go.mod 保持现状）。
- 代码注释与用户可见字符串用中文。
- 资源约束：AX6 可用存储仅数 MB，代码必须精简、冗余功能绝对禁止（铁律 10）。

## 2. 开发环境与目标设备差异

| 项目 | 开发机 | 目标设备 |
|---|---|---|
| 硬件 | x86 Ubuntu 24.04 虚机 | Redmi AX6 / OpenWrt ARM64 |
| Go | 1.24.5（/home/test/go，先 `source env.sh`；模板 env.example.sh 入库，env.sh 不入库） | 交叉编译静态二进制 |
| 构建 | `go build` | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` + UPX（`~/upx-5.2.1-amd64_linux`） |
| 测试 | `go test -race`、httptest 模拟云盘 API | 真机验收（功能/性能/内存/CA 证书） |

**部署方向（PM 明确）**：路由器（192.168.2.1，SSH 登录；免密优先，密码回退）作为 SSH 客户端**主动连接开发机拉取产物**；开发机不作为客户端登录路由器。开发机局域网 IP（路由器视角）：**192.168.2.21**。内网地址允许入库（PM 拍板 2026-11-16：仅私有网段地址、不含凭证）。部署脚本（v0.10.0 已提供）：

- `scripts/deploy-prepare.sh`（开发机）：交叉编译 ARM64 + UPX 压缩 → 暂存目录（默认 `~/mini-cloud-staging`）；
- `scripts/deploy-router.sh`（路由器，busybox ash 兼容）：scp 拉取 → 备份旧二进制（`.prev`）→ 停服务 → 安装 → 重启 → 健康检查（期望 401/200）→ **失败自动回滚**。

两脚本均不含凭证（scp 交互式输密码）；体积观察项（铁律 10）由脚本输出并记入 PROGRESS.md。

**差异影响**：

- 性能数字（AES-GCM 吞吐、代理转发吞吐）必须以 AX6 实测为准，开发机测得的只能作参考。OpenSSL 实测 AX6 AES-256-GCM ≈ 136 MB/s，Go 实现需真机 benchmark 确认（Phase 0 前置项）。
- 路由器 CA bundle 可能不全 → 云盘 HTTPS 请求失败时的排查项。
- 测试策略：单元/集成测试全部在 x86 跑（含 -race）；云盘驱动用 `httptest` 起假服务器（正常响应 + 过期/403 场景），**不使用真实凭证**。现有云盘驱动无测试的历史问题借此解决。
- 测试视频/文件：**从天翼家庭开始**（URL 时效长、55 GB 样本现成）。

## 3. 功能 A：凭证网页管理（v0.10.0）

### 3.1 需求

1. 移动云盘 authorization 过期后（当前表现为空目录 + 提示名），用户用浏览器打开 mini-cloud 网页输入新 authorization 即可更新，无需 SSH。
2. 更新后立即生效（运行时热更新），并持久化：重启后仍有效。
3. 附带独立修复 3 处现存转义缺陷（§3.6，独立提交）。

### 3.2 接口设计

`internal/driver/driver.go` 新增可选接口（与 TokenManager/HealthReporter 同模式）：

```go
// CredentialField 凭证字段描述（供管理页渲染表单）
type CredentialField struct {
    Key   string // 表单字段名
    Label string // 显示名
    Kind  string // "password" 或 "text"
}

// CredentialUpdater 凭证更新（可选接口）：两阶段——先验证后提交（D6 修订）
type CredentialUpdater interface {
    CredentialFields() []CredentialField
    // ValidateCredential 校验格式并做网络验证（调用方须带超时，建议 10s）。
    // 不得改变任何状态（内存凭证、token state、config 均不动）。
    ValidateCredential(ctx context.Context, values map[string]string) error
    // CommitCredential 应用新凭证到内存并写入 token state；同步、不联网、不回滚。
    // token state 写入失败时凭证仍生效（重启后以 config.json 为准），返回该错误由调用方提示。
    CommitCredential(values map[string]string) error
}
```

实现方：首期仅 `mobile`（字段 `authorization`）。天翼（username/password）、百度（refresh_token）后续按需实现，管理页自动适配。

### 3.3 页面与流程

新增 `internal/admin` 包（渲染与表单逻辑，通过 main 注入挂载信息与持久化回调，不依赖 conf，避免循环依赖）：

- `GET /admin`：管理页，Basic Auth 保护。列出所有支持 CredentialUpdater 的挂载：名称、当前健康状态（`HealthReporter.HealthWarning`）、凭证表单。每次渲染生成一次性 CSRF token（内存 map + 过期时间）。
- `POST /admin/update`：`mount` + 字段值。流程（D5/D6 修订，两阶段零回滚）：
  1. 校验 Basic Auth + CSRF token（一次性）。
  2. `ValidateCredential(ctx, values)`（`context.WithTimeout`，10 秒）：格式校验 + 网络验证，**不改变任何状态**。失败 → 直接报错，内存与磁盘均保持不变。
  3. main 原子写 config.json（temp + chmod + fsync + rename + **父目录 fsync**，写前备份 `.bak`；I3）。失败 → 报错，**状态仍为零变化**（此时尚未生效）。
  4. `CommitCredential(values)`：加锁应用内存凭证 + 写 token state（调用点唯一）。token state 写入失败仅提示（凭证已生效，重启后以 config.json 为准）。**提交失败（非哨兵错误）→ 用 `.bak` 还原配置**（I6，PM 拍板方案①），真正零变化。
  5. 成功页显示新 token 的过期时间；失败页显示原因。
- 全流程串行化（I1）：`Handler.updateMu` 覆盖验证→写盘→生效，双标签并发提交按先后完整执行，杜绝「磁盘存 A、运行用 B」的错位。
- 根导航页增加「管理」链接（仅浏览器访问时可见）。

**并发模型（D5）**：`cfg` 加互斥锁保护或 copy-on-write——管理页写入不得与请求期读取（`checkAuth` 每请求读 `cfg.Server.*`）共享可写状态；`checkAuth` 读取路径纳入锁范围。

**config.json 写回副作用（D5-4）**：`conf.Save` 会输出空 `cloud` 旧字段。v0.10.0 将 `Cloud` 字段改为**指针 + `omitempty`**（Go 中 struct 值的 omitempty 不生效，指针才能省略），避免用户误判配置损坏。

### 3.4 移动驱动实现要点

1. `ValidateCredential`：`parseAuthString` 校验格式 → 用**临时 Client 实例**（仅 set 新 authorization）做网络验证（列根目录；401/403 视为无效）→ 不动正式 Client 的任何状态。
2. `CommitCredential`：应用 Validate 阶段暂存的凭证（含已验证发现的云主机）到正式 Client（走现有互斥锁）→ `saveTokenState`；**不联网**。token state 写入失败返回错误但凭证保持生效。
3. 网络不可达时拒绝更新并明确提示（不写入无法验证的凭证）。
4. 更新成功后 `HealthWarning` 自动消失（`client.isExpired()` 为 false）。
5. config.json 写入由 main 负责（驱动不持有 config 路径）。

### 3.5 安全

- 复用现有 Basic Auth（单用户体系）；管理页不再单独设密码。
- **已拍板：不上 HTTPS**，HTTP 明文（局域网内使用；同网段设备理论上可截获 authorization，风险接受）。如需加密，由用户在路由器前置 HTTPS 反向代理，本项目不实现。
- CSRF token：GET 生成、POST 校验、一次性。
- 所有页面输出 HTML 转义；日志不打印凭证全文（只打账号与过期时间）。
- config.json、state/* 文件权限 0600（写入后显式 chmod）。

### 3.6 附带独立修复：3 处现存转义缺陷（P4）

| 缺陷 | 位置 | 影响 | 修复 |
|---|---|---|---|
| href 无百分号编码 | `internal/webdav/prop.go` / `buildHref` | 中文/空格/`#`/`?` 文件名在严格客户端 404 | `url.PathEscape` 逐段编码 |
| 目录页文件名未 HTML 转义 | `internal/webdav/webdav.go` / `serveDirHTML` | 文件名可注入脚本（XSS） | `html/template` 或 `html.EscapeString` |
| 根 PROPFIND 挂载名未 XML 转义 | `main.go` / `serveRootPropfind` | 挂载名含 `&` 等字符时产出非法 XML | `xml.EscapeText` |

三条独立提交、独立验收，纳入 v0.10.0；与 Vault 无耦合。

### 3.7 验收标准（v0.10.0）

> 真机验收对照清单见 docs/acceptance-v0.10.0.md（闸门⑤ 对着勾执行）。

1. x86 VM + mock 驱动：更新成功 / 格式错误 / 网络验证失败三条路径 UI 与回滚行为正确。
2. 真机 AX6：输入新 authorization → 挂载恢复、可列目录、可播放；重启后仍有效（config.json 已更新）。
3. 旧 302 行为不受影响（现有测试全绿）。
4. 未登录访问 /admin 返回 401；CSRF token 无效时 POST 被拒绝。
5. 三处转义缺陷修复后：含中文/空格/`#` 文件名的 PROPFIND href 可被严格客户端访问；目录页与根 PROPFIND 对特殊字符安全。
6. 真机 `ls -l` 核对 config.json、config.json.bak、state/* 权限为 0600（D2）。
7. 管理页更新成功后 config.json 无空 `cloud` 块（omitempty 生效）。
8. 两阶段语义：验证失败、config 写失败或提交失败（.bak 还原）时，内存凭证与磁盘文件均保持不变（零回滚）。

### 3.8 实现期修订（v0.3.2，2026-11-16 实现、自查与 RV-03 审核后补充）

按代码自查（15 条，PROGRESS.md 有逐条留痕）与外部代码审核 RV-2026-11-16-03（I1–I6、P1–P9）落地的实现细节，与 §3.2/§3.3 共同构成最终契约：

- **保留键**：values 增加 `__req`（更新请求唯一标识，admin 生成；驱动按它关联 Validate 与 Commit，隔离并发更新）与 `__expiry`（驱动回填显示用）。
- **哨兵错误**：`driver.ErrStateWriteFailed`——CommitCredential 在「凭证已生效但 token state 写盘失败」时用 `%w` 返回；admin 据此显示「更新成功（警告）」，其余提交错误显示「凭证生效失败」。
- **提交生效后清缓存**：admin 在提交生效后回调 main（`OnCommitted`），使该挂载的目录/链接缓存全部失效（`CachedReader.InvalidateAll`），避免旧账号数据残留 30s/10min。
- **移动驱动并发防护**：① 待提交凭证按 `__req` 隔离并 5 分钟过期清理；② 凭证代次计数（`credGen`）——401 自动刷新回调（提取为 `handleAuthFailed`，刷新端点 `refreshURL` 可注入测试）若发现刷新期间有新提交，丢弃过期刷新结果并恢复已提交凭证；③ 路径缓存代次（`cacheGen`）——在途路径解析结果在提交后作废；④ token state 写盘失败时删除旧缓存文件，保证重启后以 config.json 为准；⑤ 提交后同步 `d.authorization` 字段（防未来重新 Init 回退）。
- **config 写回细节**：写回路径用 `conf.LoadStrict`（文件缺失/损坏直接报错，**绝不重建默认配置**，防认证降级）+ `conf.WriteCredential`（严格读取→按挂载映射字段→备份→原子写）；以磁盘当前内容为基准（尊重运行期间手工修改），写盘成功后才更新内存副本；`Save`/`writeFileAtomic` 在 rename 后 **fsync 父目录**（断电可持久，尽力而为）；`conf.Load` 启动时收紧存量 config.json 权限至 0600；`config.json.bak`/`.tmp` 加入 .gitignore。
- **提交失败回滚（I6，PM 拍板方案①）**：persist 成功返回回滚函数，提交失败时 admin 调用它用 `.bak` 还原配置并同步内存副本，做到真正的零变化。
- **更新串行化（I1）**：`Handler.updateMu` 覆盖验证→写盘→生效全流程。
- **ctx 传导**：`discoverCloudHost`/`personalRequest` 全面接受 ctx（10 秒管理页超时真正生效）。
- **Cloud 字段**：改为指针 + `omitempty`（Go 的 struct 值 omitempty 不生效，指针才能省略）；`normalize` 在 clouds 存在时清空 Cloud，避免输出伪 `"cloud"` 块。
- **状态码（P1）**：400 输入错误（表单/CSRF/挂载）；502 验证失败（无法区分凭证无效与上游不可达）；500 写盘/提交失败；200 成功。
- **安全响应头（P2）**：管理页返回 `Cache-Control: no-store`、`X-Frame-Options: DENY`、CSP（`default-src 'none'; style-src 'unsafe-inline'; form-action 'self'`）。
- **有效期门槛（P7，PM 拍板）**：剩余有效期不足 24 小时的 authorization 拒绝接收。
- **Stop 幂等（P4）**：`CachedReader.Stop` 改 `sync.Once`（原属 Vault Phase 0 事项，提前在 v0.10.0 落地），补「重复 Stop 不 panic」测试。
- **编码去重（P6）**：路径百分号编码统一为 `webdav.EncodePathSegments`（导出），main 与 webdav 共用。
- **版本（P3）**：`version` 常量升 `v0.10.0`。

## 4. 功能 B：Vault 加密文件播放（v1.0+）

可行性、加密格式（MCF1）、密钥层级、URL 时效详见 docs/vault-feasibility.md（v1.1）。本节记录与现有代码的集成契约与评审后定稿的语义。

### 4.1 包装顺序（D1/D4 修订后，与报告 §8.2 逐字一致）

```go
raw := createDriver(cc)                      // 驱动以 cc.MountName() 命名（挂载名，非类型名）
if err := raw.Init(ctx); err != nil { ... }  // error 必须处理
auth.StartWatcher(ctx, raw, 5*time.Minute)   // 必须传 raw：TokenManager 断言在 raw 上

cached := fs.NewCachedReader(fs.NewRetryReader(raw))
var drv driver.Driver = cached
if vaultCfg != nil {
    v := vault.New(cached, raw, vaultCfg)    // /M/<vault> 前缀：List 读内存虚拟树、Link 走 raw（自管 URL 表）；其余路径：List/Link 全部委托 cached
    if err := v.Init(ctx); err != nil { ... }
    v.Start(ctx)
    drv = v
}
```

- **路径分流（I11 修订）**：`/M/<vault 前缀>/...` → List 直接读内存虚拟树（不经 cached，manifest 更新延迟与缓存内存可控）、Link 走 raw；其余路径 → List/Link 全部委托 `cached`（302 行为不变）。
- **重试语义（I11 修订）**：Vault 直连 raw.Link 绕过了 RetryReader——Vault 自实现重试：**仅对网络错误重试 1 次**，"not found" 不重试（与 RetryReader 语义对齐）。
- **URL 时效由 Vault 自管（D1）**：现有 `CachedReader` 链接缓存固定 10 分钟 TTL、无按条目指定 TTL 的扩展点，Vault 路径不走它的链接缓存；Vault 自建「URL + 绝对过期时间」表，TTL = `min(10min, expires - now - 60s)`；403 时失效旧 URL → 重新 Link → 重试一次，再失败 502；并发刷新用标准库 mutex 自实现（不引外部库）。
- **`TokenManager` / `HealthReporter` / `CredentialUpdater` 的断言对象必须始终是 raw 驱动**；Vault 与 CachedReader 不得实现这些接口。
- **Stop 幂等（I7）**：`CachedReader.Stop` 改 `sync.Once`（当前二次调用 panic）；所有权链唯一：`main → Handler.Close → Vault.Stop → CachedReader.Stop`，任何中间层不得被 main 单独 Stop；补「重复 Stop 不 panic」单元测试。

### 4.2 分阶段与验收（与报告 §9 对齐）

- **Phase 0 / v1.0.0**：`cmd/mcrypt` + `internal/vault`（格式、解密 Reader、manifest），只接 local 驱动。验收：全文件 SHA-256 一致；Range 返回 206；随机 Seek 不整文件下载；`GET <vault 根>/_mc/...` 返回 403（I4）；空文件可解密、头部被篡改时拒绝（I2）；**manifest 记录的 fileID 与对象头 fileID 一致性校验**（I12，PM 拍板提前到 Phase 0）；`go test -race` 全绿；**AX6 上 Go AES-GCM benchmark ≥ 40 MB/s**（低于则重新评估并发策略）。
- **Phase 1 / v1.1.0**：接天翼家庭 → 移动（验证 403 刷新）→ 百度（OpenRange/dlink 缓存）。验收：4K remux 顺序播放 + 拖动 10 次不失败；URL 过期自动刷新不中断；响应头重写正确；**对象绑定强校验**（manifest fileID 与对象头一致、metadata.p 与请求路径比对，PM 拍板放 Phase 1）。
- **Phase 2 / v1.2.0**：块缓存/预读、百度 4 GB 分卷、manifest 增量更新、多密钥/轮换。

### 4.3 Vault 路径语义与格式要点（评审后定稿）

**路径语义（PM 设想，2026-11-16）**：

- 配置全局一套：`vault.paths`（根目录名列表，如 `["backup", "Archive"]`，**无前导斜杠**，可多个）+ `vault.key_file`（全局 master key，永不传上网盘）。
- 对每个挂载 M：请求路径 `/M/<p>/...` 的第一段 `p` 与 `vault.paths` 匹配即进入 Vault（Handler 已 strip 掉 `/<mount>` 前缀，Vault 收到的是挂载内相对路径，只比较首段字符串，**大小写敏感**）；**子目录继承**——Vault 根下任意深度的目录与文件全部按加密文件处理，虚拟视图完全由 manifest 决定。
- 物理对象名与扩展名均非原名（如 `_mc/o/7f/3a/….bin`）；每个 Vault 根独立持有自己的 `_mc/manifest.mcf` 与对象，共用全局 master key。
- `PROPFIND`/List 虚拟树只含 manifest 记录；`_mc` 与 manifest 未记录的其他物理对象一律不出现；`GET /M/<p>/_mc/...` 及任何未记录路径 → **403**，绝不回落 302 交出密文。
- 未找到 manifest（或损坏）→ 该 Vault 根虚拟视图为空目录 + 健康提示，服务不崩溃。
- **边界语义（PM 拍板，2026-11-16）**：名字匹配即视为 Vault——若某云盘根下存在恰好同名的**普通**文件夹且无 manifest，将显示为空目录（内容不可见）。此属用户误用 vault 路径名，不受保护，按此语义实现。

**格式不变量（v1.1 定稿，详见报告 §7.3）**：fileID 每次加密新生成、禁止复用；manifest 条目含 header_sha256（空文件防头部篡改）；头部字段校验（chunk_size ∈ [64 KiB, 1 MiB]、data_offset == 64+meta_len、meta_len ≤ 4 KiB、plain_size ≥ 0）；对象绑定字段（fileID 一致 + metadata.p 路径比对）自 Phase 0 起就位、强校验 Phase 1。

**响应头（D3 定稿）**：ETag 与 MIME 唯一来源 = Vault 虚拟树（`model.Obj` 增可选字段 ETag/ContentType，`prop.go` / `buildPropsXML` 改造入修改清单）；Vault 自行构造 `Link.Header`，禁止透传上游 `Content-Length` / `Content-Range` / `Content-Encoding` / `ETag`；`Content-Encoding` 强制 identity。

**上游异常分支**（I6，完整表见报告 §7.4）：200 忽略 Range → 流式丢弃到起始偏移后继续；416 → 透传；Content-Range 不符 → 关闭重试一次，再失败 502；客户端断开 → ctx 取消上游；Seek 换流 → 旧 body 必须 Close；`max_streams` 为全局信号量（含获取超时），超限 503。

## 5. 里程碑

| 版本 | 内容 | 依据 |
|---|---|---|
| v0.10.0 | 凭证网页管理（mobile authorization）+ 3 处转义缺陷独立修复 + 部署脚本（scripts/） | 本文 §3 |
| v1.0.0 | Vault Phase 0 本地 PoC | 报告 §9 |
| v1.1.0 | Vault Phase 1 云端 MVP（首接天翼家庭） | 报告 §9 |
| v1.2.0 | Vault Phase 2 加固 | 报告 §9 |

每个版本按 docs/process.md 的 6 道闸门执行。

## 6. 主要风险与对策

| 风险 | 对策 |
|---|---|
| 网页更新写坏 config.json | 原子写（temp+rename+fsync）+ 写前备份 .bak + chmod 0600 |
| 写入无法验证的凭证 | 网络验证（10s 超时）失败即拒绝并回滚 |
| Vault 改动破坏 302 路径 | 不改 302 分支；回归测试 + 验收标准 3 |
| 移动 URL 15 分钟过期 | Vault 自管 URL 表 + 403 刷新（报告 §5.4） |
| 路由器 Go 代理吞吐不足 | Phase 0 AX6 benchmark 前置 |
| 凭据数据竞争 | 沿用现有互斥锁 + getter/setter 模式（be1c043）；cfg 加锁（D5） |
| 普通文件夹恰好与 vault.paths 同名 | 已拍板（TODO 2026-11-16）：显示为空目录 + 健康提示，用户误用不受保护 |
| 管理页局域网明文提交凭证 | 已拍板接受（不上 HTTPS）；用户可自行前置 HTTPS 反代 |
| AX6 存储/内存紧张 | 铁律 10：代码精简、冗余功能禁止；闸门⑤ 验收含体积与内存观察项 |
