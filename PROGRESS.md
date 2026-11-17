# PROGRESS.md

> 开发进度与审核意见处置记录（专业语言），供外部审核员与程序员使用。
> PM 版进度由程序员在对话中另行汇报（交易语言）。

## 当前版本

- 代码：v0.10.0 已发布（tag v0.10.0，真机验收通过）；Vault Phase 0 **库层完成**（格式/加密/读取器/mcrypt，经 RV-17-02/03 审核闭环）
- 文档：设计文档 v0.3.2、可行性报告 v1.3

## 进行中

- **Vault v1.0.0 Phase 0**（闸门① PM 已批准）：库层完成；**待接线**——local 驱动接入 + Vault 包装层 + webdav 接线（_mc 403、HTTP 206、ETag/MIME 唯一来源、max_streams 信号量），接线后方可执行 Phase 0 验收清单（含 AX6 benchmark）

## 已完成

| 日期 | 事项 |
|---|---|
| 2026-11-15 | 建立角色/边界/协作规则与审核闭环（docs/process.md）；创建设计文档 v0.1 |
| 2026-11-16 | 确定开发分支 `feature/file-encryption`（master 不动）；文档初始化提交 aeb928e 并推送 |
| 2026-11-16 | 外部文档评审 RV-2026-11-16-01（结论：有条件通过）；按意见完成文档修订（D1–D5、I1–I7、P1–P6）；PM 五项拍板记录于 TODO.md |
| 2026-11-16 | 复核评审 RV-2026-11-16-02（结论：有条件通过第二轮）；14 条新意见（D6、I8–I14、P7–P12）处置完成；PM 追加拍板（fileID 校验提前 Phase 0、内网地址允许入库）；RV-01 状态列更新（13 关闭 / 5 部分关闭） |
| 2026-11-16 | **闸门① 通过（PM 批准），v0.10.0 实现完成**：两阶段 CredentialUpdater + internal/admin 管理页（CSRF、html/template 转义）+ mobile 实现（临时客户端验证、按 __req 隔离、凭证/缓存代次防护）+ conf 原子写/0600/备份 + 3 处转义修复（buildHref 百分号编码、目录页/根页转义）。自查（/code-review high）15 条全部修复；`go vet` + `go test -race` 全绿；ARM64 交叉编译通过；实现期修订记录于 design.md §3.8 |
| 2026-11-16 | **闸门③ 外部代码审核 RV-2026-11-16-03（结论：有条件通过）**：I1–I6 全部修复（更新串行化、LoadStrict 严格读取、目录 fsync、WriteCredential 测试、401×提交竞态测试、提交失败 .bak 回滚），P1–P9 全部落实（状态码、安全响应头、version v0.10.0、Stop 幂等、字段同步、编码去重、24h 门槛、文档修正、自查留痕表）；`go vet` + `go test -race` 全绿；ARM64 交叉编译通过 |
| 2026-11-16 | **部署脚本完成（PM 拍板：审核后立即写、单独提交）**：scripts/deploy-prepare.sh（开发机编译+UPX+暂存，端到端跑通）+ scripts/deploy-router.sh（路由器拉取+备份+重启+健康检查+失败回滚，busybox ash 兼容，语法与健康检查解析本地验证通过）；体积观察项（铁律 10）：ARM64 原始 8,716,472 B → UPX 后 2,974,696 B（v0.9.2 基线 UPX 约 2.3 MB，增量主要来自 html/template 与凭证相关代码）；真机验证属闸门⑤ |
| 2026-11-17 | **Vault Phase 0 复核 RV-2026-11-17-03（通过）**：I1/I2/I3、P1–P6 全部闭环（磁盘格式零变更，旧对象无需重加密）；P7 报告版本统一 v1.3；P8 PROGRESS 状态区清理；P9 顺序流式契约细节落 design §4.1；P10 meta.S 强制一致 + 加密失败删除半成品 |
| 2026-11-17 | **Vault Phase 0 核心审核 RV-2026-11-17-02**：读取器设计获确认（随机 Seek O(1) 内存/打开即校验/ServeContent 契约对齐）；I1（Metadata.H 废弃，哈希唯一真值源=manifest）、I2（CipherSize 非对齐公式修正）修复；P1–P6 全部落实（短读防御/keygen 权限/out-in 拒绝/符号链接提示/死循环防御/文档注记）；I3 PM 拍板方案 (b)（Phase 1 顺序流式 RangeSource）；本地端到端（100MB+空目录+嵌套）diff -r 完全一致；x86 参考 Seal 1928 MB/s / Open 2492 MB/s |
| 2026-11-17 | **发布复核 RV-2026-11-17-01（闸门⑤通过/闸门⑥收尾）**：RV-05 全部关闭；真机修复方向获确认；PM 拍板发布策略（tag 打 feature 分支、全部开发完成后一次合入 master、真机运行 ≥1 个月方可称正式版、v0.10.0 为回退基线）；P19 撤回（302 模式播放不加内存属预期）；I13 降为可选加固并落实 kill \$(pgrep -f) 停服；P20 README 补部署脚本指引；I12 保留待生产首验 + 启动健壮性问题记入技术债；**tag v0.10.0 已打并推送** |
| 2026-11-17 | **闸门⑤ 真机验收通过**：§1 部署与启动（v0.10.0 日志确认、UPX 2,974,696 B、断开 SSH 存活）、§2 管理页 8/9 项通过（「更新后立即恢复」因旧授权未过期待生产首验）、§3 播放器回归正常、§4 空闲 RSS 16,620 kB / 播放 RSS 15,836 kB、§5 回滚实战验证（首轮误判自动恢复）；grep 确认无空 cloud 块 |
| 2026-11-17 | **真机验收（闸门⑤）进展**：部署成功（v0.10.0 启动日志确认，UPX 2,974,696 B）；验收清单 §1/§2 核心项通过——管理页 1/2/3/4/6 项全过（根导航+管理链接、/admin、错误格式拒绝、正确新授权更新成功、重启后仍有效）、权限 0600 三处核对通过、断开 SSH 后服务存活（PID 31395）；第 5 项（更新后立即恢复）因旧授权未过期无法验证，标注待生产首验；回滚机制已在首轮误判时实战验证有效；§2 空 cloud 块检查通过（grep 输出 0）；回滚演练经首轮实战验证勾销；§4 空闲 RSS 16,620 kB（16.2 MB）已记录；剩余：播放时 RSS 与 §3 播放器回归待 PM 实测 |
| 2026-11-17 | **真机验收首轮（闸门⑤）**：v0.10.0 在 AX6 实测启动正常（四个云盘就绪、日志确认 version=v0.10.0）；部署脚本健康检查在 AX6 失败（根因：BusyBox wget v1.25.1 无 -S 参数；另确认路由器无 setsid/pkill，脚本回退路径有效）；已改为「进程存活 + 日志 Gateway 启动行」检查并在本机用真实二进制验证通过（exit=0）；回滚逻辑实测有效（旧版本 v0.9.2 已自动恢复运行） |
| 2026-11-16 | **第五轮复核 RV-2026-11-16-05（Go 代码侧通过，闸门③ 收尾）**：I11 函数前移修复（启动失败路径回滚闭环）；P15–P18 全部落实（setsid 统一启动、回滚后健康验证、同目录原子替换、归档勘误）；P10 经 tree API 核对确认为评审误报并撤回 |
| 2026-11-16 | **闸门③ 复核 RV-2026-11-16-04（结论：通过）**：RV-03 的 15 条全部关闭；新增 I7–I10（锁位置、回滚如实报告、部署脚本进程生命周期与日志、验收清单口径）与 P11–P14 全部处置；P10 驳回（依据：git 记录显示脚本提交模式为 100755 可执行，与审核所称 644 不符）；RV-02/RV-03 状态列随本轮复核更新为已关闭 |

