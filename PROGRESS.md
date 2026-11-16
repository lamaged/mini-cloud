# PROGRESS.md

> 开发进度与审核意见处置记录（专业语言），供外部审核员与程序员使用。
> PM 版进度由程序员在对话中另行汇报（交易语言）。

## 当前版本

- 代码：v0.10.0 实现完成（未发版，version 常量与 README 变更记录待闸门⑥）
- 文档：设计文档 v0.3.1、可行性报告 v1.2（已按 RV-2026-11-16-01/02 修订）

## 进行中

- v0.10.0 凭证网页管理 + 3 处转义缺陷修复：闸门③ 审核处置完成、部署脚本完成；**待真机验收（闸门⑤，清单 docs/acceptance-v0.10.0.md）**

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
| D6 | 阻断 | 凭证写回顺序与接口矛盾、无回滚方法 | 已改 | CredentialUpdater 改两阶段接口：ValidateCredential（零状态变更，10s 超时）+ CommitCredential（生效并落 token state，调用点唯一）；§3.3 流程零回滚；§3.4 移动实现改为临时 Client 验证；§3.7 验收 8 | 文档修订完成 | 待复核 | 2026-11-16 |
| I8 | 重要 | 报告改名后约 25 处相对链接 404 | 已改 | 报告内代码链接全部改为 `../` 相对路径（随 docs/ 目录修正） | 文档修订完成 | 待复核 | 2026-11-16 |
| I9 | 重要 | vault.paths 格式（有无前导斜杠）两处冲突 | 已改 | 统一为无前导斜杠目录名；匹配规则定稿（挂载内相对路径首段比较、大小写敏感）；design §4.3、报告 §1.1/§7.8、TODO 拍板记录同步 | 文档修订完成 | 待复核 | 2026-11-16 |
| I10 | 重要 | 上游 200 时「丢弃到偏移」在 55 GB 场景不可行 | 已改 | 分级策略：换新签名重发一次 → ≤2 MiB 允许丢弃 → 否则 502+日志；Content-Range 不符先失效 URL（共用刷新锁）；不做整文件顺序转发 | 文档修订完成 | 待复核 | 2026-11-16 |
| I11 | 重要 | Vault 绕过 RetryReader；「List 走 cached」注释矛盾 | 已改 | 路径分流规则：vault 前缀 → List 读内存虚拟树、Link 走 raw；其余路径委托 cached；Vault 对 raw.Link 网络错误重试 1 次、not found 不重试；注释同步 design §4.1、报告 §8.2 | 文档修订完成 | 待复核 | 2026-11-16 |
| I12 | 重要 | fileID 一致性校验建议提前到 Phase 0 | 已拍板 | PM 同意（2026-11-16）：fileID 一致性 → Phase 0 必做（报告 §7.3 不变量 4、§9 验收）；metadata.p 路径比对与失败处置仍 Phase 1 | 已裁决 | PM 已拍板 | 2026-11-16 |
| I13 | 重要 | §11「三家均 206」与 §4.5「百度待测」矛盾 | 已改 | 结论表口径修正：已测三家（天翼个人/家庭、移动）均 206；百度走代理路径，Range 待实测 | 文档修订完成 | 待复核 | 2026-11-16 |
| I14 | 重要 | key_id/flags/reserved 无字段语义 | 已改 | 报告 §7.3 定稿：key_id Phase 0 恒零、Phase 2 轮换启用（SHA-256(masterKey)[0:8]）；flags 非零即拒绝；reserved 忽略但保留 | 文档修订完成 | 待复核 | 2026-11-16 |
| P7 | 流程 | §6 风险表仍写「待 PM 确认」 | 已改 | 改为「已拍板（TODO 2026-11-16）」，与 §4.3/TODO 一致 | 文档修订完成 | 待复核 | 2026-11-16 |
| P8 | 流程 | 铁律 10 的两项观察项未进入闸门⑤ 清单 | 已改 | 闸门⑤ 增两条勾选项：记录 UPX 后体积、记录空闲与 4 路并发 RSS，均写入 PROGRESS.md | 文档修订完成 | 待复核 | 2026-11-16 |
| P9 | 流程 | 内网地址与 SSH 密码登录入库，与 P5 处置矛盾 | 已拍板 | PM 拍板（2026-11-16）：内网地址允许入库（仅私有网段、无凭证）；SSH 表述统一「免密优先，密码回退」；报告 §12.B 加「测试环境地址」注记 | 已裁决 | PM 已拍板 | 2026-11-16 |
| P10 | 流程 | 处置类别越出模板；「待复核」缺关闭规则 | 已改 | 模板补「已拍板」类别与状态取值；process.md §2.4 增关闭规则；RV-01 状态列据此更新 | 文档修订完成 | 待复核 | 2026-11-16 |
| P11 | 流程 | §8.2 静默删除 getObj 错误处理修改项 | 已改 | 报告 §8.2 prop.go 行恢复「getObj 错误处理（List 失败不得返回伪目录，Vault 降级路径依赖）」 | 文档修订完成 | 待复核 | 2026-11-16 |
| P12 | 流程 | 归档件铁律条数、env 模板 GOPATH 两处小不一致 | 已改 | 归档件 r1 加「aeb928e 快照」注记；CLAUDE.md GOPATH 表述修正为 /home/test/go-path | 文档修订完成 | 待复核 | 2026-11-16 |

