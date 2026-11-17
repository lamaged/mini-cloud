# Vault Phase 0 评审闭环确认（RV-2026-11-17-06）

| 项目 | 内容 |
|---|---|
| 评审编号 | RV-2026-11-17-06（闭环确认） |
| 评审类型 | 阶段收束：确认 RV-2026-11-17-02 ~ 05 全部意见关闭，移交 Phase 1 输入清单 |
| 评审对象 | `feature/file-encryption` @ `d4cc8200ec6bde77651be1852075301dbf92c41f` |
| 覆盖范围 | Vault Phase 0 全部代码（`internal/vault/*`、`cmd/mcrypt`、`conf.Vault`、`model.Obj`、`webdav` 接线、`main` 装配）与相关文档 |
| 评审方式 | 逐轮静态阅读（diff + 关键文件全文）；**未编译、未执行**；AX6 性能与端到端数据以提交说明为准 |
| 结论 | **Phase 0 评审闭环**：4 轮共 30 条意见全部关闭，无未决 I 级事项；剩余 P 级 3 条并入 Phase 1 设计评审 |

---

## 1. 各轮关闭情况

| 轮次 | 意见 | 关闭情况 | 关键证据（最终提交） |
|---|---|---|---|
| RV-2026-11-17-02 | I1–I3、P1–P6（9 条） | 全部关闭 | `Metadata.H` 废弃、权威哈希唯一真值源 = `manifest.Entry.SHA256`；`CipherSize` 非对齐公式修正；`EncryptObject` 强制 `meta.S`；`keygen` 显式 chmod；`out⊂in` 拒绝；短读/`c==0` 防御 |
| RV-2026-11-17-03 | P7–P10（4 条） | 全部关闭 | 报告版本统一 v1.3；PROGRESS 状态区清理；顺序流式契约落 `design §4.1`；加密失败删除半成品 |
| RV-2026-11-17-04 | I1–I4、P11–P18（12 条） | 全部关闭 | URL 型驱动跳过 vault 装配（不接管同名目录）；`ErrTooManyStreams → 503`；GET/PROPFIND ETag 同源；报告围栏修复；`VaultConfig` 改指针；children 索引；`e.Obj` 前缀校验；验收口径与 bench 口径落文 |
| RV-2026-11-17-05 | I5、P19–P22（5 条） | 全部关闭 | 空 MIME 的 nil-map panic 修复（显式构造 `http.Header`）+ 空 MIME 测试；doc 示例删 `v.Start`；JSON 示例恢复合法；信号量前置入契约；`List` 返回副本 |

**合计 30 条：重要（I）9 条、建议（P）21 条，全部关闭。**

---

## 2. 移交 Phase 1 的输入（不属 Phase 0 缺陷）

| 来源 | 内容 |
|---|---|
| P9（RV-17-03） | 顺序流式 `RangeSource` 四条验收口径（缓冲 4–8 MiB、相邻偏移差判定、失效时机、缓冲计入 `max_streams`） |
| P21（RV-17-05） | 信号量必须在 `raw.Link` 之前获取（避免超限拒绝时白开上游连接） |
| P22（RV-17-05） | `Vault.Warning()` 接入 `/admin` 展示（当前仅日志；已记 TODO 技术债） |
| TODO 技术债 | 移动驱动「过期 token + 无缓存」时 `Init` 的 `discoverCloudHost` 可能启动失败，与「过期 → 空目录 + 提示」承诺不符 |

---

## 3. 评审未覆盖事项（已在相应文档留痕）

- 55 GB 用例执行环境为**开发机**（AX6 可用存储仅数 MB），真机 55 GB 用例在 Phase 1 接天翼家庭后验证（报告 §9）。
- AX6 AES-256-GCM `Seal 384.6 / Open 383.8 MB/s` 口径：单线程、1 MiB 分块、纯内存无 I/O（PROGRESS「进行中」）。
- v0.10.0 验收第 5 项「更新后立即恢复」仍为**待生产首验**（等移动 authorization 真实过期时补勾）。

---

## 4. 放行意见

- **Vault Phase 0**：代码与文档评审通过，可进入 Phase 1 设计评审（闸门①）。
- **建议**：本轮起不再单独开启 Phase 0 复核轮次；后续变更随 Phase 1 设计评审与代码审核一并覆盖。

---

*本确认基于 `d4cc820` 的静态阅读，未编译、未执行、未接触真实凭证。*