## 审核意见处置记录

> 模板：编号｜级别｜摘要｜处置类别｜处置说明｜状态｜复核｜日期
> 处置类别取值：`已改` / `补充` / `可选（暂不处理）` / `待拍板` / `已拍板`（待拍板经 PM 裁决后）/ `驳回（附依据）`
> 状态取值：`待处理` / `已裁决` / `文档修订完成` / `待复核` / `已关闭`。关闭规则（docs/process.md §2.4）：外部审核员下一轮复核通过后，状态改为「已关闭」并记录复核编号/日期。

### RV-2026-11-16-01（18 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| D1 | 阻断 | URL 缓存 TTL 在 Vault 上层不可实现 | 已改 | Vault 自管「URL+绝对过期时间」表（TTL=min(10min,expires-60s)），不走 CachedReader 链接缓存；design.md §4.1、报告 §5.4/§8.2 | 已关闭 | RV-02 | 2026-11-16 |
| D2 | 阻断 | WriteFile 改 0600 对存量文件无效 | 已改 | 写入后显式 Chmod 0600（.bak 同）；闸门⑤ 增真机权限核对项；design.md §3.3/§3.7 | 已关闭 | RV-02 | 2026-11-16 |
| D3 | 阻断 | PROPFIND 属性不经过 Link.Header | 已改 | ETag/MIME 唯一来源=Vault 虚拟树（model.Obj 增可选字段）；prop.go 入修改清单；禁透传上游头；design.md §4.3、报告 §7.6 | 已关闭 | RV-02 | 2026-11-16 |
| D4 | 阻断 | raw.Name() 是类型名非挂载名；示例有语法错 | 已改 | 示例改用 cc.MountName()，修正语法与 error 处理；签名统一 `vault.New(cached, raw, vaultCfg)`；design.md §4.1 与报告 §8.2 逐字一致 | 已关闭 | RV-02 | 2026-11-16 |
| D5 | 阻断 | 凭证三处状态的提交顺序与并发模型未定义 | 已改（部分关闭） | 顺序与 cfg 加锁已写；接口与流程矛盾 → 升为 RV-02 D6 重新闭环 | 部分关闭（见 RV-02 D6） | RV-02 | 2026-11-16 |
| I1 | 重要 | fileID/nonce 不变量未写死 | 已改 | 报告 §7.3 格式不变量 1：fileID 每次新生成、禁复用、工具拒绝同 fileID 重复加密 | 已关闭 | RV-02 | 2026-11-16 |
| I2 | 重要 | 空文件与头部无认证、字段无校验 | 已改 | manifest 增 header_sha256；头部字段校验规则；Phase 0 验收补空文件与篡改拒绝项；报告 §7.3 | 已关闭 | RV-02 | 2026-11-16 |
| I3 | 重要 | manifest 未与对象绑定，可跨对象替换 | 已改（部分关闭） | 绑定字段自 Phase 0 就位；fileID 一致性校验提前 Phase 0 → RV-02 I12 | 部分关闭（见 RV-02 I12） | RV-02 | 2026-11-16 |
| I4 | 重要 | _mc 未屏蔽（PROPFIND 可见/GET 可直取） | 已改 | PROPFIND 虚拟树不出现 _mc；GET _mc/未记录路径=403 绝不 302；Phase 0 验收项；报告 §7.2 | 已关闭 | RV-02 | 2026-11-16 |
| I5 | 重要 | Link.StatusCode 表述与代码不符 | 已改 | CLAUDE.md 措辞：仅用于非 seekable 流（streamReader）；seekable 由 ServeContent 计算 200/206/416 | 已关闭 | RV-02 | 2026-11-16 |
| I6 | 重要 | 上游异常分支与 max_streams 语义缺失 | 已改（部分关闭） | 表与全局信号量已补；200 分支不可行与重试语义 → RV-02 I10/I11 | 部分关闭（见 RV-02 I10/I11） | RV-02 | 2026-11-16 |
| I7 | 重要 | Stop 需幂等且所有权唯一 | 已改 | CachedReader.Stop 改 sync.Once；所有权链 main→Handler.Close→Vault.Stop→CachedReader.Stop；补「重复 Stop 不 panic」单测；design.md §4.1、报告 §8.3 | 已关闭 | RV-02 | 2026-11-16 |
| P1 | 流程 | 缺文档评审环节与归档位置 | 已改 | 闸门① 增外部文档评审项；§2.3 增 D/I/P 分级与 docs/review/ 归档规则 | 已关闭 | RV-02 | 2026-11-16 |
| P2 | 流程 | PROGRESS.md 缺结构化留痕格式 | 已改 | 处置表模板落地；状态枚举与关闭规则随 RV-02 P10 补全 | 已关闭 | RV-02 | 2026-11-16 |
| P3 | 流程 | TODO.md 缺 4 条待拍板项 | 已拍板 | 四项均 PM 拍板（2026-11-16），决定与理由记录于 TODO.md 拍板记录 | 已关闭 | RV-02 | 2026-11-16 |
| P4 | 流程 | 三处既有转义缺陷应拆出独立修复 | 已改 | design.md §3.6 独立修复清单（独立提交/独立验收，纳入 v0.10.0）；报告 §8.2 修改表同步 | 已关闭 | RV-02 | 2026-11-16 |
| P5 | 流程 | env.sh 未入库、内网代理不宜入库 | 已改（部分关闭） | env.example.sh 已入库、代理说明已泛化；新增内网地址入库 → RV-02 P9 | 部分关闭（见 RV-02 P9） | RV-02 | 2026-11-16 |
| P6 | 流程 | 文档布局、命名与引用方式 | 已改（部分关闭） | 改名与「文件+符号」引用完成；相对链接随目录变更失效 → RV-02 I8 | 部分关闭（见 RV-02 I8） | RV-02 | 2026-11-16 |

