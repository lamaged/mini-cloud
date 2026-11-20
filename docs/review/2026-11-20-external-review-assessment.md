# 外部审核意见评估（975afbc）

| 项目 | 内容 |
|---|---|
| 评估编号 | RV-2026-11-20-01（外部审核意见的二次核实） |
| 评估对象 | 外部审核员对 `feature/file-encryption` @ `975afbc` 的 12 条意见（1×P0 / 6×P1 / 5×P2） |
| 评估方式 | 逐条对照分支源码静态核对（`cmd/mcrypt`、`internal/vault`、`internal/conf`、`internal/webdav`、`drivers/*`）；未编译、未执行 |
| 结论 | **12 条中 11 条成立或部分成立，1 条过时（#12）**；采纳其中 7 条为本批必修，3 条为可选，1 条不处理。整体评价：高质量第二意见，且补到此前多轮审核漏掉的 4 处 |

---

## 1. 总体评价

1. **外部审核质量高**：三条"值得肯定"（密钥结构、随机读取架构、Vault 与 Driver 隔离）与本项目 40 余轮审核的结论一致，且对 HKDF RFC 5869 测试向量的留意是加分项。
2. **命中率高、噪音低**：12 条里只有 #12 过时（`NewUpstreamWithLink` 已在 Phase 1 落地），其余均有依据。
3. **补到 4 处此前遗漏**（见 §4 自省），其中 #4（manifest 并发解析峰值）是本批最有价值的一条。
4. **"暂缓合并"措辞需按项目实际校准**：本项目 master 冻结在 v0.9.2、全部开发在 `feature/file-encryption`，不存在"合并"动作；真实含义应表述为「**升正式版 / 正式部署验收前，先闭合 P0 + P1 关键项**」。

---

## 2. 逐条核实

