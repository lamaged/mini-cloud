# Vault Phase 0 核心审核（RV-2026-11-17-02）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-17-02 |
| 评审类型 | 代码/安全审核——Vault Phase 0 核心（MCF1 格式、加解密、解密读取器、manifest、mcrypt） |
| 评审对象 | `feature/file-encryption` @ `6293b11b9d488743dcf78c66a50e6a1610ac5735`（`5e35417` 格式/加密、`eacdb07` 读取器/mcrypt、`6293b11` manifest 上限） |
| 对比基线 | `7456cad`（v0.10.0 发布点） |
| 评审方式 | GitHub 逐文件静态阅读（`internal/vault/*`、`cmd/mcrypt/main.go` 全文）；**未编译、未执行** |
| 结论 | **核心设计正确，两项实现级 bug 需修，一项设计级 gap 需 Phase 1 前决策；尚未接入 HTTP/driver，无端到端路径** |

---

## 0. 直接回答：解密读取器（视频拖动核心）设计是否合理

**合理，且几处关键点做得对**：

1. **随机 Seek 是块粒度、O(1) 内存**：`Seek(0, End)` 直接返回 `PlainSize`、不读密文；`Seek(start)` 只置 `hasChunk=false`，下一次 `Read` 才按需加载目标块——一次拖动 = 1 次 `ReadAt`（1 块 ≈ 1 MiB），`countingSource` 测试断言随机 Seek 读入量 ≤ 2 块。
2. **块级 GCM 认证**：`nonce = 4B 前缀 + 8B 块序号`、`AAD = SHA256(头) + 块序号`，篡改/截断/重排都会被 `ErrAuth` 拦截（测试覆盖篡改、截断、重排三类）。
3. **打开即校验**：`OpenDecryptReader` 强制 fileID 一致性（I12）+ `header_sha256`（I2 空文件头部认证）+ 头部 9 项字段校验，全部在"吐出第一字节明文前"完成。
4. **与 `http.ServeContent` 的契约对齐**：实现 `io.ReadSeeker` + `Stat()`，`ServeContent` 的 `Seek(0,End)→Seek(start)→顺序 Read` 模式与之兼容。

**但有三处要改**（见 I1/I2/I3），其中 I1/I2 是代码 bug，I3 是"顺序读"的设计缺口。

---

## 1. 重要（I1–I3）

### I1　`EncryptObject` 在计算 SHA-256 之前就加密了元数据 → 对象内嵌 `Metadata.H` 恒为空

- **位置**：`internal/vault/encrypt.go` / `EncryptObject`；`cmd/mcrypt/main.go` / `cmdEncrypt`。
- **事实**：`SealMeta(...)` 在函数开头就执行（此时 `meta.H` 还是空字符串），随后才流式分块并 `meta.H = hex(hasher.Sum(nil))`。因为 `meta` 是**已序列化进密文**的结构体，这次回写对磁盘上的元数据毫无作用；它唯一的作用是让调用方（`cmdEncrypt`）通过指针拿到哈希，从而写进 `manifest.Entry.SHA256`。
- **后果**：
  1. 对象内嵌的加密元数据里 `h=""`，而 manifest 里 `sha256` 是正确的——**两处真值源不一致**，与 `docs/vault-feasibility.md §7.3`（元数据含 `h: sha256`）不符。
  2. 当前无害（`DecryptReader` 不读 `meta.H`，`decrypt`/`verify` 用的是 manifest 的 `e.SHA256`），但 Phase 1 若要做"按对象自包含校验"或任何读 `Metadata.H` 的路径，会静默拿到空哈希。
  3. 末尾的 `meta.H=...; meta.S=size` 是对已序列化结构体的无意义回写，误导后续维护者。
- **根因**：单遍流式下"元数据必须写在数据前（`data_offset` 依赖 `meta_len`）"与"哈希只能在读完数据后得到"是天然矛盾。
- **要求**（二选一，写进文档）：
  - **(a) 推荐**：`Metadata.H` 语义改为"不使用/废弃"，权威 SHA-256 只存 `manifest.Entry.SHA256`；删掉 `meta.H` 回写，注释说明"元数据先于数据落盘，无法在单遍内自包含哈希"。
  - **(b)** 若必须对象自包含：改两遍加密（第一遍只算哈希，第二遍加密），代价是大文件读两遍；Phase 0 不推荐。