### RV-2026-11-16-02（14 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| D6 | 阻断 | 凭证写回顺序与接口矛盾、无回滚方法 | 已改 | CredentialUpdater 改两阶段接口：ValidateCredential（零状态变更，10s 超时）+ CommitCredential（生效并落 token state，调用点唯一）；§3.3 流程零回滚；§3.4 移动实现改为临时 Client 验证；§3.7 验收 8 | 已关闭 | RV-04 | 2026-11-16 |
| I8 | 重要 | 报告改名后约 25 处相对链接 404 | 已改 | 报告内代码链接全部改为 `../` 相对路径（随 docs/ 目录修正） | 已关闭 | RV-04 | 2026-11-16 |
| I9 | 重要 | vault.paths 格式（有无前导斜杠）两处冲突 | 已改 | 统一为无前导斜杠目录名；匹配规则定稿（挂载内相对路径首段比较、大小写敏感）；design §4.3、报告 §1.1/§7.8、TODO 拍板记录同步 | 已关闭 | RV-04 | 2026-11-16 |
| I10 | 重要 | 上游 200 时「丢弃到偏移」在 55 GB 场景不可行 | 已改 | 分级策略：换新签名重发一次 → ≤2 MiB 允许丢弃 → 否则 502+日志；Content-Range 不符先失效 URL（共用刷新锁）；不做整文件顺序转发 | 已关闭 | RV-04 | 2026-11-16 |
| I11 | 重要 | Vault 绕过 RetryReader；「List 走 cached」注释矛盾 | 已改 | 路径分流规则：vault 前缀 → List 读内存虚拟树、Link 走 raw；其余路径委托 cached；Vault 对 raw.Link 网络错误重试 1 次、not found 不重试；注释同步 design §4.1、报告 §8.2 | 已关闭 | RV-04 | 2026-11-16 |
| I12 | 重要 | fileID 一致性校验建议提前到 Phase 0 | 已拍板 | PM 同意（2026-11-16）：fileID 一致性 → Phase 0 必做（报告 §7.3 不变量 4、§9 验收）；metadata.p 路径比对与失败处置仍 Phase 1 | 已裁决（RV-04 复核落实） | RV-04 | 2026-11-16 |
| I13 | 重要 | §11「三家均 206」与 §4.5「百度待测」矛盾 | 已改 | 结论表口径修正：已测三家（天翼个人/家庭、移动）均 206；百度走代理路径，Range 待实测 | 已关闭 | RV-04 | 2026-11-16 |
| I14 | 重要 | key_id/flags/reserved 无字段语义 | 已改 | 报告 §7.3 定稿：key_id Phase 0 恒零、Phase 2 轮换启用（SHA-256(masterKey)[0:8]）；flags 非零即拒绝；reserved 忽略但保留 | 已关闭 | RV-04 | 2026-11-16 |
| P7 | 流程 | §6 风险表仍写「待 PM 确认」 | 已改 | 改为「已拍板（TODO 2026-11-16）」，与 §4.3/TODO 一致 | 已关闭 | RV-04 | 2026-11-16 |
| P8 | 流程 | 铁律 10 的两项观察项未进入闸门⑤ 清单 | 已改 | 闸门⑤ 增两条勾选项：记录 UPX 后体积、记录空闲与 4 路并发 RSS，均写入 PROGRESS.md | 已关闭 | RV-04 | 2026-11-16 |
| P9 | 流程 | 内网地址与 SSH 密码登录入库，与 P5 处置矛盾 | 已拍板 | PM 拍板（2026-11-16）：内网地址允许入库（仅私有网段、无凭证）；SSH 表述统一「免密优先，密码回退」；报告 §12.B 加「测试环境地址」注记 | 已裁决（RV-04 复核落实） | RV-04 | 2026-11-16 |
| P10 | 流程 | 处置类别越出模板；「待复核」缺关闭规则 | 已改 | 模板补「已拍板」类别与状态取值；process.md §2.4 增关闭规则；RV-01 状态列据此更新 | 已关闭 | RV-04 | 2026-11-16 |
| P11 | 流程 | §8.2 静默删除 getObj 错误处理修改项 | 已改 | 报告 §8.2 prop.go 行恢复「getObj 错误处理（List 失败不得返回伪目录，Vault 降级路径依赖）」 | 已关闭 | RV-04 | 2026-11-16 |
| P12 | 流程 | 归档件铁律条数、env 模板 GOPATH 两处小不一致 | 已改 | 归档件 r1 加「aeb928e 快照」注记；CLAUDE.md GOPATH 表述修正为 /home/test/go-path | 已关闭 | RV-04 | 2026-11-16 |

