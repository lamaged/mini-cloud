# Phase 1 第一块审核：上游取数层（RV-2026-11-17-07）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-17-07 |
| 评审类型 | 代码审核（Phase 1 第一块：`internal/vault/upstream.go` + 测试） |
| 评审对象 | `feature/file-encryption` @ `b342202d51e30c58807ff0af5f4320998a9aad10` |
| 对比基线 | `d4cc820`（Phase 0 闭环点） |
| 评审方式 | 逐文件静态阅读（`upstream.go`、`upstream_test.go`、`vault.go`、`reader.go` 全文与算术推演）；**未编译、未执行** |
| 结论 | **不通过（需修 P0）**：范围边界清楚、契约与测试骨架到位，但顺序缓冲存在一处**必然触发**的短读缺陷（约每 4 MiB 一次），会使解密读取器误报「文件被截断」；另 1 条性能缺陷与 3 条契约/并发问题 |

---

## 0. 范围与状态（先厘清，避免误判）

本提交新增 `internal/vault/upstream.go`（URL 自管 + 顺序缓冲 + 403 刷新），但 **尚未接入 `Vault.Link`**：

- `Vault.Link` 仍走 `v.raw.Link → link.Reader → io.ReaderAt`（`vault.go` 内无 `NewUpstream` 调用）；
- `Upstream` 目前只被 `upstream_test.go` 使用，生产路径不可达；
- 因此天翼/移动（URL 型驱动）依旧不可用，与 Phase 0 一致。

这属分块交付的合理边界，**但须在提交说明与 PROGRESS 中写明「取数层入库、未接线」**，避免被读成「Phase 1 已支持云盘播放」。下文 U1/U2 在接线后立即生效，故按接线前必修处理。

---

## 1. 阻断级（P0）

### U1　缓冲命中路径短读不补齐 → 解密读取器每约 4 MiB 误报「文件被截断」

- **位置**：`upstream.go` / `ReadAt` 的缓冲命中分支：
  ```go
  if len(u.buf) > 0 && off >= u.bufStart && off < u.bufEnd {
      n := copy(p, u.buf[rel:])
      return n, nil      // ← n 可能小于 len(p)，且返回 nil error
  }
  ```
- **算术必然性**：窗口 `StreamWindow = 4 MiB = 4,194,304`；块密文长度 `1 MiB + 16 = 1,048,592`。四块合计 `4,194,368`，**比窗口多 64 字节**（再加 `dataOffset`）。因此以窗口起点为基准顺序读取时，第 4 块必然跨越 `bufEnd`：
  - 第 0/1/2 块完整落在缓冲内；
  - 第 3 块需要 `[bufStart+3·1,048,592, +1,048,592)`，超出 `bufEnd` 若干十~数百字节；
  - `copy` 只拷到 `bufEnd`，返回 `n < len(p)` 且 `err == nil`。
- **下游后果**：`reader.go` / `loadChunk` 要求 `n == cLen`，否则返回
  `"密文块 %d 不完整: %d/%d（文件被截断）"`。即：**顺序播放约 3–4 MiB 后必然报「文件被截断」**，播放中断。
- **同时违反接口契约**：`io.ReaderAt` 规定 `n < len(p)` 时必须返回非 nil error；当前返回 `(n, nil)`。
- **测试为何没抓到**：`TestUpstreamSequentialBuffering` 只用 1000 字节的 `p` 在缓冲内读取，从未构造「跨窗口的块读」。
- **要求**：
  1. 命中判定改为「**完整覆盖**」：仅当 `off+len(p) <= bufEnd` 才走缓冲；否则先拷贝已有部分，再对剩余部分发起请求（或整体落回 fetch）。
  2. `ReadAt` 内部循环填充 `p`，耗尽缓冲后按顺序续拉，直至 `len(p)` 填满或到达 EOF；短读必须以 `io.EOF` 结束。
  3. 补测试：以 `1 MiB+16`（真实块长）为单位从 0 连续读 8 块，断言每块 `n == cLen`、内容逐字节一致、请求次数 ≈ 2（两个 4 MiB 窗口）。

---

## 2. 重要