### I2　`Header.CipherSize()` 对非对齐明文算错（最后一块按满块计）

- **位置**：`internal/vault/format.go` / `CipherSize`。
- **事实**：`CipherSize() = DataOffset + ChunkCount*(ChunkSize+TagSize)`。当 `PlainSize` 不是 `ChunkSize` 的整数倍时，最后一块明文更短、密文也更短，该式多算了 `ChunkSize - 最后块明文` 字节。例：1.5 MiB 明文 → 实际密文 = `data_offset + 1.5MiB + 2*16`，此式给出 `data_offset + 2MiB + 2*16`。
- **正确公式**（与报告 §7.3 一致）：`CipherSize = DataOffset + PlainSize + ChunkCount*TagSize`。
- **影响**：当前疑似无调用方（reader/encrypt/mcrypt 均未使用），属潜在 bug；Phase 1 若用它做对象大小/完整性核算，会得到错误总数。
- **要求**：改正或删除，并加一个"非对齐明文"的测试用例锁定公式。

### I3　顺序读 = 每块一次 `ReadAt`，与报告"顺序读保持一个上游连接"相悖（Phase 1 设计缺口）

- **位置**：`internal/vault/reader.go` / `DecryptReader.Read` + `loadChunk`。
- **事实**：`loadChunk` 每块调一次 `src.ReadAt(buf, ChunkCipherOffset(idx))`，一块读完即弃。对 Phase 0 的 `*os.File` 无妨；但 Phase 1 的 `RangeSource` 是云盘 HTTP Range，**一次顺序播放 55 GB = 约 5.5 万次 Range 请求**（4K 41 Mbps 下约 5 次/秒），每次都有建连/签名开销，且与报告 §7.4「顺序读时保持一个上游连接、不按块发请求」直接矛盾。
- **为什么随机 Seek 不受影响**：拖动天然是"一次 Seek 一次请求"，块粒度恰好正确。
- **要求**：Phase 1 前决策——
  - **(a)** 接受块粒度（云盘 CDN 的 Range 足够便宜，5 req/s 也许可接受），但要在报告里**改掉**"不按块发请求"的承诺，避免文档与实现不一致；
  - **(b)** 在 `internal/vault/upstream.go` 做"顺序流式 + 读前"的 `RangeSource`（下发大段 Range、内部缓冲、按块切出），让顺序读走长连接、随机读走单块。二选一写进 design §4.1/§7.4。

---

## 2. 建议（P1–P6）

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | `OpenDecryptReader` 读 meta 时未检查 `ReadAt` 返回的 `n` | `blob` 已预分配满 `MetaLen`，Phase 1 网络短读（`n<MetaLen` 且 `err==nil`）会被当成完整数据送进 GCM，最终误报"元数据解密失败/认证失败"而非"不完整"。补 `n < h.MetaLen` 检查，报"元数据被截断" |
| P2 | `cmdKeygen` 用 `os.WriteFile 0600`，对**已存在**的 `vault.key` 不收紧权限 | 与 config 的 D2 同类：`WriteFile` 的 perm 只在创建时生效。补显式 `os.Chmod(path, 0600)` |
| P3 | `cmdEncrypt` 未拒绝 `-out` 位于 `-in` 内部（或二者相同） | `mcrypt encrypt -in . -out .` 会把刚写的 `_mc/` 对象再当明文加密一遍（自引用/膨胀）。启动时校验 `out` 不在 `in` 之下并拒绝 |
| P4 | 符号链接等非常规文件被静默跳过 | 加密目录树里若有 symlink，`return nil` 跳过且无提示，用户可能误以为已加密。建议打印"跳过 <路径>（非普通文件）" |
| P5 | `DecryptReader.Read` 无 `c==0` 防御 | 当前块不可能为空，但若未来 `ChunkPlainLen` 逻辑变化出现空块，`copy` 返回 0 → 死循环。补 `if c==0 { return n, io.ErrUnexpectedEOF }` |
| P6 | `metaAADHeader` 用"零占位头"做 AAD 的原因只在代码注释、未在格式文档强调 | 逻辑自洽（meta AAD 绑头部除 meta_len/data_offset 外所有字段，分块 AAD 绑完整头，空文件由 header_sha256 兜底），但"为什么 meta AAD 不绑 meta_len/data_offset"应在 `docs/vault-feasibility.md §7.3` 写一句，防止后人误改成"绑完整头"而破坏 Sealing 顺序 |