### RV-2026-11-16-03（代码审核，15 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 双标签并发提交可致 config 与运行时不一致 | 已改 | Handler 增 `updateMu` 串行化验证→写盘→生效全流程；补 TestUpdateSerialized（A 阻塞时 B 被锁） | 已关闭 | RV-04 | 2026-11-16 |
| I2 | 重要 | 写回路径在 config.json 缺失时重建默认配置 | 已改 | 新增 `conf.LoadStrict`（缺失/损坏直接报错，绝不重建）；写回路径改用 `WriteCredential`；补 TestWriteCredentialMissingFile | 已关闭 | RV-04 | 2026-11-16 |
| I3 | 重要 | Save 未 fsync 父目录，断电后 rename 可能丢失 | 已改 | `writeFileAtomic` 在 rename 后 fsync 父目录（尽力而为 + WARN）；Save/Backup/RestoreBackup 统一走原子写；design §3.3 同步 | 已关闭 | RV-04 | 2026-11-16 |
| I4 | 重要 | persist 闭包（最高风险写路径）无测试 | 已改 | 抽为 `conf.WriteCredential`，补 5 个用例（正常写入且其它条目保留/挂载不存在不改文件/旧版 cloud 迁移等价/文件缺失不重建/无效 JSON 报错） | 已关闭 | RV-04 | 2026-11-16 |
| I5 | 重要 | 401 刷新 × 提交的代次防护无测试 | 已改 | 回调提取为 `handleAuthFailed` 方法 + `Client.refreshURL` 可注入；补 TestAuthRefreshRaceWithCommit（阻塞刷新端点，断言最终内存与 token state 均为已提交凭证） | 已关闭 | RV-04 | 2026-11-16 |
| I6 | 重要 | 提交失败时 config 已改，与「零变化」承诺不符 | 已改（PM 拍板方案①） | PersistFunc 返回回滚函数；提交失败时 admin 调用回滚用 `.bak` 还原配置并同步内存副本；补 TestCommitPlainErrorRollsBack；design §3.3/§3.7-8 同步 | 已关闭 | RV-04 | 2026-11-16 |
| P1 | 建议 | 错误分支统一 200 | 已改 | 400 输入错误（表单/CSRF/挂载）；502 验证失败；500 写盘/提交失败；200 成功；测试同步 | 已关闭 | RV-04 | 2026-11-16 |
| P2 | 建议 | 缺 no-store / X-Frame-Options / CSP | 已改 | 管理页返回三条安全头；补 TestPageSecurityHeaders | 已关闭 | RV-04 | 2026-11-16 |
| P3 | 建议 | version 仍 v0.9.2 | 已改 | 升 `v0.10.0`（README 变更记录留闸门⑥） | 已关闭 | RV-04 | 2026-11-16 |
| P4 | 建议 | Stop 未 sync.Once，文档称已改 | 已改 | `CachedReader.Stop` 改 sync.Once（提前于 Vault Phase 0 落地）；补 internal/fs/cache_test.go（重复 Stop 不 panic） | 已关闭 | RV-04 | 2026-11-16 |
| P5 | 建议 | d.authorization 提交后未同步 | 已改 | CommitCredential 在 commitMu 内同步 `d.authorization` | 已关闭 | RV-04 | 2026-11-16 |
| P6 | 建议 | 路径编码两份实现 | 已改 | 统一为 `webdav.EncodePathSegments`（导出），main 复用，删除 main 侧 encodeURLPath | 已关闭 | RV-04 | 2026-11-16 |
| P7 | 建议 | 即将过期 token 判为验证成功 | 已改（PM 拍板 24h） | 剩余有效期不足 24 小时拒绝接收（提示「请获取新的」）；补 TestValidateAuthorizationTooShort | 已关闭 | RV-04 | 2026-11-16 |
| P8 | 建议 | design §3.4 与 Commit 实现不符 | 已改 | §3.4-2 改为「应用 Validate 阶段暂存的凭证（含云主机）→ saveTokenState；不联网」 | 已关闭 | RV-04 | 2026-11-16 |
| P9 | 建议 | 自查 15 条无逐条留痕 | 已改 | 补下表（自查留痕） | 已关闭 | RV-04 | 2026-11-16 |

