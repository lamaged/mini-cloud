# 崩溃修复复核（RV-2026-11-18-02）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-18-02 |
| 评审类型 | RV-2026-11-18-01（A/B/C/D/E/F/G）处置复核 + 回归检查 |
| 评审对象 | `feature/file-encryption` @ `a33657f47149106e432b6854e53147c4d72cd3ea` |
| 对比基线 | `25f83ef`（崩溃报告基线） |
| 评审方式 | 逐文件静态阅读（`vault/upstream.go`、`vault/reader.go`、`vault/crypto.go`、`webdav.go`、`main.go`、`scripts/procd-mini-cloud.init`、`deploy-router.sh`、测试与文档 diff）；**未编译、未执行** |
| 结论 | **通过（有条件）**：A 严格按 4 条约束实现，B 扩展到明文、E/G 到位、F 已交付；新增 1 条重要（F 与部署健康检查的交互会造成"假成功"）与 4 条建议 |

---

## 1. 处置复核

| 编号 | 意见 | 结论 | 证据 |
|---|---|---|---|
| A | `doRange` 读入中转产生瞬时双份 | **关闭** | ① 填充前 `bufStart/bufEnd = 0` 使缓冲不可读 ✓；② `if cap(u.buf) < length+1 { make }` 一次性预扩、之后只切片不 `append` ✓；③ 截断判定改用实际 `n` ✓；④ 网络读取在 `u.mu` 之外 ✓。缓冲所有权与发布时机符合评审要求 |
| B | 解密块每块新建 | **关闭（扩展到位）** | `crypto.go` 增 `openInto(dst,…)`（`gcm.Open(dst[:0],…)`）；`reader.go` 持 `cbuf`/`pbuf` 两个 grow-only 字段，每块零分配；注释明确 `r.chunk` 与 `pbuf` 别名的串行约束 |
| C | `max_streams` 4→3 | **暂缓（符合评审）** | 默认值未改；PROGRESS 记为"先实测" |
| D | `SetMemoryLimit` | **待实测后拍板** | 未实施；PROGRESS 记 64 MiB 建议 |
| E | ctx 取消被当上游异常 | **关闭** | `fetchRange` 入口与失败后各判 `ctx.Err()` → 直接返回，不刷新不重试；`webdav` 把 `Canceled/DeadlineExceeded` 降为 Debug；`TestUpstreamCancelNoRefresh` 断言 `linkCalls` 不增 |
| F | 崩溃即长期宕机 | **已交付，但有交互缺陷（见 I1）** | `scripts/procd-mini-cloud.init`（`USE_PROCD`、`respawn 3600 5 5`、`limits core="0"`、`/bin/sh -c "exec …"` 保持 PID、日志重定向）；`deploy-router.sh` 三处 procd 感知（start/stop/rollback） |
| G | 缺运行时内存观测 | **关闭** | `-debug` 下每 5 分钟 `slog` HeapInuse/HeapSys/NumGC/goroutines；`runtime` 导入正确 |

**附带核实**：崩溃报告已按评审更正内存账（ReadAll 峰值 ≈7 MiB、解密块非漏算），并把 A–G 并入 §3/§5；`.gitignore` 增 `docs/review/*.log`（日志含签名 URL，不入库 ✓）。

---

## 2. 新增重要

### I1　装上 procd 后，部署健康检查可能"假成功"

- **位置**：`scripts/deploy-router.sh` / `health_check`（判据 = 新进程存活 + 新增日志出现 "Gateway 启动"）× `scripts/procd-mini-cloud.init`（respawn）。
- **场景**：新二进制"能启动、随后崩溃"（如 manifest 解密 panic、启动期 OOM）。procd 会立即拉起（threshold=3600 内），于是：
  1. 日志里反复出现 `Gateway 启动` 行 → 健康检查的"启动行"判据满足；
  2. 轮询时刻进程可能恰好在运行 → "PID 存活"判据也满足；
  3. 脚本报"部署成功"、**不回滚**，而实际是 crash-loop，5 次后 procd 放弃 → 服务停摆且无人知晓。
- **影响**：这是 F 引入的**部署安全网退化**——此前"新进程起不来 → 自动回滚"的演练（RV-05 §5）在 procd 模式下不再可靠。
- **要求**（任选其一即可，成本都很低）：
  - 健康通过后**再等 3–5 s 复检一次 PID 是否相同**（respawn 会换 PID，PID 变化即判失败）；
  - 或统计**新增**日志中 `Gateway 启动` 出现次数必须恰为 1，且不得出现 `fatal error` / `panic:`；
  - 两种判据都建议同时加 `procd` 是否处于 running（`/etc/init.d/mini-cloud status` 或 `ubus call service list`）。

---

## 3. 建议