### U2　随机 Seek 之后预读永久失效（单块路径不更新也不清空缓冲）

- **位置**：`ReadAt` 的顺序判定与 `fetchRange(..., allowBuffer=false)`。
- **事实**：顺序判定以**旧缓冲**的 `bufEnd` 为基准：
  ```go
  sequential := len(u.buf) == 0 || (off >= u.bufEnd && off-u.bufEnd <= StreamWindow)
  ```
  随机读（`off` 距旧 `bufEnd` > 4 MiB）走单块分支，且 `allowBuffer=false` → 缓冲保持旧值；此后每次读到的 `off` 都在旧 `bufEnd` 之外且距离仍 > 4 MiB → **永远判定为非顺序**，每个 1 MiB 块各发一次请求，直到发生 403 刷新（`refreshURL` 会置空缓冲）才恢复。
- **影响**：拖动一次之后，整段后续播放退化为「每块一请求」（4K 约 5 次/秒），与本块 P9 契约「顺序播放不按块发请求」的初衷相反，也削弱了 `StreamWindow` 的存在意义。
- **要求**：单块读取成功后，要么把该块写入缓冲（`bufStart=off, bufEnd=off+n`），要么**清空缓冲**（使下一次读走「首读」分支并重新预读 4 MiB）。建议前者（不浪费已取数据，且下一块起点正好等于 `bufEnd` → 命中顺序判定）。
- **补测试**：`ReadAt(2 MiB)` → `ReadAt(50 MiB)`（跳跃）→ 再连续读 3 块，断言最后一次跳跃后请求次数 ≤ 2 且逐块内容正确。

### U3　P21 契约未落实：信号量仍在 `raw.Link` 之后获取

- **事实**：`Vault.Link` 先 `v.raw.Link(ctx, objPath)`，后 `v.sem.acquire(...)`；而 RV-05 刚把「信号量必须在 `raw.Link` 之前获取」写入 `design §4.1`（P21）。
- **影响**：本地驱动 `Link` 是 `os.Open`，代价可忽略；但 Phase 1 接线后 `Link` = 云盘 API 调用（天翼/移动每次 Link 都是一次 HTTP 签名请求），超限拒绝时仍白开一次 API 调用——正是 P21 要避免的情形。
- **要求**：接线 `Upstream` 时把 `sem.acquire` 前移到首次 `Link` 之前；当前提交至少把该顺序对齐文档，或在 `Vault.Link` 增加注释说明「local 路径无需前置，接线时调整」。

### U4　`ensureFreshURL` 在锁外写共享状态，单飞与「非并发安全」契约相互矛盾

- **事实**：`ensureFreshURL` 置 `refreshing=true` 后解锁，再调用 `refreshURL`；而 `refreshURL` 直接写 `u.linkURL / u.linkHdr / u.expiresAt / u.buf / u.bufStart / u.bufEnd`，**不持锁**。`doRange` 在持锁状态下读这些字段。`DecryptReader` 的注释声明「非并发安全、调用方串行」，若该前提成立则单飞机制多余；若日后允许并发（如预读 goroutine），这里就是数据竞争。
- **要求**：二选一并写清前提——
  - **(a)** 明确 `Upstream` 仅串行使用：删除 `refreshing` 单飞逻辑，改为注释说明「调用方保证串行」；
  - **(b)** 保留单飞：`refreshURL` 先写局部变量，最后持锁一次性交换（`u.linkURL = newURL; …`），避免锁外写。

---