| # | 外部评级 | 核实结论 | 必要性 | 处置建议 |
|---|---|---|---|---|
| 1 | P0 | **成立**。`keygen` 用 `os.WriteFile`（O_TRUNC）直接覆盖；此前只修了"已存在文件不收紧 0600"，漏了覆盖本身 | 高 | 立即：`O_CREATE\|O_EXCL` + `--force` 二次确认 + 4 组测试 |
| 2 | P1 | **成立**。`vault.paths` 无逐段校验；`"backup/videos"` 会导致容器识别失败 → 退回物理视图 → 密文 302 泄露 | 中高 | 启动校验：非空、禁 `/` `\`、禁 `.` `..`、必须单段、去重；`MaxStreams` 设上限（如 ≤16） |
| 3 | P1 | **成立**。`buildRoot` 对相对路径会在 `path.Dir(".")="."` 死循环。前提：损坏清单被 GCM 拒绝，能进 buildRoot 的坏 Path 仅持钥者可造 | 中 | `buildRoot` 前校验 `Entry.Path`/`Dirs`：以 `/` 开头、`path.Clean==self`、无 `.`/`..`、深度/长度上限，不合格拒载 |
| 4 | P1 | **成立，且最值钱**。32 路并发 `LoadManifestSum` 峰值叠加；`maxHotManifest` 在解析后才判；`SetMemoryLimit` 在 vault Init 后 | 高（AX6） | 全局 manifest 解析并发上限（1–2 路信号量）；`SetMemoryLimit` 前移到 vault Init 前；按实测收窄 `MaxManifestSize` |
| 5 | P1 | **成立**。只查块数不查总字节；加密中文件变长/变短产出"头说 N、密文 M"的坏对象 | 中高 | `total += n` 后 `total != size` 报错 + `size < 0` 校验 + 4 组测试 |
| 6 | P1 | **成立（两半）**。(a) `SaveManifest` 无大小上限，可能生成服务端拒载的清单；(b) 直接覆盖写、非原子 | 中 | (a) 保存前查 JSON/gzip/密文三尺寸；(b) temp+rename（就地覆盖变体场景） |
| 7 | P1 | **成立**。`.gitignore` 现无 `vault.key`/`*.key` | 高 | 立即补一行 |
| 8 | P2 | **成立**。`hex.DecodeString` 错→nil→跳过 `header_sha256` 校验（`mcrypt verify` 与 `Vault.Link` 两处同因） | 中低 | 解码失败/长度≠32 判校验失败，fail-closed |
| 9 | P2 | **成立**。`decrypt` 用 `Entry.Path` 拼输出路径，无 `../` 边界校验 | 低 | 校验 joined 落在 outDir 内 |
| 10 | P2 | **成立，且是此前 defer 的旧账**（RV-02 P11）。`getObj` 把 `List` 错误（含 `ErrForbidden`）伪装成目录 → 假 207 | 中 | `ErrForbidden` 透传 403，其余 List 错误不伪装成目录 |
| 11 | P2 | **部分成立**。root 级 warning 只随 emptyRoot(sum=0) 或 >8MiB 出现，恢复路径均会清；本轮已把容器级改为"本轮清除" | 低 | 顺手加"读成功后先清 `r.warning` 再比摘要"一行 |
| 12 | P2 | **不成立 / 过时**。`NewUpstreamWithLink` 已在 Phase 1（2900ec3）落地，`Vault.Link` 复用已获取 link | 无 | 无需处理 |

---

## 3. 五类处置（按 process §2.3）

| 分类 | 条目 | 处置方向 |
|---|---|---|
| **阻断（必修）** | #7 .gitignore、#1 keygen 覆盖 | 一行/三行级，后果不可逆，先做 |
| **补充（本批）** | #4 并发上限 + 软上限前移、#5 字节校验、#2 paths 校验 | AX6 资源 + 数据完整性 |
| **补充（本批）** | #3 buildRoot 校验、#10 getObj 透传、#8 hex 错误 | fail-closed，成本低 |
| **可选** | #6 SaveManifest 约束、#9 路径边界、#11 告警清一行 | 边界/防呆 |
| **驳回（附依据）** | #12 双重 raw.Link | `NewUpstreamWithLink` 已实现，意见过时 |

> 待拍板项：无。建议的修复顺序与"升正式版"的门槛由 PM 决定。

---

## 4. 审核自省（此前多轮遗漏，本次外部审核补上）

1. **#1 的覆盖维度**：此前只点了"已存在文件权限不收紧"（已修 chmod），漏了 `os.WriteFile` 的 O_TRUNC 覆盖。
2. **#3 buildRoot 死循环**：此前核了 `path.Join` 对 `e.Obj` 的清 `..`，未核虚拟 `Entry.Path` 上 `path.Dir` 的收敛性。
3. **#4 manifest 并发峰值**：此前只点了 `SetMemoryLimit` 时机，未点"32 路并发解析 + 8 MiB 守卫在解析后"的叠加峰值。
4. **#5 字节数校验**：此前把 `idx == ChunkCount()` 当"已有好守卫"予以肯定，未识出"同块数、不同字节"的漏洞。
5. **#10 getObj 伪装目录**：此前已 flag（RV-02 P11）并建议恢复修改项，但实现被 defer；外部审核再次捞起，应借此补齐。

---

## 5. 附议项

外部审核建议"补真正的端到端测试"（明文→mcrypt→模拟云盘→WebDAV PROPFIND/GET/Range/Seek→SHA-256 比对，覆盖空文件/大文件/中文名/多目录/随机 Seek/错钥/篡改/清单缺失/清单损坏）——**同意，且建议纳入本批**。当前单测虽全，但"经 HTTP 全链路"的测试一直薄弱，这组 e2e 能把 #3/#5/#10 这类"边界输入才触发"的问题一次压住。

---

## 6. 处置表（待程序员回填）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| 1 | P0 | keygen 覆盖已有 master key | 阻断 | | 待处理 | | |
| 7 | P1 | vault.key 未入 .gitignore | 阻断 | | 待处理 | | |
| 4 | P1 | manifest 并发扫描峰值 + 软上限时机 | 补充 | | 待处理 | | |
| 5 | P1 | EncryptObject 不校验总字节 | 补充 | | 待处理 | | |
| 2 | P1 | vault.paths 缺严格校验 | 补充 | | 待处理 | | |
| 3 | P1 | buildRoot 相对路径死循环 | 补充 | | 待处理 | | |
| 10 | P2 | getObj 伪装目录（含 403） | 补充 | | 待处理 | | |
| 8 | P2 | verify/服务端忽略 hex 解码错误 | 补充 | | 待处理 | | |
| 6 | P1 | SaveManifest 无约束/非原子 | 可选 | | 待处理 | | |
| 9 | P2 | decrypt 路径逃逸 | 可选 | | 待处理 | | |
| 11 | P2 | 告警残留（root 级） | 可选 | | 待处理 | | |
| 12 | P2 | 双重 raw.Link | 驳回 | `NewUpstreamWithLink` 已实现 | 已关闭 | | |
| — | — | 端到端测试补充 | 补充 | 见 §5 | 待处理 | | |

---

*本评估基于 `975afbc` 分支源码的静态核对与既往多轮审核记录，未编译、未执行。*
