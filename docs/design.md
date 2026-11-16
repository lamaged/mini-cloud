# mini-cloud 设计文档

> 状态：草案 v0.1（待评审）
> 日期：2026-11-15
> 配套文档：README.md（产品文档与变更记录）、docs/process.md（开发与审核流程：角色分工、铁律、审核闭环）、个人文件夹功能-测试总结.md（Vault 可行性报告）

## 1. 背景与目标

当前版本 v0.9.2。接下来两个开发方向：

| 编号 | 目标 | 版本 | 动机 |
|---|---|---|---|
| A | 凭证网页管理：通过 Web 页面更新云盘凭证（首期：移动 authorization） | v0.10.0 | 移动 authorization 约 30 天硬过期，目前需 SSH 登录路由器改 config.json，非常不便 |
| B | Vault 加密文件 WebDAV 还原播放 | v1.0+ | 可行性已验证（见测试总结报告，结论：可行，建议进入 Phase 0） |

**共同原则**：

- 未加密路径的 302 直连行为完全不变（回归测试保护）。
- 只读 WebDAV 不变；不引入外部依赖（纯标准库，go.mod 保持现状）。
- 代码注释与用户可见字符串用中文。

## 2. 开发环境与目标设备差异

| 项目 | 开发机 | 目标设备 |
|---|---|---|
| 硬件 | x86 Ubuntu 24.04 虚机 | Redmi AX6 / OpenWrt ARM64 |
| Go | 1.24.5（/home/test/go，先 `source env.sh`） | 交叉编译静态二进制 |
| 构建 | `go build` | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` + UPX |
| 测试 | `go test -race`、httptest 模拟云盘 API | 真机验收（功能/性能/内存/CA 证书） |

**差异影响**：

- 性能数字（AES-GCM 吞吐、代理转发吞吐）必须以 AX6 实测为准，开发机测得的只能作参考。OpenSSL 实测 AX6 AES-256-GCM ≈ 136 MB/s，Go 实现需真机 benchmark 确认（Phase 0 前置项）。
- 路由器 CA bundle 可能不全 → 云盘 HTTPS 请求失败时的排查项。
- 测试策略：单元/集成测试全部在 x86 跑（含 -race）；云盘驱动用 `httptest` 起假服务器（正常响应 + 过期/403 场景），**不使用真实凭证**。现有云盘驱动无测试的历史问题借此解决。

## 3. 功能 A：凭证网页管理（v0.10.0）

### 3.1 需求

1. 移动云盘 authorization 过期后（当前表现为空目录 + 提示名），用户用浏览器打开 mini-cloud 网页输入新 authorization 即可更新，无需 SSH。
2. 更新后立即生效（运行时热更新），并持久化：重启后仍有效。

### 3.2 接口设计

`internal/driver/driver.go` 新增可选接口（与 TokenManager/HealthReporter 同模式）：

```go
// CredentialField 凭证字段描述（供管理页渲染表单）
type CredentialField struct {
    Key   string // 表单字段名
    Label string // 显示名
    Kind  string // "password" 或 "text"
}