### RV-2026-11-16-03（代码审核，15 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 双标签并发提交可致 config 与运行时不一致 | 已改 | Handler 增 `updateMu` 串行化验证→写盘→生效全流程；补 TestUpdateSerialized（A 阻塞时 B 被锁） | 文档修订完成 | 待复核 | 2026-11-16 |
| I2 | 重要 | 写回路径在 config.json 缺失时重建默认配置 | 已改 | 新增 `conf.LoadStrict`（缺失/损坏直接报错，绝不重建）；写回路径改用 `WriteCredential`；补 TestWriteCredentialMissingFile | 文档修订完成 | 待复核 | 2026-11-16 |
| I3 | 重要 | Save 未 fsync 父目录，断电后 rename 可能丢失 | 已改 | `writeFileAtomic` 在 rename 后 fsync 父目录（尽力而为 + WARN）；Save/Backup/RestoreBackup 统一走原子写；design §3.3 同步 | 文档修订完成 | 待复核 | 2026-11-16 |
| I4 | 重要 | persist 闭包（最高风险写路径）无测试 | 已改 | 抽为 `conf.WriteCredential`，补 5 个用例（正常写入且其它条目保留/挂载不存在不改文件/旧版 cloud 迁移等价/文件缺失不重建/无效 JSON 报错） | 文档修订完成 | 待复核 | 2026-11-16 |
| I5 | 重要 | 401 刷新 × 提交的代次防护无测试 | 已改 | 回调提取为 `handleAuthFailed` 方法 + `Client.refreshURL` 可注入；补 TestAuthRefreshRaceWithCommit（阻塞刷新端点，断言最终内存与 token state 均为已提交凭证） | 文档修订完成 | 待复核 | 2026-11-16 |
| I6 | 重要 | 提交失败时 config 已改，与「零变化」承诺不符 | 已改（PM 拍板方案①） | PersistFunc 返回回滚函数；提交失败时 admin 调用回滚用 `.bak` 还原配置并同步内存副本；补 TestCommitPlainErrorRollsBack；design §3.3/§3.7-8 同步 | 文档修订完成 | 待复核 | 2026-11-16 |
| P1 | 建议 | 错误分支统一 200 | 已改 | 400 输入错误（表单/CSRF/挂载）；502 验证失败；500 写盘/提交失败；200 成功；测试同步 | 文档修订完成 | 待复核 | 2026-11-16 |
| P2 | 建议 | 缺 no-store / X-Frame-Options / CSP | 已改 | 管理页返回三条安全头；补 TestPageSecurityHeaders | 文档修订完成 | 待复核 | 2026-11-16 |
| P3 | 建议 | version 仍 v0.9.2 | 已改 | 升 `v0.10.0`（README 变更记录留闸门⑥） | 文档修订完成 | 待复核 | 2026-11-16 |
| P4 | 建议 | Stop 未 sync.Once，文档称已改 | 已改 | `CachedReader.Stop` 改 sync.Once（提前于 Vault Phase 0 落地）；补 internal/fs/cache_test.go（重复 Stop 不 panic） | 文档修订完成 | 待复核 | 2026-11-16 |
| P5 | 建议 | d.authorization 提交后未同步 | 已改 | CommitCredential 在 commitMu 内同步 `d.authorization` | 文档修订完成 | 待复核 | 2026-11-16 |
| P6 | 建议 | 路径编码两份实现 | 已改 | 统一为 `webdav.EncodePathSegments`（导出），main 复用，删除 main 侧 encodeURLPath | 文档修订完成 | 待复核 | 2026-11-16 |
| P7 | 建议 | 即将过期 token 判为验证成功 | 已改（PM 拍板 24h） | 剩余有效期不足 24 小时拒绝接收（提示「请获取新的」）；补 TestValidateAuthorizationTooShort | 文档修订完成 | 待复核 | 2026-11-16 |
| P8 | 建议 | design §3.4 与 Commit 实现不符 | 已改 | §3.4-2 改为「应用 Validate 阶段暂存的凭证（含云主机）→ saveTokenState；不联网」 | 文档修订完成 | 待复核 | 2026-11-16 |
| P9 | 建议 | 自查 15 条无逐条留痕 | 已改 | 补下表（自查留痕） | 文档修订完成 | 待复核 | 2026-11-16 |

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