### 自查留痕（RV-03 P9：v0.10.0 实现期 /code-review high 的 15 条）

| # | 问题 | 处置 | 涉及文件 | 测试 |
|---|---|---|---|---|
| 1 | .bak/.tmp 含凭证未进 .gitignore | 加入 .gitignore | .gitignore | — |
| 2 | 提交后 CachedReader 缓存不失效（30s 空目录假象） | `InvalidateAll` + admin `OnCommitted` | fs/cache.go、admin、main | TestOnCommittedCalled |
| 3 | 单一 pending 槽并发串号 | 按 `__req` 键控 + 5 分钟清理 | mobile/credential.go | TestPendingIsolationByReq |
| 4 | Load 保留默认 Cloud 指针 → 伪 cloud 块 | normalize 在 clouds 存在时清空 Cloud | conf/config.go | TestSaveOmitsEmptyCloud |
| 5 | 目录页 href 未编码（#/? 链接错） | escPath/EncodePathSegments 逐段编码 | webdav | TestServeDirHTMLEscapesNames、TestBuildHrefEscapesSpecialChars |
| 6 | persist 失败留脏内存配置 | 写盘成功后才更新内存副本 | main.go | conf.WriteCredential 测试（RV-03 I4） |
| 7 | Validate 的 ctx 未传导（10s 超时失效） | personalRequest/discoverCloudHost 接受 ctx | mobile/api.go、auth.go | —（代码结构保证） |
| 8 | 提交普通错误被显示为「更新成功」 | ErrStateWriteFailed 哨兵区分 | driver、admin | TestUpdateCommitPlainErrorNotSuccess |
| 9 | state 写失败重启恢复旧账号 | 提交时删除残留旧缓存文件 | mobile/credential.go | TestCommitCredentialStateWriteFailure |
| 10 | 401 刷新覆盖刚提交的凭证 | credGen 代次 + handleAuthFailed 恢复 | mobile/driver.go | TestAuthRefreshRaceWithCommit（RV-03 I5） |
| 11 | Save 吞 fsync 错误、rename 后 chmod | chmod 前移 + 每步错误返回 + 目录 fsync | conf/config.go | TestSavePermissions0600 |
| 12 | 在途路径解析写入旧账号 ID | cacheGen 代次作废 | mobile/driver.go | —（与 #10 同机制） |
| 13 | 根页面/根 PROPFIND 前缀未编码 | EncodePathSegments + xmlText | main.go | TestServeRootPropfindEscapesXML、TestServeRootIndexEscapesHTML |
| 14 | Load 不收紧存量 0644 | Load/LoadStrict 启动 chmod 0600 | conf/config.go | TestSavePermissions0600（存量分支） |
| 15 | 管理页写回覆盖运行期手工修改 | 写回以磁盘当前内容为基准 | main.go、conf | TestWriteCredentialNormal（重新 LoadStrict 断言） |