// CredentialUpdater 凭证更新（可选接口）：支持运行时更新凭证
type CredentialUpdater interface {
    CredentialFields() []CredentialField
    // UpdateCredential 校验并应用新凭证；失败时必须回滚到旧凭证
    UpdateCredential(ctx context.Context, values map[string]string) error
}
```

实现方：首期仅 `mobile`（字段 `authorization`）。天翼（username/password）、百度（refresh_token）后续按需实现，管理页自动适配。

### 3.3 页面与流程

新增 `internal/admin` 包（渲染与表单逻辑，通过 main 注入挂载信息与持久化回调，不依赖 conf，避免循环依赖）：

- `GET /admin`：管理页，Basic Auth 保护。列出所有支持 CredentialUpdater 的挂载：名称、当前健康状态（`HealthReporter.HealthWarning`）、凭证表单。每次渲染生成一次性 CSRF token（内存 map + 过期时间）。
- `POST /admin/update`：`mount` + 字段值。流程：
  1. 校验 Basic Auth + CSRF token（一次性）。
  2. 找到挂载 → `UpdateCredential(ctx, values)`。
  3. 成功 → main 将新值写回 config.json 对应 cloud 条目（原子写 temp+rename，写前备份 config.json.bak），返回成功页（含新 token 的过期时间）。
  4. 失败 → 驱动内部已回滚旧凭证，返回错误页与原因。
- 根导航页增加「管理」链接（仅浏览器访问时可见）。

### 3.4 移动驱动实现要点

1. `UpdateCredential` 流程：`parseAuthString` 校验格式 → 保存旧值 → 应用新值到 Client（走现有互斥锁）→ 网络验证（调用个人云 API 列根目录；401/403 视为无效）→ 验证失败恢复旧值并返回错误；成功则 `discoverCloudHost` + `saveTokenState`（`state/<挂载名>_token.json`）。
2. 网络不可达时拒绝更新并明确提示（不写入无法验证的凭证）。
3. 更新成功后 `HealthWarning` 自动消失（`client.isExpired()` 为 false）。
4. config.json 写入由 main 负责（驱动不持有 config 路径）；`conf.Save` 权限从 0644 改为 0600（含真实凭证）。

### 3.5 安全

- 复用现有 Basic Auth（单用户体系）；管理页不再单独设密码。
- CSRF token：GET 生成、POST 校验、一次性。
- 所有页面输出 HTML 转义；日志不打印凭证全文（只打账号与过期时间）。
- config.json、state/* 文件权限 0600。

### 3.6 验收标准（v0.10.0）

1. x86 VM + mock 驱动：更新成功 / 格式错误 / 网络验证失败三条路径 UI 与回滚行为正确。
2. 真机 AX6：输入新 authorization → 挂载恢复、可列目录、可播放；重启后仍有效（config.json 已更新）。
3. 旧 302 行为不受影响（现有测试全绿）。
4. 未登录访问 /admin 返回 401；CSRF token 无效时 POST 被拒绝。

## 4. 功能 B：Vault 加密文件播放（v1.0+）

可行性、加密格式（MCF1 文件头 + 1 MiB 分块 AES-256-GCM）、密钥层级、URL 403 刷新、组件划分详见《个人文件夹功能-测试总结.md》v1.0。本节只记录与现有代码的集成契约。

### 4.1 包装顺序（新增 Vault 与 CredentialUpdater 之后）

```go
raw := createDriver(...)                       // tianyi / mobile / baidu / local
raw.Init(ctx)
auth.StartWatcher(ctx, raw, 5*time.Minute)     // 必须传 raw：TokenManager 断言在 raw 上

cached := fs.NewCachedReader(fs.NewRetryReader(raw))
drv := driver.Driver = cached
if vaultCfg != nil {
    v := vault.New(cached, vaultCfg)
    v.Init(ctx)
    v.Start(ctx)
    drv = v
}
```

- Vault 只拦截 `<mount>/<vaultPath>/...`（如 /backup、/Archive）；其余路径原样委托 → 302 行为不变。
- **`TokenManager` / `HealthReporter` / `CredentialUpdater` 的断言对象必须始终是 raw 驱动**；Vault 与 CachedReader 不得实现这些接口（避免误断言到包装层）。

### 4.2 分阶段与验收（与报告 §9 对齐）

- **Phase 0 / v1.0.0**：`cmd/mcrypt` + `internal/vault`（格式、解密 Reader、manifest），只接 local 驱动。验收：全文件 SHA-256 一致；Range 返回 206；随机 Seek 不整文件下载；`go test -race` 全绿；**AX6 上 Go AES-GCM benchmark ≥ 40 MB/s**（低于则重新评估并发策略）。
- **Phase 1 / v1.1.0**：接天翼家庭 → 移动（验证 403 刷新）→ 百度（OpenRange/dlink 缓存）。验收：4K remux 顺序播放 + 拖动 10 次不失败；URL 过期自动刷新不中断；响应头重写正确。
- **Phase 2 / v1.2.0**：块缓存/预读、百度 4 GB 分卷、manifest 增量更新、多密钥/轮换。

## 5. 里程碑

| 版本 | 内容 | 依据 |
|---|---|---|
| v0.10.0 | 凭证网页管理（mobile authorization） | 本文 §3 |
| v1.0.0 | Vault Phase 0 本地 PoC | 报告 §9 |
| v1.1.0 | Vault Phase 1 云端 MVP | 报告 §9 |
| v1.2.0 | Vault Phase 2 加固 | 报告 §9 |

每个版本按 docs/process.md 的 6 道闸门执行。

## 6. 主要风险与对策

| 风险 | 对策 |
|---|---|
| 网页更新写坏 config.json | 原子写（temp+rename）+ 写前备份 .bak |
| 写入无法验证的凭证 | 网络验证失败即拒绝并回滚 |
| Vault 改动破坏 302 路径 | 不改 302 分支；回归测试 + 验收标准 3 |
| 移动 URL 15 分钟过期 | Vault 代理层 403 刷新（报告 §5.4） |
| 路由器 Go 代理吞吐不足 | Phase 0 AX6 benchmark 前置 |
| 凭据数据竞争 | 沿用现有互斥锁 + getter/setter 模式（be1c043） |
| 管理页暴露给局域网内攻击面 | Basic Auth + CSRF + 输出转义 + 日志脱敏 |
