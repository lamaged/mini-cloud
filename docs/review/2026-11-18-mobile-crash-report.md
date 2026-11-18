# 移动云盘站真机验收报告 + 崩溃缺陷修复方案（2026-11-18）

| 项目 | 内容 |
|---|---|
| 评审对象 | `feature/file-encryption` @ `25f83ef`（v1.1.0-dev，Phase 1 代码评审闭环后的首个真机缺陷） |
| 验收类型 | 闸门⑤ 移动云盘站真机验收 + 极限压测 |
| 设备 | 红米 AX6（OpenWrt ARM64，总内存约 402 MB） |
| 结论 | **移动站核心验收通过**（含 15 分钟签名自动刷新实测）；**极限压测暴露内存预算缺陷**——4 并发解密流下应用崩溃（SIGSEGV），根因已定位，修复方案（A/B/C/D）待审核与 PM 确认后实施 |

---

## 1. 验收结果（闸门⑤ 移动站）

| # | 项目 | 结果 | 证据 / 数据 |
|---|---|---|---|
| 1 | 部署与启动 | ✓ | 日志 04:07:27 `Mini Cloud WebDAV Gateway 启动 version=v1.1.0-dev`；移动/天翼挂载 vault 虚拟树就绪 |
| 2 | 虚拟树浏览 | ✓ | PM 用 Edge 直接可见移动云盘 backup 目录与原始文件名 |
| 3 | 网页下载（不播放） | ✓ | 实测 ~10 MB/s；观察点 CPU 13.8%（AES-GCM + HTTP 开销，符合预期）；空闲内存 30,728 KB |
| 4 | 播放器播放 + 来回拖动 | ✓ | 无失败、无明显卡顿 |
| 5 | **长时间播放 ≥30 分钟** | ✓ | 连续播放 30+ 分钟正常——**覆盖 ≥1 个签名过期边界**（移动签名有效期约 15 分钟），证明「到期前自动换签」（`ensureFreshURL`，expires−60s 主动刷新）在真机实测通过。建议 PM 补充佐证：日志中应有 `vault upstream: 签名已刷新` 行 |
| 6 | **极限压测**（2 播放器 + 网页下载 924 MB 文件） | ✗ **应用崩溃** | 内存持续下降 → SIGSEGV。详见 §2 |

## 2. 崩溃分析

### 2.1 现场数据

- PM 观察：内存减少、应用崩了（压测 = 移动 + 天翼两路加密视频播放，同时浏览器下载 924 MB 加密文件，即 3+ 个并发解密流）。
- 崩溃栈（`docs/review/mini-cloud.log` 行 159–166）：

```
unexpected fault address 0x4003d70000
fatal error: fault
[signal SIGSEGV: segmentation violation code=0x1 addr=0x4003d70000 pc=0x8ade8]
runtime.memmove()                        ← memmove_arm64.s:175
runtime.growslice()                      ← slice.go:283
io.ReadAll({...})                        ← io.go:723
vault.(*Upstream).doRange(...)           ← upstream.go:293
vault.(*Upstream).fetchRange / readOnce / ReadAt
vault.(*DecryptReader).loadChunk / Read
net/http.serveContent → CopyN(730 MB)    ← 播放/下载链路
```

- 崩溃时并发流：goroutine 146（崩溃点，chunk 偏移 0x13a ≈ 330 MB）、318（runnable，chunk 0xd2 ≈ 220 MB）、276/312（IO wait，另两条 serveContent 链路）——**4 个并发解密流，正好占满 max_streams=4**。
- 故障地址 `0x4003d70000` 位于 Go 堆 arena（arm64 起始 0x4000000000）+61.5 MiB 处——堆区写页错误，与内存耗尽一致。

### 2.2 根因：每流内存预算漏账

契约（design §4.1 / upstream.go 注释）按「窗口 4 MiB + 解密块 2 MiB = 6 MiB/流」预算，但实现存在两笔未入账的瞬时占用：

| 每流内存项 | 设计预算 | 实际 | 说明 |
|---|---|---|---|
| 顺序读窗口 `u.buf` | 4 MiB ✓ | 4 MiB | 持久 |
| `doRange` 中 `io.ReadAll` 临时块 | ✗ **漏算** | ≤ 4 MiB+1 | 先整块读入 `data`，再 `append` 进窗口缓冲——两块**同时存在**，且 `data` 依赖 GC 回收（GC 滞后时跨周期叠加） |
| 解密块明文 `r.chunk` | ✓ 含在 2 MiB | 1 MiB | 每块换新，旧块待 GC |
| 密文块缓冲（reader.go:123） | ✗ **漏算** | 1 MiB+16 | `loadChunk` 每读一块 `make` 一个新缓冲，旧块待 GC |
| **每流峰值** | **6 MiB** | **约 10–11 MiB** | |

