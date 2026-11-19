# mini-cloud 设计文档

> 状态：草案 v0.6（§5 Phase 2 设计，已按 RV-2026-11-19-02 评审意见与 PM 拍板修订，待 PM 批准闸门①）
> 日期：2026-11-19（v0.1 发布于 2026-11-15）
> 配套文档：README.md（产品文档与变更记录）、docs/process.md（开发与审核流程：角色分工、铁律、审核闭环）、docs/vault-feasibility.md（Vault 可行性报告与实测数据）

## 版本与审阅记录

| 版本 | 日期 | 变更 |
|---|---|---|
| v0.1 | 2026-11-15 | 首版（v0.10.0 凭证网页管理 + Vault 分阶段设计） |
| v0.2 | 2026-11-16 | 按评审 RV-2026-11-16-01 修订 D1–D5 / I1–I7 / P1–P6；落 PM 五项拍板（HTTPS 不做、绑定加固 Phase 1、三处转义缺陷入 v0.10.0、Vault 全局配置与路径语义、证据分层压缩） |
| v0.3 | 2026-11-16 | 按评审 RV-2026-11-16-02 修订：D6（两阶段接口）、I8–I14、P7–P12；PM 追加拍板（fileID 校验提前 Phase 0、内网地址允许入库） |
| v0.4 | 2026-11-19 | 新增 §5 Phase 2 设计（清单增量更新、异常提示接 /admin、移动驱动启动健壮性）；PM 拍板范围（天翼 limitrate 调研留技术债） |
| v0.5 | 2026-11-19 | §5.1 按 PM 容器结构拍板重写：vault.paths 条目为容器根（只放目录），子目录各自持 _mc 为独立加密单元，自动发现/重建/摘除（免配置免重启）；旧单根结构一次迁移 |
| v0.6 | 2026-11-19 | 按 RV-2026-11-19-02 修订：PM 拍板 I1（接受一级目录名明文）、I2（子目录内全量重加密，适度粒度）；补 I3（扫描短超时/异步换树/失败隔离）、I4（摘要=解密后内容 SHA-256）、P1–P4（两级结构/mcrypt 用法/迁移方式/stray 文件语义） |

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