---

## 3. 已核实成立（不必改）

| # | 项 | 结论 |
|---|---|---|
| 1 | 头部 9 项字段校验（magic/version/flags/chunk_size/plain_size/meta_len/data_offset） | 成立，I2 定稿规则全部落地；`flags!=0` 拒绝、`data_offset==64+meta_len` 强制 |
| 2 | nonce=前缀+块序号、AAD=SHA256(头)+块序号 | 成立，防重排/头篡改/块替换 |
| 3 | `Seek(0,End)` 零 I/O 返回明文大小；随机 Seek 只读 1 块 | 成立（`countingSource` 断言 ≤2 块） |
| 4 | fileID 一致性（I12）+ header_sha256（I2）在打开时强制 | 成立，各有拒绝测试 |
| 5 | HKDF-SHA256 自实现 | 成立（RFC 5869 A.1 测试向量通过） |
| 6 | 三层密钥 info 隔离（data/meta/manifest） | 成立，盐用 fileID / nil 区分 |
| 7 | 流式加密内存 = 一块 + 元数据；`idx==ChunkCount` 防大小不符 | 成立 |
| 8 | manifest 密文+解压双重 32 MiB 上限（gzip 炸弹） | 成立（6293b11 补齐） |
| 9 | 篡改/截断/重排/空文件头篡改均返回 `ErrAuth` | 成立（reader_test 覆盖） |

---

## 4. 尚未接线（下一步注意）

`DecryptReader` 目前是独立库，**还没有接到 `http.ServeContent` / `local` 驱动 / Vault 包装层**，所以"视频拖动"的端到端路径尚不存在。接线时需满足：

- `RangeSource` 对 Phase 0 = `*os.File`（`ReadAt`/`Close` 天然满足）；对 Phase 1 = 云盘 Range（受 I3 顺序读约束）。
- `webdav.serveContent` 的 `Stat()` 已能拿到 `PlainSize`/`ModTime`，但 **ETag 与 MIME 唯一来源仍是 manifest**（D3 定稿），需在 Vault 层把 `model.Obj` 的 ETag/ContentType 字段接上，避免 PROPFIND 与 GET 的 ETag 分叉。
- `_mc` 屏蔽（I4）、`GET /<vault>/_mc/...` 403 尚未实现，Phase 0 验收项。
- `CachedReader.Stop` 已是 `sync.Once`，Vault 层 Stop 只需按所有权链下传。

---

## 5. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 元数据先于哈希计算加密，对象内嵌 H 恒空 | | | 待处理 | | |
| I2 | 重要 | CipherSize 对非对齐明文多算 | | | 待处理 | | |
| I3 | 重要 | 顺序读=每块一次 ReadAt，与报告承诺相悖 | | | 待处理 | | |
| P1 | 建议 | 读 meta 未检查 ReadAt 的 n | | | 待处理 | | |
| P2 | 建议 | keygen 未收紧已存在 vault.key 权限 | | | 待处理 | | |
| P3 | 建议 | encrypt 未拒绝 out 位于 in 内部 | | | 待处理 | | |
| P4 | 建议 | 符号链接静默跳过 | | | 待处理 | | |
| P5 | 建议 | Read 无 c==0 防御 | | | 待处理 | | |
| P6 | 建议 | meta AAD 零占位原因补文档 | | | 待处理 | | |

---

*本报告基于 `6293b11` 的静态阅读，未编译、未执行、未使用真实凭证。I1/I2 均可在 Phase 0 内修复，I3 需 Phase 1 前拍板。*