**4 流 × ~10 MiB ≈ 40–44 MiB，加上 Go 堆 GC 滞后余量（GOGC=100 堆翻倍）与基线占用，峰值 50–60 MiB**。路由器空闲仅 ~30 MB（93 MB cache 可回收但低内存内核下回收不及时，页错误先行触发 SIGSEGV）→ 内核无法为堆增长提供物理页 → 应用崩溃。

**为什么此前未发现**：单元测试为单流、开发机内存充足；`-race` 检测数据竞争而非内存峰值；真机低内存 + 4 并发全速流（下载流以 ~10 MB/s 全量解密 924 MB，每 ~0.4 s 产生一轮 4 MiB 瞬时块）才触发。

**佐证建议**：PM 可在路由器执行 `dmesg | tail -n 20`，若出现 `Out of memory` / `oom-kill` 记录可进一步佐证（当前日志已能独立说明崩溃发生于堆分配路径）。

## 3. 修复方案

| 编号 | 方案 | 内容 | 收益 |
|---|---|---|---|
| **A** | 消除 4 MiB 瞬时双份（核心） | `doRange` 不再 `io.ReadAll` 中转：直接把网络数据读入 `u.buf` 的复用数组（预扩到 length+1，手工限长读循环，保持「读至 EOF/上限」语义），截断判定改用实际读入字节数 `n` | 每流 −4 MiB |
| **B** | 密文块缓冲复用 | `DecryptReader` 增持 `cbuf []byte` 字段（增长即复用，单 goroutine 串行使用），替换 `loadChunk` 内每次 `make` | 每流 −1 MiB |
| **C** | 默认并发上限 4 → 3 | `max_streams` 默认值改 3（config 仍可调高）。压测场景 = 2 播放器 + 1 下载恰为 3 流；4 流多为残留/异常连接 | 峰值 −6 MiB |
| **D**（可选） | 运行时内存软上限 | `debug.SetMemoryLimit(48 MiB)`：Go 1.19+ 特性，堆接近上限时 GC 提前回收，防止内核 OOM 兜底（一行代码，无外部依赖） | 系统级保险 |

**修复后预算**：每流持久 6 MiB（窗口 4 + 明文 1 + 密文复用 1）+ 瞬时 ~1 MiB（明文换块）≈ 7 MiB；**3 流 ≈ 21 MiB + 基线，相对 ~30 MB 空闲留有约 9 MB 余量**。

### 风险与回归测试

- **A 改动 `doRange` 核心路径**，需回归：截断检测（I1）、短窗口续拉（P9/U1）、403 换签（I10）、缓冲命中与失效（U2/P9③）、200 分级（I10）。现有 upstream 测试组（24 组）全部重跑 + 新增「读入复用数组不产生额外分配」类断言（以 `testing.AllocsPerRun` 或行为等价验证）。
- 全套门禁：`go vet`、`go test ./... -race`、ARM64 交叉编译 + UPX。
- **真机复验（闸门⑤）**：重放原压测场景（2 播放器 + 网页下载 924 MB），观察 ≥10 分钟：内存自由量不持续下降、`top` 中 RSS 稳定、无 502/崩溃；随后恢复正常播放。
- D 若采纳：验证 GC 行为无异常（播放中无卡顿、CPU 无异常抬升）。

## 4. 待审核问题

1. §2.2 的内存账与根因判断是否认同？崩溃现场 4 并发流与预算缺口的对应关系是否成立？
2. 方案 A 的「直接读入窗口缓冲」是否与契约④（缓冲计入信号量账）自洽？有无遗漏的共享状态风险（当前 `u.buf` 的 mu 保护域需同步调整）？
3. C 的默认值 3 是否合适（PM 压测场景恰为 3 流）？
4. D（`SetMemoryLimit`）是否采纳？若采纳，48 MiB 的取值是否合理（相对 30 MB 空闲 + 可回收 cache 的取舍）？

## 5. 处置表

| 编号 | 级别 | 摘要 | 处置类别 | 状态 |
|---|---|---|---|---|
| A | 重要 | doRange 读入中转产生 4 MiB/流瞬时双份 | 待审核 | 待处理 |
| B | 建议 | loadChunk 密文缓冲每块新建 | 待审核 | 待处理 |
| C | 建议 | max_streams 默认 4 → 3 | 待审核 | 待处理 |
| D | 可选 | SetMemoryLimit 运行时内存软上限 | 待审核 | 待拍板 |

---

*崩溃日志：docs/review/mini-cloud.log（78 KB，950 行）。测试与修复均未动工，等待审核与 PM 确认。*
