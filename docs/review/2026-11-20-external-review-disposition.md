# 外部审核 12 条处置复核（RV-2026-11-20-02）

> **勘误（2026-11-20，见 [RV-2026-11-20-03](2026-11-20-external-review-round2.md)）**：本文件有三处不足——① P1「manifestSem 每挂载而非全局」被低估为"建议"，实为**必须改为进程级共享**（并需重拍默认值）；② 遗漏 keygen `--force` 失败路径（O_TRUNC 先截断→写失败丢旧钥 + 权限不改）；③ 遗漏配置语义错误误触发 `.bak` 自愈（RV-18-02 的回归）。三处均已在 RV-20-03 修正，本文件其余结论不受影响。

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-20-02 |
| 评审类型 | RV-2026-11-20-01（外部审核 12 条 + e2e）处置复核 |
| 评审对象 | `feature/file-encryption` @ `1210a3a9e02e7959a502d97f9ef1ee724343d796` |
| 对比基线 | `200a5a2`（RV-07 放行点；外部审核基线 `975afbc`） |
| 评审方式 | `vault.go`/`manifest.go`/`encrypt.go`/`webdav/prop.go`/`main.go`/`e2e_test.go`/`main_test.go`/`cmd/mcrypt` diff 静态阅读；**未编译、未执行** |
| 结论 | **通过**：12 条全部实质关闭且各有测试佐证；e2e 5 组 9 场景质量高。**其中 #12 我要收回此前的"过时"判定——外部审核是对的，我记错了调用点**。仅 2 条 P 级观察 |

---

## 1. 处置复核

| # | 意见 | 结论 | 证据 |
|---|---|---|---|
| 1 | keygen 覆盖旧密钥 | **关闭** | `O_CREATE\|O_EXCL` 排他创建，已存在返回明确错误；`--force` 才 `O_TRUNC`；4 组测试（存在拒绝/内容不变/force/失败无残留） |
| 7 | vault.key 未入 .gitignore | **关闭** | 补 `vault.key`、`*.key`、`mcrypt` |
| 2 | vault.paths 缺校验 | **关闭** | 启动严格校验（单段/无分隔符/无 `.` `..`/保留名/去重）+ `MaxStreams` 天花板 4（PM 拍板） |
| 3 | buildRoot 死循环 | **关闭** | `ValidateVirtualPath` 导出 + `validateManifestPaths` 在 buildRoot 前 fail-closed（`/` 开头、`path.Clean==self`、无 `.`/`..`、深度/长度上限） |
| 4 | manifest 并发内存 | **关闭** | `manifestSem` 并发限 2（读取/解密/解压/JSON 全程持信号量）；`SetMemoryLimit` 前移 vault.Init 前；`MaxManifestSize` 16 MiB（PM 拍板） |
| 5 | EncryptObject 字节数 | **关闭** | `total += n` 后 `total != size` 报错 + `size < 0` 校验 |
| 6 | SaveManifest 无约束/非原子 | **关闭** | 三层尺寸校验（JSON/gzip/密文，16 MiB）+ mcrypt temp+rename 原子写 |
| 8 | hex 解码忽略错误 | **关闭** | `Vault.Link` 与 verify 均 `err != nil \|\| len != 32` → fail-closed |
| 9 | decrypt 路径逃逸 | **关闭** | `ValidateVirtualPath` + 输出边界校验（joined 落在 outDir 内） |
| 10 | getObj 伪装目录 | **关闭** | `return nil, err`——`ErrForbidden` 透传 403，其余如实上抛（RV-02 P11 的旧账补齐） |
| 11 | 告警残留 | **关闭** | `clearRootWarning` 在"读成功 + 摘要未变"分支先清旧告警 |
| 12 | 双重 raw.Link | **关闭（改判成立）** | `Vault.Link` 改用 `NewUpstreamWithLink`（复用已取 link） |
| — | 端到端测试 | **关闭** | `e2e_test.go` 5 组 9 场景：PROPFIND/GET(SHA-256)/Range/5×Seek/空文件/错钥/篡改/清单缺失/损坏 |

---

## 2. 重要更正：我此前的 #12 判定是错的

- **我在 RV-2026-11-20-01 把 #12 判为"过时"**，依据是"Phase 1 已引入 `NewUpstreamWithLink`"。
- **事实**：`NewUpstreamWithLink` 只在 `loadRoot`（清单加载）使用；**`Vault.Link`（对象服务，每次开播都会走）此前仍调用 `NewUpstream`，内部会二次 `raw.Link`**——外部审核的指认是正确的，我记错了调用点。
- **团队处置诚实**：PROGRESS 记"#12 改判成立（常驻审核记错位置）"，并已修（`Vault.Link` 复用 `link`）。这次改判避免了每次开播多一次云盘 API 调用（对 15 分钟签名有效的移动云盘，这是真实流量与延迟成本）。
- **对团队诚实记录差错的处置方式，给予正面确认。**

---

## 3. 建议（2 条 P 级）

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | `manifestSem` 注释写"全局"，实现是**每 Vault（每挂载）** 2 路 | 多挂载时总并发 = 挂载数 × 2（实测 ≤3 挂载 → ≤6 路 × ≤16 MiB，配 64 MiB 软上限可接受）。建议把注释改为"每挂载 2 路"（或若真需全局，改为包级信号量），避免"全局"字样误导后续调优 |
| P2 | `manifestSem` 获取不响应 ctx | `v.manifestSem <- struct{}{}` 为阻塞发送，等待期不感知 ctx 取消；因持锁方受 5s `manifestReadTimeout` 约束，最坏等待 ≈10s，可接受。建议后续改 `select { case sem <- struct{}{}: case <-ctx.Done(): return }` 更严谨（低优先） |

---

## 4. 回归核对

| 项 | 结论 |
|---|---|
| 加密格式兼容性 | `ValidateVirtualPath`/字节校验只影响"是否接受"，不改磁盘格式；旧对象照常可读 ✓ |
| `MaxManifestSize` 16 MiB | 读写两端一致（`SaveManifest` 三层校验 + `LoadManifestSum` 双限）；容器方案下每子根独立清单，16 MiB ≈ 3.5 万文件/子根，够用 ✓ |
| `SetMemoryLimit` 前移 | 配合 `manifestSem`(2) + 16 MiB，启动期大清单以"GC 提前介入"换"不 OOM"，注释写明取舍 ✓ |
| getObj 语义 | 非 vault 路径 List 错误由"伪目录"改"如实上抛"——更正确；root `/` 特例保留 ✓ |
| e2e 覆盖面 | 走真实 `webdav.Handler → Vault → fakeRaw` 链；URL 型驱动分支未覆盖（由 `upstream_test` 覆盖），不构成缺口 |
| `TestVaultSupported` | 改真实驱动实例断言（tianyi 个人/家庭、mobile、local、baidu），锁死 `Name() ↔ 允许列表` 不变量 ✓ |

---

## 5. 放行

| 项 | 结论 |
|---|---|
| 本轮处置 | **通过**：12 条 + e2e 全部闭环，无阻断项 |
| 下一阶段 | 闸门④（`go vet`+`go test -race`，已自报全绿）→ 闸门⑤ 真机验收（§5.1 8 项 + 天翼个人站） |

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 状态 |
|---|---|---|---|---|
| P1 | 建议 | `manifestSem` 注释"全局"与实际"每挂载"不符 | | 待处理 |
| P2 | 建议 | `manifestSem` 获取不响应 ctx | | 待处理 |

---

*本报告基于 `1210a3a` 的静态阅读，未编译、未执行。*