## 3. 建议

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | 200 大偏移**直接失败**，未按 I10「先换签名重试一次」 | `doRange` 注释写「换签名由 fetchRange 重试」，但 `fetchRange` 对 `ErrRangeNotSupported` 立即返回、不重试。二者择一：或在 `fetchRange` 对 200 也做一次换签名重试（I10 原文），或改注释与报告 §7.4 表格，明确「200 即判定不支持，不重试」 |
| P2 | 416 被当作可刷新错误 | `fetchRange` 对所有错误（除 `ErrRangeNotSupported`）都换签名重试；416 是「偏移越界」，换签名无意义且多一次 API 调用。建议 416 直接返回，不刷新 |
| P3 | 透传 `Link.Header` 全部字段到请求 | 当前 `for k, vs := range hdr` 把驱动给的响应头（可能含 `Content-Type`/`Content-Length`）塞进 GET 请求。建议白名单（`User-Agent`、`Referer`），避免把响应语义带进请求 |
| P4 | 测试缺口 | 除 U1/U2 用例，建议补：① 403 发生在**顺序缓冲连续读**中途（当前只在首读）；② 并发调用 `ensureFreshURL`（若保留单飞）；③ `Content-Range` 与请求不符时的换签名重试路径（当前无覆盖） |
| P5 | 200 分支丢弃后未读完 body | `io.CopyN(io.Discard, …, off)` + 读 `p` 后即返回，剩余 body 随 `Close` 丢弃，连接无法复用。属可接受取舍，建议注释说明「200 路径放弃 keep-alive」 |

---

## 4. 已核实成立

| # | 项 | 结论 |
|---|---|---|
| 1 | 签名过期解析（§5.1） | 天翼 `Expires`（含 13 位毫秒归一）与移动 `X-Amz-Date + X-Amz-Expires` 均实现，四条用例覆盖 |
| 2 | 403 → 换签名重试一次 | `refreshURL` 重新 `Link`，`TestUpstream403Refresh`（重试 2 次）与 `TestUpstream403ThenRecover`（第二次成功且内容正确）覆盖 |
| 3 | 200 分级（I10） | 小偏移（≤ 2 MiB）丢弃后继续；大偏移返回 `ErrRangeNotSupported` |
| 4 | 416 分支 | 明确报错，测试覆盖 |
| 5 | UA 透传 | `TestUpstreamRandomRead` 断言 `Mozilla/5.0` 到达 CDN（天翼必需） |
| 6 | 缓冲失效时机 | `refreshURL` 置空缓冲，反向 Seek 由非顺序分支绕开旧缓冲，`TestUpstreamBackwardSeekInvalidates` 覆盖 |
| 7 | 内存账 | 每流 4 MiB 窗口 + 2 MiB 解密块 = 6 MiB，`max_streams=4` 封顶 24 MiB，与 design §4.1 契约一致 |
| 8 | ctx 传导 | `baseCtx` 取 Link 时请求 ctx，客户端断开即取消上游请求（I6） |

---

## 5. 放行意见

| 项 | 结论 |
|---|---|
| 本提交（取数层入库） | 可入库，但须在 PROGRESS/提交说明标注「未接线」；**U1 必须在接线前修复**，否则接线即播放不可用 |
| 接线（`Vault.Link` 走 `Upstream`）前 | U1、U2 必修；U3 随接线落实；U4 二选一明确；P1/P2 建议同步 |
| 测试 | 建议把「块长 × 跨窗口连续读」作为取数层的核心用例（当前唯一能暴露 U1 的场景） |

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| U1 | 阻断 | 缓冲命中短读不补齐，每约 4 MiB 误报「截断」 | | | 待处理 | | |
| U2 | 重要 | 随机 Seek 后预读永久失效（每块一请求） | | | 待处理 | | |
| U3 | 重要 | 信号量仍在 raw.Link 之后（与 P21 契约不符） | | | 待处理 | | |
| U4 | 重要 | ensureFreshURL 锁外写状态；单飞与串行契约矛盾 | | | 待处理 | | |
| P1 | 建议 | 200 大偏移不重试，与 I10/注释不一致 | | | 待处理 | | |
| P2 | 建议 | 416 不应触发换签名 | | | 待处理 | | |
| P3 | 建议 | Link.Header 全量透传，建议白名单 | | | 待处理 | | |
| P4 | 建议 | 测试缺口（缓冲中途 403、并发刷新、Content-Range 不符） | | | 待处理 | | |
| P5 | 建议 | 200 路径放弃 keep-alive，建议注释 | | | 待处理 | | |

---

*本报告基于 `b342202` 的静态阅读与算术推演，未编译、未执行、未接触真实凭证。U1 的触发点为「4×(1 MiB+16) > 4 MiB」，与具体文件内容无关，属必然路径。*
