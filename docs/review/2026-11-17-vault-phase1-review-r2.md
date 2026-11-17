# 第十轮复核：RV-08 处置确认（RV-2026-11-17-09）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-17-09 |
| 评审类型 | RV-08（I1–I5、P1–P6）处置复核 + 回归检查 |
| 评审对象 | `feature/file-encryption` @ `af71273f57fd6c6d5c7b12d1d855349ea4d2fa97` |
| 对比基线 | `f8a5cc0`（RV-08 审核点） |
| 评审方式 | 逐文件静态阅读（`upstream.go`、`vault.go`、`conf/config.go`、`driver.go`、`webdav.go` 与全部测试 diff）；**未编译、未执行** |
| 结论 | **通过（有条件）**：RV-08 的 5 条重要 + 6 条建议全部实质关闭并补了测试；新增 2 条重要（均属配置自愈的边界，不阻断真机验收）、7 条建议 |

---

## 1. 处置复核

| 编号 | 意见 | 结论 | 证据 |
|---|---|---|---|
| I1 | 206 短 body 未区分截断与 EOF | **关闭** | 206 分支改「解析 `Content-Range` 三段 → `len(data) < crEnd-crStart+1` 判截断 → 返回包装 `driver.ErrUpstream` 的错误」；`TestUpstreamTruncatedBodyRetries` 断言「截断 1 次 + 重试成功、恰好 2 次请求」 |
| I2 | 416 与 EOF 语义不统一 | **关闭** | 416 分支解析 `bytes */N`，`off >= N` → `(0, io.EOF)`；`TestUpstream416` 改为断言 `(0, io.EOF)` |
| I3 | 百度 Vault 必然失败 | **关闭（PM 拍板收紧）** | `process.md 铁律 2` 改为「只支持天翼/移动，百度保留兼容存量、不做新开发/Vault 适配」；`Vault.Link` 对非 `ReaderAt` 的代理流返回包装 `ErrUpstream` 的错误 → 502「不在支持范围」；README 支持表与已知限制同步；TODO 记录拍板 |
| I4 | 自愈静默覆盖、备份坏时先覆盖后失败 | **关闭** | 流程改为「先 `LoadStrict(.bak)` 验证 → 存 `.broken-<ts>`(0600) → `RestoreBackup` → 重载」；任一步失败即放弃、原文件不动；日志 `slog.Error` 含 broken 路径 / bak mtime / clouds 数；`TestLoadSelfHealFromBackup` 增副本断言、新增 `TestLoadSelfHealBrokenBackup`（备份坏 → 原文件不动、不产生副本） |
| I5 | 自愈复活旧凭证需明示 | **关闭** | 日志明示「凭证可能已过期，请在 /admin 核对或更新」；`design §3.3` 增「自愈仅针对 JSON 语法错误，语义错误仍拒绝启动」 |
| P1 | loadRoot 对 URL 驱动两次 Link | **关闭** | 新增 `NewUpstreamWithLink(...)`，`refreshURL(ctx, link)` 接受已获取 Link；`loadRoot` 复用探测结果 |
| P2 | ErrUpstream 未映射 502 | **关闭** | `driver.ErrUpstream` 哨兵；`doRange`/`discardServe` 的失败统一 `%w` 包装；`webdav.handleGetHead` 增 `errors.Is(err, driver.ErrUpstream) → 502` |
| P3 | 绑定校验放行空路径 | **关闭** | 改为 `if p := dr.OriginalPath(); p != inner`（空路径同样拒绝，fail-closed） |
| P4 | 30s 总超时偏紧 | **关闭** | 90s，注释给出「1 Mbps 下 4 MiB ≈ 32s」的依据 |
| P5 | URL 驱动接管同名目录需记录 | **关闭** | README 已知限制增「vault 根名匹配即接管，同名普通目录将被隐藏（空目录 + 日志告警）」 |
| P6 | rangeReadCloser 依赖外层限长 | **关闭** | 注释写明依赖 `LoadManifest` 的 `LimitReader(32 MiB+1)` |

**结论：11 条全部闭环，且每条都有对应测试或文档留痕。**

---

## 2. 新增重要

### I6　自愈的前提 `config.json.bak` 只由管理页产生——「从没用过 /admin」的机器自愈无效

- **事实**：全仓库只有 `main.go` 的 persist 回调在写 config 前调用 `conf.Backup`；`Save`/`Load` 都不会自动生成 `.bak`。因此**从未通过 `/admin` 更新过凭证的部署**（全新安装、只手工编辑过 config.json 的机器）没有备份，`Load` 的自愈分支直接跳过，仍按原样报错。
- **为什么值得修**：本次真机踩的正是这个场景——PM 手工往 config.json 加 vault 段时写坏逗号，机器此前**未必**用过 `/admin`。换言之，这条自愈恰好在最需要它的机器上不生效。
- **建议**：`Load` 成功加载后，若 `path+".bak"` **不存在**则写一份（0600）；或把「备份」从 persist 回调下沉到 `Save` 内部（每次 Save 前备份，语义更统一，但需评估与 admin 回滚语义的关系——admin 的回滚依赖「写前备份」，下沉后需保证备份仍是"上一次成功内容"）。
- **建议补测**：`无 .bak + 损坏 → 报错`（已有）与 `无 .bak + 正常加载 → 生成 .bak`（新增）。

### I7　空 config.json（0 字节）不会自愈