### RV-2026-11-16-04（复核 + 部署脚本审核，9 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I7 | 重要 | updateMu 在 ParseForm 前获取，慢请求占锁 | 已改 | 锁移至 CSRF 校验后、Validate 前；http.Server 增 ReadHeaderTimeout 10s | 已关闭 | RV-05 | 2026-11-16 |
| I8 | 重要 | 回滚失败仍显示「配置已还原」 | 已改 | rollback 改 func() error；失败明示「回滚失败，请手工核对 config.json（.bak 仍保留）」；补 TestCommitPlainErrorRollbackFails | 已关闭 | RV-05 | 2026-11-16 |
| I9 | 重要 | 部署脚本：未脱离会话/sleep 3 太短/健康检查不分新旧进程/日志丢弃 | 已改 | start-stop-daemon（setsid 回退）脱离会话；旧进程 kill -0 确认退出；新 PID 校验；健康检查轮询 15×1s；日志落 mini-cloud.log | 已关闭 | RV-05 | 2026-11-16 |
| I10 | 重要 | 验收清单两处与脚本不一致 | 已改 | §1 改为 grep mini-cloud.log 核对版本 + 断开会话存活验证；§5 回滚演练改为「能执行但启动即退出的假产物」（空文件会被拒，注明） | 已关闭 | RV-05 | 2026-11-16 |
| P10 | 建议 | 脚本文件模式 644 | 驳回（附依据） | git 提交记录为 create mode 100755（可执行位），与审核所称 644 不符；本地 ls -l 亦为 -rwxr-xr-x | 驳回成立（评审撤回） | RV-05 | 2026-11-16 |
| P11 | 建议 | 兜底分支会误报成功 | 已改 | 提交结果分支显式化：凭证生效（含告警）/未生效+回滚/未生效+无回滚三条路径 | 已关闭 | RV-05 | 2026-11-16 |
| P12 | 建议 | 并发测试未断言最终一致 | 已改 | TestUpdateSerialized 增断言：最后一次写盘值 == 最后一次生效值（auth-B） | 已关闭 | RV-05 | 2026-11-16 |
| P13 | 建议 | prepare 脚本固定 /tmp、未检查 upx | 已改 | mktemp -d 独立临时目录 + trap 清理；upx 存在性检查（缺省路径可覆盖 UPX 变量） | 已关闭 | RV-05 | 2026-11-16 |
| P14 | 建议 | 路由器重启不自启，需明确口径 | 已改 | 验收清单 §7 范围口径：整机重启开机自启不在 v0.10.0 范围；TODO.md 技术债列 procd init 脚本 | 已关闭 | RV-05 | 2026-11-16 |