**配置自愈范围（I5，2026-11-17）**：启动自愈仅针对 **JSON 语法错误**（手工编辑笔误）——自动用 .bak 恢复并明示「已回退到 <时间> 的备份，其中凭证可能已过期，请在 /admin 核对」；语义错误（clouds 为空、key_file 不存在等）仍拒绝启动。自愈不等于「任何配置问题都能自己好」。

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
- **更新锁位置（I7）**：`updateMu` 在表单解析与 CSRF 校验之后获取，串行化只覆盖验证→写盘→生效；`http.Server` 增 `ReadHeaderTimeout: 10s`（全站防慢连接）。
- **回滚如实报告（I8）**：回滚函数返回 error；还原失败页面明示「回滚失败，请手工核对 config.json（.bak 仍保留）」，不得假装已还原。
- **部署脚本强化（I9/I10）**：脱离会话启动（start-stop-daemon → setsid 回退）；旧进程退出确认 + 新 PID 校验（防旧进程死透前假通过）；健康检查轮询 15×1s（弱网启动余量）；日志落 mini-cloud.log；验收清单同步（日志核对版本号、假产物回滚演练、P14 开机自启口径入技术债）。
- **P11–P13**：提交失败分支显式化（凭证生效/未生效两条路径，防契约变化时误报成功）；并发测试补「最后一次写盘值 == 最后一次生效值」最终一致断言；prepare 脚本改 mktemp 独立临时目录 + upx 存在性检查。

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
    v := vault.New(cached, raw, vaultKey, vaultCfg) // 实际签名以代码为准（P13）；/M/<vault> 前缀：List 读内存虚拟树、Link 走 raw（自管 URL 表）；其余路径：List/Link 全部委托 cached
    if err := v.Init(ctx); err != nil { ... }
    drv = v
}
```
（示例仅示意装配顺序；实际接口以代码为准：仅 Init/Stop，无 Start——P19）

- **路径分流（I11 修订）**：`/M/<vault 前缀>/...` → List 直接读内存虚拟树（不经 cached，manifest 更新延迟与缓存内存可控）、Link 走 raw；其余路径 → List/Link 全部委托 `cached`（302 行为不变）。
- **重试语义（I11 修订）**：Vault 直连 raw.Link 绕过了 RetryReader——Vault 自实现重试：**仅对网络错误重试 1 次**，"not found" 不重试（与 RetryReader 语义对齐）。
- **顺序读取数（I3，PM 拍板 2026-11-17 方案 b）**：Phase 1 的 upstream.go 实现顺序流式 RangeSource（长连接 + 内部缓冲，顺序播放不按块发请求）；随机 Seek 走单块 Range。Phase 0 本地文件按块 ReadAt，无网络开销。
- **信号量获取时机（P16③/P21，Phase 1 验收口径）**：并发信号量必须在 raw.Link 之前获取，避免超限拒绝时已白开上游连接。
- **顺序流式 RangeSource 契约（P9，Phase 1 验收口径）**：① 预读/缓冲窗口 4–8 MiB（与 AX6 内存预算对齐，计入每流账）；② 相邻 ReadAt 偏移差 ≤ 缓冲窗口即视为顺序（命中缓冲直接返回，不发新请求）；③ 反向 Seek、跨块跳跃、403 重签后缓冲立即失效并重新拉取；④ 缓冲内存计入全局 max_streams 信号量（每流 ≈ 6 MiB：窗口 4 + 密文 1 + 明文 1，RV-2026-11-18 实测口径），与 URL 刷新锁同一互斥域。
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
- 未找到 manifest（或损坏）→ 该 Vault 根虚拟视图为空目录 + 日志告警，服务不崩溃。
- **驱动能力不足（如百度代理流，不支持流式随机读）→ 完全不建 vault 根**：该挂载按普通模式工作，密文目录物理可见（铁律 2：加密不支持百度，P18）。
- 备份刷新策略（P16）：`.bak` 仅在缺失/损坏时用当前成功加载的配置刷新（保护管理页写前备份的回滚语义）；纯手工编辑机器的备份可能较旧，自愈日志明示 `bak_mtime` 供核对。
- **manifest 启动加载；热刷新（容器自动发现）见 §5.1（Phase 2，2026-11-19 PM 拍板）**。
- **边界语义（PM 拍板，2026-11-16）**：名字匹配即视为 Vault——若某云盘根下存在恰好同名的**普通**文件夹且无 manifest，将显示为空目录（内容不可见）。此属用户误用 vault 路径名，不受保护，按此语义实现。

**格式不变量（v1.1 定稿，详见报告 §7.3）**：fileID 每次加密新生成、禁止复用；manifest 条目含 header_sha256（空文件防头部篡改）；头部字段校验（chunk_size ∈ [64 KiB, 1 MiB]、data_offset == 64+meta_len、meta_len ≤ 4 KiB、plain_size ≥ 0）；对象绑定字段（fileID 一致 + metadata.p 路径比对）自 Phase 0 起就位、强校验 Phase 1。

**响应头（D3 定稿）**：ETag 与 MIME 唯一来源 = Vault 虚拟树（`model.Obj` 增可选字段 ETag/ContentType，`prop.go` / `buildPropsXML` 改造入修改清单）；Vault 自行构造 `Link.Header`，禁止透传上游 `Content-Length` / `Content-Range` / `Content-Encoding` / `ETag`；`Content-Encoding` 强制 identity。

**上游异常分支**（I6，完整表见报告 §7.4）：200 忽略 Range → 流式丢弃到起始偏移后继续；416 → 透传；Content-Range 不符 → 关闭重试一次，再失败 502；客户端断开 → ctx 取消上游；Seek 换流 → 旧 body 必须 Close；`max_streams` 为全局信号量（含获取超时），超限 503。

## 5. 功能 C：Phase 2 加固（v1.2.0，PM 拍板范围 2026-11-19）

### 5.1 容器根 + 子目录自动发现（加密目录即传即用，2026-11-19 PM 拍板 + RV-2026-11-19-02 定稿）

**需求与结构约定（PM 拍板）**：`vault.paths` 配置的容器根（如 `backup`）在云盘根目录下，**容器下只放目录、不放文件**；每个子目录（dir1、dir2…）是**独立加密单元**——各自持有自己的 `_mc/`（清单 + 密文对象），互不影响。**物理层级固定三层：`容器/明文目录/_mc`，单元之间不嵌套**；单元**内部**允许虚拟多级目录（加密清单记录、名字加密，如 `电影/2024/xxx.mp4`）。**使用模型（PM 拍板 2026-11-19）**：子目录 = 原子单元——整体增加、整体删除；拒绝**单个文件级**就地修改、拒绝变更目录层级；**整块替换（覆盖整个 `_mc`）视为「换新」、允许**（RV-2026-11-19-03 P10 措辞定稿：整块覆盖 ≠ 就地修改）；要改 = 删除旧目录、上传新目录**。目标：新增 / 删除加密目录**全部免配置、免重启**，≤TTL 自动生效。旧单根结构（`backup/_mc` 直接存在）不再支持，一次性迁移（见 P3 口径）。

**两个产品口径（PM 拍板，RV-2026-11-19-02 I1/I2）**：

- **I1：一级目录名明文可见，接受**。子目录名（dir1/dir2…）即云盘物理名、也直接展示在 WebDAV 顶层；仅文件内容与文件内名字（manifest 元数据）受加密保护。高敏感目录名场景（如病历类）后续可加「编号 + 显示名」间接层（manifest 加字段），本期不做。
- **I2：子目录 = 加密与增量单元，子目录内仍全量重加密**。新增/删除一个子目录只动它自己；子目录**内部**加文件需重加密、重传整个子目录（「合并模式 / 内容定址」在技术债，暂不做）。**PM 拍板的使用模型：子目录为原子单元（整体加/整体删，拒绝就地修改与层级变更），"重加密+重传"只发生在"整块换新"场景**。仍建议按剧集/专辑/年份等适度粒度分子目录，单目录控制在一轮重加密+重传可接受的量级。

**设计要点**：

1. **容器语义（PM 拍板 2026-11-19 定稿：容器 = 加密专区，不认识即不管）**：`vault.paths` 条目仍为单段名（匹配语义不变，§4.3），意义变为「容器根」。**容器列表由当前加密单元（子根）合成——只认文件夹（加密单元）、其他一律不显示；容器下一切非单元路径（stray 文件、无 `_mc` 目录、放错层级的密文、保留名）一律「未找到」（404，程序不认识即不管，密文同样拿不到，P7 枚举风险根治）**。请求命中子根路径（`backup/dirN/...`）进入该子根的虚拟树；子根内 `_mc` 仍为 403 安全哨兵（不变）。
2. **两级结构（P1）**：`roots` 由「一容器一树」改为**容器 → 子根两级**（容器下多个子根，各持一树）；`findRoot` 先按首段匹配容器、再按第二段匹配子根；`isMcPath` 按**第二段起**的 `_mc` 判定（`backup/dirN/_mc/...` → 403）。与 Phase 1 代码的差异点在实现期逐一对齐，不另起炉灶。
3. **子根生命周期（自动发现，TTL 默认 5 分钟）**：
   - 懒刷新：无常驻轮询 goroutine。容器级 `refreshMu`；TTL 到期后的首次 List（容器路径或任意 vault 路径）触发一次「容器扫描」。
   - **扫描不阻塞浏览（I3）**：触发扫描的那次 List **立即返回旧树**；扫描放入一次性 goroutine 异步执行（`refreshMu` 串行化去重，下个周期前不重复）。换树仍为「旧树之外建好、持锁一次交换」。
   - **扫描超时与失败隔离（I3）**：manifest 读取用**独立的短超时（≤5s）**——新建专用 `http.Client`（区别于播放流 90s 客户端），N 个子目录**逐个失败隔离**：单目录超时/失败 → 记告警、跳过、下周期重试，绝不拖累整体与其余子根。
   - 扫描流程：`List(容器)` 得子目录集 → 对每个子目录尝试读其 `_mc/manifest.mcf`：
     - 读成功 → 与内存摘要比对：变化 → 异步原子重建该子根虚拟树；未变 → 跳过；
     - 「对象不存在」（子目录无 `_mc`）→ 非加密目录，普通模式（如残留旧树则摘除）；
     - 有 `_mc` 但清单损坏/校验失败 → 子根保留、虚拟树为空 + 警告（接 /admin，§5.2）；**密文对象仍受 403 屏蔽，绝不回落普通模式**（防密文泄露）；
   - 云端消失的子目录 → 摘除子根。
4. **保留名**：容器直接子目录中名字以 `_` 开头的（如旧结构的 `_mc`）属「非单元路径」，按第 1 条统一「未找到」；扫描仍识别并告警「旧结构请迁移到 <名字>/_mc」——防旧结构密文泄露 + 迁移引导。
5. **变更摘要口径（I4）**：摘要 = **解密后清单内容的 SHA-256**（解析时顺手计算并存储）。不得取密文哈希——manifest 为 AES-GCM 随机 nonce，同内容密文每次都不同，会误判"每次都变化"。不做 generation 字段（避免 manifest 格式与 mcrypt 联动改动）。
6. **成本与守卫**：
   - 每周期成本 = 1 次 `List(容器)` + N 次 manifest 读（N = 子目录数；PM 场景 ≤10，各几十 KB，5 分钟一次可忽略）。守卫：子目录数 > 32 时跳过扫描并告警（阈值评审可调）。
   - 单子根 manifest 原始大小 > 8 MiB → 该子根跳过热刷新（日志提示重启生效），避免 AX6 瞬时双份峰值。
   - 任一子根重建/新增/摘除后调用 `cached.InvalidateAll()`（清 30s 目录 / 10min 链接缓存——旧 fileID 签名 URL 必须失效）。
7. **mcrypt 用法（P2）**：每个子目录**各跑一次** mcrypt——`mcrypt encrypt -key vault.key -in <本地目录> -out <上传目标>/<目录名>`；上传到云盘 `backup/<目录名>/`（**先传对象后传清单**，避免中间态）。
8. **旧单根迁移（P3）**：① 单组内容（原 backup 只有一组逻辑内容）→ 云盘上把 `backup/_mc` 整体**移动**进 `backup/<名字>/`（零重加密，manifest 不记录自身位置）；② 多组内容 → 逐组重跑 mcrypt 后上传，旧 `_mc` 删除。README 同步。
9. **上传中间态**：清单先到 / 对象后到等中间态由 Link 既有 502/重试语义兜底，下周期自愈。
10. **正在播放的流不受影响**：Link 按当前树做绑定校验，已建立的流持有自己的 Upstream。
11. 无新依赖（标准库 mutex；I3 的短超时客户端为内置 `http.Client` 配置）。

**验收（真机）**：
1. 运行中新增 dirN（上传含 `_mc` 的新目录）→ ≤5 分钟浏览器可见、可播放；
2. 整块换新 dir1（删除旧目录 + 上传新目录，含就地覆盖变体）→ ≤5 分钟列表变化、新文件可播、旧签名 URL 不残留；
3. 删除 dir2 → ≤5 分钟自动消失，无报错；
4. 清单损坏的 dir3 → 空目录 + /admin 提示 + `_mc` 403；
5. 旧结构 `backup/_mc` → 不可见 + 告警（不泄露密文）；
6. 单子目录网络超时（模拟）→ 该目录跳过并告警，其余目录与浏览不受影响；
7. 扫描周期内播放不中断；触发扫描的浏览不阻塞（立即返回旧树）；连续多个周期 RSS 稳定（无泄漏）。
8. **天翼个人站加密验收（PM 拍板加入）**：容器方案在天翼个人站（189Personal）真机验证——代码已支持（与家庭站同驱动类型），补真机验收即可。

### 5.2 异常提示接 /admin

**需求**：移动 authorization 过期、vault 降级（清单损坏/热刷新失败/按普通模式工作）等异常在管理页可见，免 SSH 看日志（Phase 0 P22 技术债）。

**设计要点**：

1. **Vault 不实现 `driver.HealthReporter`**（铁律 7：Vault 不得实现驱动功能接口，否则 main 对最终 Reader 断言会拿到 Vault 告警而**丢失移动驱动的 authorization 过期告警**）。设计为**双通道**：
   - 驱动告警：main 继续对**原始驱动**断言 `HealthReporter`（现状不变，移动告警保留）；
   - Vault 告警：Vault 暴露具体方法 `Warning() string`（非接口），main 在装配挂载时保留 vault 实例引用，收集告警时把「驱动告警 + Vault 告警」合并为一条展示。
2. **页面渲染时按需收集**：/admin 渲染遍历挂载，收集驱动 `HealthWarning()` 与 Vault `Warning()`（均纯内存读、无网络请求），非空以醒目提示条显示在对应挂载区域；html/template 转义不变。
3. 无轮询、无状态存储、无新依赖。

**验收**：httptest/真机——移动过期 → /admin 显示提示条；vault 降级（清单损坏）→ 提示条出现；正常状态无提示；提示文案含 XSS 字符被正确转义。

### 5.3 移动驱动启动健壮性

**需求**：移动 authorization 过期 + 无本地 state 缓存时，启动不卡顿、不阻塞其他挂载就绪，且提示可操作（去 /admin 更新）。

**设计要点**（实现期先核查 drivers/mobile 现状再定细节）：

1. `Init` 中的会话校验失败（过期/网络错误）→ **不重试、不阻塞**：立即返回就绪（或返回错误由 main 按降级挂载处理），绝不 panic、不留半初始化状态。
2. 校验请求带短超时（≤10s，与 /admin 验证一致）。
3. 缓存恢复与校验解耦：state 文件可解析即恢复旧会话挂上（运行时 401 再走既有被动刷新路径）；启动校验只用于决定初始 `HealthWarning` 内容（「authorization 已过期，请在 /admin 更新」→ 与 5.2 联动显示）。
4. 空 state + 过期凭证：挂载正常在线、显示过期提示、`Link` 不发起无谓请求（沿用「过期返回空目录」语义）。

**验收**：httptest + 真机——过期 token + 空 state 启动：服务快速就绪、四挂载全部在线、移动显示过期提示；/admin 更新后立即恢复；启动总耗时与正常启动无显著差异。

---

## 6. 里程碑

| 版本 | 内容 | 依据 |
|---|---|---|
| v0.10.0 | 凭证网页管理（mobile authorization）+ 3 处转义缺陷独立修复 + 部署脚本（scripts/） | 本文 §3 |
| v1.0.0 | Vault Phase 0 本地 PoC | 报告 §9 |
| v1.1.0 | Vault Phase 1 云端 MVP（首接天翼家庭） | 报告 §9 |
| v1.2.0 | Vault Phase 2 加固（容器根自动发现 + 异常提示接 /admin + 移动启动健壮性） | 本文 §5 |

每个版本按 docs/process.md 的 6 道闸门执行。

## 7. 主要风险与对策

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