| 编号 | 内容 | 建议 |
|---|---|---|
| P1 | A 的读循环缺"零进展"防御 | `for n < len(data) { m, err := Read(…); n += m; … }`：若底层返回 `(0, nil)` 会死循环（标准库 `io.Copy` 正是为此设了 `ErrNoProgress`）。建议加连续零读计数（如 100 次后返回错误），或在注释里说明"依赖 net/http body 不返回 (0,nil)" |
| P2 | B 缺"零分配"回归断言 | 报告 §3 自己提出用 `testing.AllocsPerRun` 锁定，但代码里没有对应用例。建议补一条：连续 `loadChunk` N 次，`AllocsPerRun` 不随 N 增长（或断言 `m.TotalAlloc` 增量 < 阈值），否则将来改回每块 `make` 无人发现 |
| P3 | F 未自动安装 | `deploy-router.sh` 只**检测** `/etc/init.d/mini-cloud`，不安装；PM 忘记手工部署时 F 静默失效。建议：检测到 staging 含 `procd-mini-cloud.init` 且目标不存在时，打印明确安装指令（或直接 `cp + chmod +x + enable`，并在输出中声明"已安装守护"） |
| P4 | 崩溃报告数字前后不一致 | §2.2 表格已更正为"每流峰值 ~12–13 MiB"，但紧随其后的段落仍写"4 流 × ~10 MiB ≈ 40–44 MiB"；§3 又写"3 流 ≈ 21 MiB"而 C 已暂缓（实为 4 流 ≈ 24 MiB）。建议统一口径，避免后续引用再起争论 |
| P5 | G 的采样间隔对压测偏粗 | 5 分钟间隔 × 10 分钟压测 = 2 个采样点，无法画出内存曲线。建议 `-debug` 下改为 60 s（或加 `-memlog=30s` 参数），压测时才有足够分辨率 |

---

## 4. 回归核对

| 项 | 结论 |
|---|---|
| 契约④（每流 6 MiB） | A+B 后实测口径：`u.buf` 4 MiB+1 + `cbuf` 1 MiB+16 + `pbuf` 1 MiB ≈ 6.02 MiB ✓ 与 design §4.1 一致；瞬时无额外双份 ✓ |
| 缓冲正确性 | 填充前置无效 → 发布在锁内一次完成 ✓；`u.buf = data`（len=n，cap=length+1）后续仍可复用 ✓ |
| EOF/截断语义 | 截断判定用 `n` ✓；`c < len(p)` 时按 `crTotal` 区分续拉/EOF ✓（P7/P9 语义保持）；416→EOF 未受影响 ✓ |
| 200 分级 | `discardServe` 未改，仍读入调用方缓冲 ✓ |
| 取消语义（E） | 入口短路 + 失败后短路 ✓；`webdav` 仅对 `Canceled/DeadlineExceeded` 降级，其余仍 ERROR ✓ |
| 信号量与 Close | `doRange` 全程不持 `mu` 做网络 IO ✓，Close/信号量路径不再被长阻塞 ✓ |
| 测试 | 新增 `TestUpstreamCancelNoRefresh` ✓；既有 24 组（跨窗口/跳跃/短窗口/403/416/截断/总额未知）在 A 改动后逻辑等价，按提交说明全绿 |
| 文档 | 崩溃报告更正 ✓、PROGRESS 处置表 A–G ✓、评审报告归档 ✓ |

---

## 5. 放行建议

| 阶段 | 建议 |
|---|---|
| 本轮修复 | A/B/E/G 可视为已完成；**I1 建议在真机压测前修**（否则复验本身可能被假成功误导） |
| 真机复验（闸门⑤） | 重放压测（2 播放器 + 924 MB 下载）≥10 分钟，采集：`-debug` 的 memstats、`GODEBUG=gctrace=1`、每 30 s `MemAvailable`/RSS、`dmesg | tail`；**判定标准补充**：RSS 峰值、GC 次数、无 fatal、无 502 |
| 后续 | D 按实测决定（64 MiB）；C 保持暂缓；P1–P5 可随下次触及该文件一并处理 |

---

## 6. 处置表

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | procd 下健康检查可能假成功（crash-loop 被判通过、不回滚） | | | 待处理 | | |
| P1 | 建议 | A 读循环缺零进展防御 | | | 待处理 | | |
| P2 | 建议 | B 缺"零分配"回归断言 | | | 待处理 | | |
| P3 | 建议 | procd 脚本未自动安装 | | | 待处理 | | |
| P4 | 建议 | 崩溃报告数字口径需统一 | | | 待处理 | | |
| P5 | 建议 | memstats 采样间隔偏粗 | | | 待处理 | | |

---

*本报告基于 `a33657f` 的静态阅读，未编译、未执行；内存数字按代码路径推算，真机实测以压测复验为准。*