### RV-2026-11-16-05（第五轮复核，5 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I11 | 重要 | rollback_old 定义在使用之后，启动失败路径 set -e 直接退出 | 已改 | start_daemon/health_check/rollback_old 三个函数整体前移至 set -e 之后、首次使用之前（grep 核对调用点均在定义后） | 文档修订完成 | 待复核 | 2026-11-16 |
| P15 | 建议 | start-stop-daemon -b 是否继承日志重定向不确定 | 已改 | 统一改用 setsid + 显式重定向（行为可预期），回退普通后台；不再使用 start-stop-daemon | 文档修订完成 | 待复核 | 2026-11-16 |
| P16 | 建议 | 回滚后未做健康检查 | 已改 | rollback_old 回滚后同样 15×1s 健康轮询，输出「已恢复旧版本（HTTP 401/200）」，失败提示人工介入 | 文档修订完成 | 待复核 | 2026-11-16 |
| P17 | 建议 | 跨文件系统 mv 非原子 | 已改 | 先 cp 到 \$INSTALL_DIR/\$BINARY.new，再同目录 mv 原子替换 | 文档修订完成 | 待复核 | 2026-11-16 |
| P18 | 建议 | 归档件缺勘误 | 已改 | r2 归档件 P10 行下补「RV-05 复核撤回」勘误 | 文档修订完成 | 待复核 | 2026-11-16 |