- **事实**：自愈块的结构是 `if data, rdErr := os.ReadFile(path); rdErr == nil && len(data) > 0 { … RestoreBackup … }`——「保存损坏副本」与「执行恢复」被串在同一个 `if` 里。空文件时 `len(data) == 0`，整个恢复流程被跳过，直接返回语法错误。
- **为什么值得修**：编辑器先截断再写、磁盘写入中断都会留下 0 字节 config.json，这是最常见的"配置坏了"形态之一；此时恰好不自愈，与功能目标相反。
- **建议**：拆成两步——① 非空时保存 `.broken` 副本（空文件无需副本）；② 无论是否保存副本，只要 `.bak` 通过内存校验就执行恢复。

---

## 3. 建议

| 编号 | 内容 | 建议 |
|---|---|---|
| P7 | `Content-Range` 解析不接受 `bytes a-b/*` | `fmt.Sscanf(cr, "bytes %d-%d/%d", …)` 对「总额未知」写法直接报错 → 2 次重试后 502。个别 CDN/对象存储会返回 `/*`。建议对 `*` 容错（总额未知则跳过截断判定，仅校验起点） |
| P8 | 映射不完整：部分上游失败仍返回 500 | `io.ReadAll` 读取失败（`读取失败: %w`）、`ensureFreshURL` 刷新失败均未包装 `driver.ErrUpstream`，会落到 500 而非 502。建议统一包装 |
| P9 | CDN 单次 Range 上限 < 1 MiB+16 时会被误判为 EOF | 若服务端合法地只返回比请求更短的窗口（`Content-Range` 与 body 一致、但 `end+1 < total`），当前会 `return n, io.EOF`；对 1 MiB+16 的块读来说 `n` 不足 → 解密层报「块不完整」。建议：`len(data) < len(p)` 且未到文件尾时**继续续拉**，而非返回 EOF |
| P10 | `.broken-<ts>` 无清理 | 每次损坏启动留一份（约 1–2 KB）。路由器 flash 只有数 MB，建议保留最近 N 份（如 3）或固定名 `.broken`，避免长期累积 |
| P11 | 百度挂载仍会建虚拟树 | I3 已把它标为"不在支持范围"，但 `loadRoot` 仍会为百度建树（树可见、播放 502）。若要彻底一致，可在 `loadRoot` 阶段检测「Reader 非 `ReaderAt`」即跳过（该挂载完全按普通模式，`_mc` 也会可见）——两种取舍请 PM 定，维持现状也可接受 |
| P12 | 注释重复 | `vault.go` 中 `httpClient` 上方两行注释内容重复（旧行未删） |
| P13 | 百度驱动应标注 legacy | 铁律 2 收紧后，`drivers/baidu` 仍保留兼容存量。建议在包注释标注「legacy：不做新开发/Vault 适配」，避免后续维护误改或误补测试 |

---

## 4. 回归核对

| 项 | 结论 |
|---|---|
| EOF 主链路 | 正常 EOF（`len(data)` 等于声称区间、`end+1 == total`）不触发截断判定 ✓；`DecryptReader.loadChunk` 的 `n == cLen` 契约未变 ✓ |
| 416→EOF 对 `ReadAt` 的影响 | `ReadAt` 循环遇 `io.EOF` 即返回 `(total, io.EOF)`，符合 `io.ReaderAt` 契约 ✓ |
| 502 映射范围 | 403/截断/HTTP 非 2xx/网络错误/不支持 Range 均已包装 ✓；`ErrForbidden`(403) 与 `ErrTooManyStreams`(503) 的既有映射未受影响 ✓ |
| 绑定校验 fail-closed | 空 `metadata.p` 现在拒绝；`mcrypt` 恒写 `p`，现网对象不受影响 ✓ |
| 自愈与写回路径隔离 | `LoadStrict` 未改，管理页写回仍严格、不触发自愈与备份校验 ✓ |
| 流程文档一致性 | 铁律 2、README 支持表/已知限制、design §3.3、TODO 拍板、PROGRESS 处置表五处口径一致 ✓ |

---

## 5. 放行意见

| 项 | 结论 |
|---|---|
| 闸门③（Phase 1） | **通过**：RV-07、RV-08 两轮意见全部闭环；I6/I7 属自愈功能的边界增强，不影响已验收的播放链路 |
| 下一轮建议 | I6（启动时补 `.bak`）、I7（空文件自愈）各约 10 行；P8/P9 建议与 I1 同批处理（都是"上游失败语义"）；其余为文档/清理 |
| 真机 | 天翼家庭站已通过（10+ 次拖动、CPU 5.2%）；移动站与 55 GB 压测仍按 PM 计划 |

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I6 | 重要 | `.bak` 仅由管理页产生，未用过 /admin 的机器自愈无效 | | | 待处理 | | |
| I7 | 重要 | 空 config.json（0 字节）不自愈 | | | 待处理 | | |
| P7 | 建议 | `Content-Range: …/*` 不容错 | | | 待处理 | | |
| P8 | 建议 | 部分上游失败未包装 ErrUpstream（500 而非 502） | | | 待处理 | | |
| P9 | 建议 | CDN 短窗口被误判 EOF（块读 < 1 MiB+16 时） | | | 待处理 | | |
| P10 | 建议 | `.broken-<ts>` 无清理 | | | 待处理 | | |
| P11 | 建议 | 百度挂载仍建虚拟树（可看不可播），取舍待定 | | | 待处理 | | |
| P12 | 建议 | `httpClient` 注释重复 | | | 待处理 | | |
| P13 | 建议 | 百度驱动包注释标 legacy | | | 待处理 | | |

---

*本报告基于 `af71273` 的静态阅读，未编译、未执行、未接触真实凭证。*