### RV-2026-11-17-01（闸门⑤/⑥ 复核）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I12 | 重要 | 「更新后立即恢复」未真机验证，恰是核心价值 | 待拍板→已裁决 | PM 拍板：保留待生产首验，不卡 tag；真过期时三步补验（确认过期显示→/admin 更新→立即恢复）；派生问题（过期+无缓存启动失败风险）记入 TODO 技术债 | 已裁决 | PM 已拍板 | 2026-11-17 |
| I13 | 建议 | killall 对 20 字符二进制名匹配待确认 | 已改（降为可选加固） | PM 手工确认 killall 有效；脚本停服/回滚统一改 kill \$(pgrep -f \"\$BINARY\")（AX6 已确认有 pgrep）；二进制改名方案（mini-cloud-upx）留待拍板 | 文档修订完成 | 待复核 | 2026-11-17 |
| P19 | 建议 | 播放 RSS 数据点偏弱 | 驳回成立（评审撤回） | PM 反馈：302 模式路由器不在数据链路，播放不加内存属预期（正印证「为什么用 302」设计） | 已关闭 | RV-17 撤回 | 2026-11-17 |
| P20 | 建议 | README 运行节未指向部署脚本 | 已改 | 「运行」节补部署脚本推荐指引 | 文档修订完成 | 待复核 | 2026-11-17 |
| — | 拍板 | tag 打在哪、master 是否合入 | 已拍板 | PM：tag 打 feature 分支（v0.10.0 已打）；全部开发完成后一次合入 master；真机运行 ≥1 个月方可称正式版；v0.10.0 为回退基线 | 已裁决 | PM 已拍板 | 2026-11-17 |


### RV-2026-11-17-02（Vault Phase 0 核心审核）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 元数据先于哈希计算加密，对象内嵌 H 恒空 | 已改 | Metadata.H 废弃（字段保留兼容）；EncryptObject 返回权威哈希，manifest.Entry.SHA256 唯一真值源；删除无效回写 | 文档修订完成 | 待复核 | 2026-11-17 |
| I2 | 重要 | CipherSize 对非对齐明文多算 | 已改 | 公式修正 DataOffset+PlainSize+ChunkCount*TagSize；补非对齐 1.5MiB 用例 | 文档修订完成 | 待复核 | 2026-11-17 |
| I3 | 重要 | 顺序读=每块一次 ReadAt，与报告承诺相悖 | 已拍板（方案 b） | PM 拍板：Phase 1 upstream.go 实现顺序流式 RangeSource（长连接+缓冲）；随机 Seek 单块；报告 §7.4/design §4.1 同步 | 已裁决 | PM 已拍板 | 2026-11-17 |
| P1 | 建议 | 读 meta 未检查 ReadAt 的 n | 已改 | n != MetaLen → 「元数据被截断」+ shortMetaSource 测试 | 文档修订完成 | 待复核 | 2026-11-17 |
| P2 | 建议 | keygen 未收紧存量 vault.key 权限 | 已改 | WriteFile 后显式 Chmod 0600 | 文档修订完成 | 待复核 | 2026-11-17 |
| P3 | 建议 | encrypt 未拒绝 out 位于 in 内部 | 已改 | 绝对路径前缀检查，拒绝并明确报错 | 文档修订完成 | 待复核 | 2026-11-17 |
| P4 | 建议 | 符号链接静默跳过 | 已改 | 打印「跳过 <路径>（非普通文件）」 | 文档修订完成 | 待复核 | 2026-11-17 |
| P5 | 建议 | Read 无 c==0 防御 | 已改 | copy 结果为 0 时返回 io.ErrUnexpectedEOF | 文档修订完成 | 待复核 | 2026-11-17 |
| P6 | 建议 | meta AAD 零占位原因补文档 | 已改 | 可行性报告 §7.3 补注（含 I1 的 h 字段废弃说明） | 文档修订完成 | 待复核 | 2026-11-17 |


### RV-2026-11-17-03（复核 + 文档一致性，4 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| P7 | 建议 | 报告版本号四处不一致（v1.1/v1.2/v1.3） | 已改 | 统一 v1.3：头部/版本表（补 v1.3 行）/审阅表（补 RV-17-02/03 行）/页脚/§7.3 示例 JSON（h 标注废弃恒空） | 文档修订完成 | 待复核 | 2026-11-17 |
| P8 | 建议 | PROGRESS 进行中与当前版本区滞后 | 已改 | 当前版本单一化（代码/文档各一行）；进行中据实改写（库层完成、待接线） | 文档修订完成 | 待复核 | 2026-11-17 |
| P9 | 建议 | Phase 1 顺序流式契约缺实现细节 | 已改 | design §4.1 补 4 条验收口径：缓冲 4–8 MiB / 相邻偏移差判定 / 失效时机（反向 Seek、跳跃、403 重签）/ 与 max_streams 信号量同一账 | 文档修订完成 | 待复核 | 2026-11-17 |
| P10 | 建议 | meta.S 失去约束；失败残留孤儿对象 | 已改 | EncryptObject 在 SealMeta 前强制 meta.S=size；cmdEncrypt 加密失败删除半成品对象并注明 | 文档修订完成 | 待复核 | 2026-11-17 |
