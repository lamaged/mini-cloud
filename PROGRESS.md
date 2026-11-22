# PROGRESS.md

> 开发进度与审核意见处置记录（专业语言），供外部审核员与程序员使用。
> PM 版进度由程序员在对话中另行汇报（交易语言）。

## 当前版本

- 代码：v0.10.0 已发布（tag v0.10.0，真机验收通过）；Vault Phase 0 **代码完成**（格式/加密/读取器/mcrypt/Vault 层/接线，经 RV-17-02/03 审核闭环，本地端到端验证通过）
- 文档：设计文档 v0.3.2、可行性报告 v1.3

## 进行中

- **v1.2.0-beta 真机观察期**：master 合并已按 PM 拍板（2026-11-22）强制回退——继续在 feature 分支开发，等成熟再合并；tag v1.2.0-beta 重指到 feature 分支最新提交；观察起点 2026-11-19，期满无异常升正式版 v1.2.0；55GB 压测（可选）进入 ≥1 个月连续运行观察，期满无异常升正式版 v1.1.0；55GB 压测（可选）
- **Phase 2（v1.2.0）闸门③/④ 通过，真机验收中（闸门⑤）**：范围——容器根自动发现 + 异常提示接 /admin + 移动驱动启动健壮性 + 天翼个人站加密验收；design v0.6 经 RV-2026-11-19-03 复核通过（I1–I4/P1–P6 闭环，P7–P10 实现期补充）；开工后 §5.2/§5.3 与 §5.1 并行

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
| 2026-11-22 | **README 新手化重写（PM 主导、多轮合议定稿）**：定位改为「给小白照着做」——这是什么（PM 原文）/快速开始 5 步（拿程序→写配置→传路由器→启动+开机自启小抄→验证）/日常使用（更新令牌、上传加密文件）/FAQ 7 条/已知限制/给开发者索引/附录（凭证获取、播放器连接）/变更记录；「升级新版本」与「设计决策」按 PM 拍板移除；多挂载能力写入（同类型可配多个、无数量限制）；安全及风险段为 PM 定稿版；全程脱敏（<router-ip> 占位） |
| 2026-11-22 | **安全管理员复核收尾（三步，PM 拍板选 B）**：①feasibility 三处行内真实路径（我的视频/电影/Hobbit/视频文件名）占位符化——此前只改了代码块 URL 漏了行内路径；②自查清单换更严正则（挂载名后非 `<dir>` 即可疑，覆盖中文路径漏网）——严格自查命中归零（仅部署脚本与历史档案两处文档化例外）；③tag v1.2.0-beta 移到脱敏后最新提交（选 B：公开入口干净；历史旧提交仍可按 SHA 访问，重写历史不做） |
| 2026-11-22 | **提交范围最小化落地（PM 拍板两原则）**：①外审必需→脱敏保留；②非必需→不推送。文档脱敏批量执行——process/design/TODO/验收清单/测试手册/feasibility 的内网 IP 与 /home 路径全部占位符化（`<router-ip>`/`<dev-host>`/`~/go`），SSH 认证方式细节删除（保留部署方向），云盘真实内容名→`<dir>/<file>.mkv`+注记，测试设备泛化；CLAUDE.md 隐私节按两原则改写（替换安全管理员原版）+ 例外口径（部署脚本功能默认值/docs/review 历史档案/AX6）；env.example.sh 改 `$HOME/go`；自查 grep 工作区命中归零（仅例外项）；.private 清单例外更新 |
| 2026-11-22 | **发布策略再修订（PM 拍板：master 强制回退）+ 隐私脱敏机制落地**：master 回退到 v0.9.2 冻结（等成熟再合并），process.md 口径归位，tag v1.2.0-beta 重指 feature 分支；安全管理员三件套按序执行——.gitignore 增 .private/、.private/privacy-checklist.md 本地建（两道闸门验证通过）、CLAUDE.md 增「隐私与脱敏」硬规则；自查命中清单已列，存量内容处置待 PM 逐项拍板 |
| 2026-11-21 | **Phase 2 合入 master + tag v1.2.0-beta（PM 拍板）**：版本升 v1.2.0-beta；README 变更记录补 Phase 2 全貌；process.md 发布策略修订（master 承接已验收代码）；经 PR #1 合入 master（GitHub 保护规则要求 PR，完整历史保留）；tag v1.2.0-beta 打在合并点并推送；staging 重建；正式版 v1.2.0 待真机连续运行 ≥1 个月（观察起点 2026-11-19） |
| 2026-11-21 | **Phase 2 真机验收全部通过（ACC-2026-11-21）**：C1 跨挂载播放器实测无问题；C4 释放恢复通过（Edge 取消一路→重下成功 6.6 MB/s、改名可播）；「.txt」现象定性为客户端命名产物（503 纯文本错误体 + Edge 沿用首登记文件名，内容为正确视频流）；完整验收报告归档 docs/review/2026-11-21-phase2-acceptance.md（六节逐项 + 全局并发两口径数据 + 中间态 + 硬件加速 A/B）；待：版本升 v1.2.0-beta + tag |
| 2026-11-21 | **C3 限流验证通过（max_streams=3 重启后）**：4 路并发发起——第 4 路等待 5.2 秒（= 信号量获取超时 5s，分秒不差）后 503 拒绝（0 MB/OK=False），其余 3 路 207 Mbps 全速无扰；RSS 峰 44.5 MB、MemFree 最低 37.2 MB、无崩溃；「全局 3」拍板闭环（3 路满载→第 4 路等待超时→503）；此前 max_streams=4 的 4 路全成功为「天花板 4 实弹验证」；待补：C4 释放恢复、（可选）C1 跨挂载播放器 |
| 2026-11-21 | **真机验收进展（测试手册执行中）**：①上传中间态实测吻合文档（清单到而对象未传完 → 点播报 file does not exist，上传完自愈零日志）；②整块换新自动发现两组（GThrones→HCards，73 文件）；③**C2 场景通过**：3 路并发下载（3×~600 MB，移动挂载）全 OK 无 503、总吞吐 27.45 MB/s ≈ 220 Mbps、RSS 峰 53.9 MB < 70 MB 判据、MemFree 最低 24.2 MB ≥ 20 MB、无崩溃；待补：C3/C4 第 4 路限流与恢复、C1 跨挂载并发 |
| 2026-11-21 | **AX6 硬件加速 A/B 实测（PM）**：默认 386.6/442.8 MB/s vs 强制关闭加速 7.7/7.9 MB/s——50 倍差距证实 Go 自动启用 ARMv8 AES/PMULL（无需平台代码）；纯软件 7.7 MB/s < 4K 所需 9.4 MB/s，硬件加速是 4K 目标的前提；数据入 feasibility §2.4 |
| 2026-11-21 | **上传中间态语义确认并文档化（PM 提出，无需改代码）**：官方客户端长时上传期间的三种中间态（无 _mc 不显示 / 清单未到空目录+告警 / 对象未传完短暂报错）均为设计语义——不崩溃、不泄露（GCM 认证）、≤5 分钟自愈；写入 README 已知限制 + 测试手册第 4 节 |
| 2026-11-20 | **部署脚本回归修复**：start_daemon 的 `touch "$LOG"` 行在此前撤守护的回退编辑中被误删（PM 清日志后部署中断、服务停摆的真机教训）——恢复自动建日志；测试手册异常速查补对应条目；提醒：清日志用 `>` 而非 `rm` |
| 2026-11-20 | **综合复核 RV-2026-11-20-05（通过，无需再改代码）处置**：S3 超时测试补 2 断言（告警含"排队超时"+lastScan 短重试生效）；S4 文案改「下次访问时重试」（懒刷新语义，非后台定时器，代码/设计同步）；S5 README 记 ModeIrregular 范围（Windows 重解析点含云盘占位文件，fail-closed）；S6 PM 拍板：不检查 `-out` 自身/祖先 symlink（使用者自己的目录布局），README/TODO 留痕；S1/S2 暂缓（随下次触及）；S7 CI 自报口径已记录；**新增 docs/testing-manual-v1.2.0.md 真机验收测试手册**（部署确认/三站回归/全局并发 C1–C4/自动发现/管理页/稳定性/异常速查，对着勾执行） |
| 2026-11-20 | **外部审核第四轮（有条件通过，2 项闭合 + 2 项建议）处置完成**：①safeOutPath 补 Windows junction 识别（Go 1.23 起 ModeIrregular，仅查 ModeSymlink 会漏）+ TestDecryptWindowsJunction（Windows 专属，其他平台明确跳过）；②manifest 额度排队超时不再静默——返回非空告警（/admin 可见）+ 本轮有异常时下轮 1 分钟短重试（scanRetryShort），旧树保留；③writeKeyAtomic 目录同步失败打警告（rename 已成功，仅持久性提示）；④测试诚实修正——争抢测试真建两个 Vault 实例并注明 token 填位为确定性手段、超时测试改名为 TestManifestSemBudgetTimeout（P9 后台 ctx 设计如实注释）；`go vet`+`go test -race` 全绿（9 包）、ARM64+Windows 编译通过 |
| 2026-11-20 | **外部审核第三轮（3×P1 确认闭合 + 3×P2）处置完成**：P2-1 decrypt 统一安全路径 safeOutPath（Files/Dirs 同处理、拒反斜杠、词法边界 + 逐级符号链接检查——修掉「预置符号链接目录把写入引到 outDir 之外」的逃逸）+ 符号链接逃逸/Dirs 非法路径 2 组测试；P2-2 writeKeyAtomic 补父目录同步 + 目录只读写失败旧钥不变测试（root 自动跳过）；P2-3 manifest 额度等待响应超时（select mctx.Done）+ 双实例争抢/等待取消 2 组测试（manifestReadTimeout 改 var 便于测试）；测试可行性清单：4 全可测 + 1 条件可测 + 1（同步失败）审查覆盖；`go vet`+`go test -race` 全绿（9 包）、ARM64+Windows 编译通过 |
| 2026-11-20 | **外部审核第二轮（RV-2026-11-20-03，3×P1 + 2×P2）处置完成**：R2-1 信号量进程级共享（globalStreamSem/globalManifestSem，main 经 SetGlobalStreamLimit 设置一次；**全局默认 3 = PM 拍板**；补跨挂载共享测试）；R2-2 keygen --force 原子替换（temp+fsync+rename，失败旧钥不动、恒 0600 + 3 组测试）；R2-3 配置语义错误不触发 .bak 自愈（semanticError 哨兵 + isSemanticError 分流 + 2 组测试）；R2-4 decrypt e.Obj 与 fileID 推导一致校验 + Abs 错误处理；R2-5 encrypt 复用 ValidateVirtualPath；补 EncryptObject 字节校验 4 组 + 路径边界测试；`go vet`+`go test -race` 全绿（9 包）、ARM64+Windows 编译通过 |
| 2026-11-20 | **外部审核（更严格第三方，12 条）全批处置完成 + PM 拍板数字落地**：P0 keygen 排他创建+`--force`+4 组测试、.gitignore 加 vault.key/*.key；#2 vault.paths 启动严格校验（单段/保留名/去重）+ max_streams 天花板 4（PM 实测拍板）；#3 虚拟路径规范校验（ValidateVirtualPath 导出，buildRoot 前 fail-closed，防 path.Dir 死循环）；#4 manifest 解析全局并发限 2 + SetMemoryLimit 前移 vault.Init 前；#5 EncryptObject 实际总字节校验（+size<0）；#6 SaveManifest 三层尺寸校验（16 MiB，PM 拍板下调）+ mcrypt manifest temp+rename 原子写；#8 hex 解码失败/长度≠32 fail-closed（verify 与 Vault.Link）；#9 mcrypt decrypt 路径规范+输出边界校验；#10 getObj 不再伪装目录（ErrForbidden 透传）；#11 子根告警读取成功即清；#12 改判成立（常驻审核记错位置）——Vault.Link 复用已取链接；**端到端测试 5 组 9 场景全链路**（PROPFIND/GET/Range/Seek/SHA-256/错钥/篡改/清单缺失损坏）；`go vet`+`go test -race` 全绿（9 包）、ARM64+Windows 编译通过 |
| 2026-11-19 | **复核 RV-2026-11-19-07（通过）处置**：本轮修复链获正面确认（回归处置链/双层告警清除/动态标签边界/并发正确）；P1 `TestVaultSupported` 改为对真实驱动实例断言（锁死「Name() ↔ 允许列表」不变量，防驱动改名后静默失效）；P2 容器 List 失败分支补注释（有意粘滞语义，成功后自然清除）；`go vet` + `go test -race` 全绿（9 包）、ARM64 通过 |
| 2026-11-19 | **文档恒同步规则落地（PM 拍板）**：沟通协议新增第 6 条「不论改动对错，设计文档必须与代码同步、不得欠账」；补齐两处文档欠账——design §3.3 管理页现状（全挂载状态卡/动态有效期标签/过期文案/根页显示名组合）、design §5.1 装配门机制（vaultSupported 允许列表 + 回归教训注记） |
| 2026-11-19 | **⚠️ 回归事故与紧急修复**：`vaultSupported` 允许列表误用驱动类型名（189Personal 等，实为日志 type 字段），而 Name() 实际返回 tianyi/mobile/baidu/local——上一版部署后真实云盘全部判「不支持」，Vault 无一装配、加密全部失效（PM 真机发现）；已修正为 Name() 实际值并同步测试，教训：允许列表必须按 drivers/*.go 的 Name() 返回值核对，不得引用日志字段 |
| 2026-11-19 | **管理页体验优化（PM 拍板）**：挂载卡片去重（名/前缀同一词，删小字）；状态条过期文案「令牌已过期，请更新令牌」（根导航页显示名改为「挂载名：告警」组合）；移动凭证标签动态化——未过期显示「有效期还有 N 天」（向上取整，<24h 显示不足 1 天）、已过期显示「可登录移动云盘网页后，用浏览器开发者工具获得」（admin Mount.FieldsFunc 渲染时现取）；新增 TestCredentialFieldsDynamic/TestRemainingDaysCeil；`go vet` + `go test -race` 全绿（9 包）、ARM64 通过 |
| 2026-11-19 | **真机第二轮反馈修复**：①告警粘滞缺陷——容器告警改为「本轮扫描状态」（无新问题即清空，问题修复后下轮自动消失；refreshRoot/checkNestedMc 改为返回告警由 scan 累积）+ TestContainerWarningClears；②铁律 2 装配门——main 按驱动名允许列表（天翼/移动/local）装配 Vault，百度不再参与容器扫描（消除「容器扫描失败 errno=-9」噪声）+ TestVaultSupported；`go vet` + `go test -race` 全绿（9 包）、ARM64 通过 |
| 2026-11-19 | **复核 RV-2026-11-19-06（通过，有条件）处置**：两处修复确认正确、「加密专区」决策获安全增强认定；I1 design §5.1 point 3 旧口径修正（无 _mc → 404 未找到，与「不认识即不管」一致）；P1 验收清单补「非单元路径 404」用例 |
| 2026-11-19 | **真机验收中发现 §5.2 集成缺陷并修复**：天翼/百度等无 CredentialUpdater 的挂载不上管理页 → 其 vault 告警（P7 深层 _mc 等）无处显示——main 改为全部挂载入管理页（凭证挂载带表单，其余为状态观察卡，Update 为 nil 已被 400 守卫）；admin 模板仅对 Fields 非空渲染表单、CSRF 仅表单卡生成；真机触发场景：PM 把新目录误放成 backup/test/test/_mc（第四层）——按设计不识别为单元、物理可见（符合预期），告警待修复版部署后可见 |
| 2026-11-19 | **Phase 2 代码审核 RV-2026-11-19-04（闸门③ 有条件通过）处置**：无 I 级；P5 核实已闭环（credential.go:106 提交清除 hostWarn + TestHostWarnClearedOnCommit，审核员 diff 遗漏）；P1 容器级 List 包 5s 短超时；P2 初始扫描子目录并发（WaitGroup，启动总时长 ≈ 单目录）；P3 disableVault 后中断扫描循环（isDisabled 早退）；P4 移动 List/Link 对 host 为空降级（空目录/明确错误，与过期对齐）+ TestNoHostDegradedList；`go vet` + `go test -race` 全绿、ARM64 通过 |
| 2026-11-19 | **Phase 2 实现完成（§5.1 容器自动发现 + §5.2 提示接 /admin + §5.3 移动启动健壮性）**：vault 两级重构（containers 容器 → 子根 roots；findRoot 两级匹配；保留名 `_` 屏蔽 403；scan 懒刷新 5min TTL + refreshMu TryLock 去重 + 后台 ctx P9 + 每目录 ≤5s 短超时 + 失败隔离；I4 摘要比对 LoadManifestSum（解密后内容 SHA-256）+ 原子交换 + InvalidateAll；8MiB 热刷新守卫；P7 深层 _mc 检测告警；新增 6 组容器测试 + 既有 12 组迁移）；§5.2 双通道告警（驱动 HealthReporter 原始断言 + Vault.Warning 合并，铁律 7）；§5.3 mobile Init 发现失败不阻断（10s 短超时、hostWarn 提示、提交清除）+ 3 组测试；`go vet` + `go test -race` 全绿（9 包）、ARM64 交叉编译通过 |
| 2026-11-19 | **Phase 2 设计复核 RV-2026-11-19-03（闸门① 放行）处置**：I1–I4/P1–P6 全部闭环；P10 措辞即时修正（整块覆盖≠就地修改，入 design）；P7 嵌套 _mc 检测告警、P8 旧对象残留（TODO 记录）、P9 扫描 goroutine 后台 ctx——三条入实现期清单；评审确认原子单元模型与物理三层精确化是良好收敛 |
| 2026-11-19 | **Phase 2 设计评审 RV-2026-11-19-02（闸门① 有条件通过）处置**：PM 拍板 I1（接受一级目录名明文，高敏感场景的间接层后续再加）、I2（子目录内全量重加密可接受，按剧集/专辑/年份适度粒度）；补 I3（扫描不阻塞：触发 List 立即返回旧树 + 一次性 goroutine 异步换树 + manifest 读独立 ≤5s 短超时 + 单目录失败隔离）；补 I4（摘要 = 解密后内容 SHA-256，不做 generation 字段）；P1 两级结构（容器→子根，isMcPath 按第二段）；P2 mcrypt 逐子目录用法与上传顺序；P3 旧单根迁移两法（云盘移 _mc 零重加密 / 逐组重跑）；P4 stray 文件按普通文件兜底；P5 PROGRESS 进行中清理；P6 交叉引用核对无残留；design 升 v0.6 待 PM 批准闸门① |
| 2026-11-19 | **打 tag v1.1.0-beta，进入 Phase 2**：version 升 v1.1.0-beta（真机验收通过、进入 ≥1 个月运行观察）；README 变更记录补 v1.1.0-beta 条目（Vault 播放/取数层/崩溃修复/支持范围/已知限制/验收结果）；Phase 2 启动（范围待 PM 拍板后过设计评审） |
| 2026-11-19 | **Phase 1 收官复核 RV-2026-11-19-01（通过）处置**：崩溃缺陷正式结案（RV-2026-11-18 系列四轮全部闭环）；P12 复验判定线按实测更新（RSS ≤ ~70 MB = 64 MiB 软上限+运行时开销、MemFree ≥ ~20 MB）；P13 天翼 CDN ~50 Mbps 限速写入 README 已知限制 + feasibility §5.1 + TODO 技术债（Phase 2 调研 limitrate）；P14 复验时长口径闭合（≥5 min + 15 min/5 min 两组互补） |
| 2026-11-19 | **Phase 1 验收收官（闸门⑤ 全部达成，日志存 test_log/ 不入库）**：① 3×720P 15 分钟（2 移动+1 天翼）流畅，RSS 峰 50.7MB、CPU 均 4.4%，**播放中途 2 次签名自动刷新（11:29/11:31，ttl=10m0s）零中断**——「15 分钟签名自动刷新」验收项闭环；② 3×4K 全移动 5 分钟无卡顿（225 Mbps 并发解密，**超 2 路 4K 目标**），RSS 峰 68.8MB = 64MB 软上限 + ~5MB 运行时开销（GC 触发后回落 47→27MB，MemFree 最低 21MB，机制健康；60MB 判据线按实测口径更新，PM 确认维持 64MB）；③ **天翼 4K 卡顿定性**：单独天翼 4K 卡顿时 CPU 仅 8–23%（网关空等数据）→ 移动 3×4K 同管线流畅 → 天翼 720P 流畅——**天翼家庭 CDN 限速 ~50Mbps（疑似 URL x-amz-limitrate=51200 参数）所致，与网关无关**（PM 结论确认） |
| 2026-11-18 | **崩溃修复真机压测复验通过（闸门⑤）**：Edge 0→1→2→3 路加密下载（~10 MB/s×3）；RSS 12.4 MB（空闲）→ 19（1 流）→ 24–28（2 流）→ 34→45.3 MB 峰值（3 流，GC 惰性爬升后 44–45 MB 平台稳定）；MemFree 37→29 MB 后企稳；每流增量 +5~6 MB 与设计吻合；CPU 峰值 45%（约半核）；无崩溃、无 503、无泄漏（平台曲线为证）；测试时长 4.5 分钟略短于 10 分钟标准，PM 可视需要补长测；结束后 RSS 不回落属 Go MADV_FREE 可回收内存，非泄漏 |
| 2026-11-18 | **崩溃修复复核 RV-2026-11-18-03（通过，有条件）处置**：I1 验收口径定稿（允许 503 并统计来源、RSS<60 MB 判定线、HEAD 占槽为观察项；503 加 Info 日志便于统计）；I2 `SetMemoryLimit` 生效点移到挂载装配之后（启动期大清单不受限、只护运行期）；P6 feasibility §7.7 内存口径重写（6 MiB/流、默认 3 流 18 MiB、64 MiB 兜底）；P7 design §4.1 每流 ≈6 MiB 口径；P8 README 补「fatal 后需手工重启」运维后果；P9 复用断言改 TotalAlloc 增量（轻量字节级）；`go vet` + `go test -race` 全绿、ARM64 交叉编译通过 |
| 2026-11-18 | **崩溃修复复核收尾（RV-2026-11-18-02 通过）+ PM 两项拍板落地**：C 落地——max_streams 默认 3（PM 拍板：2–3 路为极限）；D 落地——`SetMemoryLimit(64 MiB)` 内存软上限兜底（PM 原则①路由器不能被拖垮）；F 移除——procd 守护按 PM 拍板删除（脚本 git rm、deploy-router.sh 还原，I1 假成功风险随之关闭；拍板理由入 TODO）；P1 读循环零进展防御（连续 100 次零读报错）；P2 补 TestLoadChunkReusesBuffers（AllocedBytesPerOp < 64 KiB 锁定复用，防回退）；P4 崩溃报告数字统一（12–13 MiB/流、48–52 MiB）；P5 memstats 采样 5 分钟→1 分钟；`go vet` + `go test -race` 全绿（9 包）、ARM64 交叉编译通过 |
| 2026-11-18 | **崩溃缺陷修复实施（RV-2026-11-18-01 处置 + PM 三原则落地）**：A `doRange` 直接读入复用窗口缓冲（消 ReadAll old+new 双份 ~7 MiB/流，4 条约束全落实）；B 解密块 cbuf+pbuf 复用（gcm.Open(dst)，每流 −2 MiB）；E `fetchRange` 入口/失败后判 ctx.Err() 不换签不重试 + webdav 取消类降 Debug + TestUpstreamCancelNoRefresh；G -debug 每 5 分钟 MemStats；F scripts/procd-mini-cloud.init（respawn 3600 5 5）+ deploy-router.sh procd 感知（stop/start/rollback 三处）；C 暂缓、D 待实测；`go vet` + `go test -race` 全绿（9 包）、ARM64 交叉编译通过；修复后每流 ~6 MiB（4 流 ≈ 24–28 MiB），对应 PM 三原则：①可崩但自愈且不拖垮路由（A+B+D+F）②内存最小化（A+B）③保留 4 并发支撑 2 路 4K 实测目标 |
| 2026-11-18 | **移动站真机验收（闸门⑤）+ 崩溃缺陷定位**：部署 ✓、虚拟树 ✓、网页下载 ~10 MB/s（CPU 13.8%）✓、播放 + 拖动 ✓、**30+ 分钟连续播放 ✓（15 分钟签名自动刷新真机实测通过）**；极限压测（2 播放器 + 924 MB 下载 = 4 并发解密流）**应用崩溃**——SIGSEGV 位于 doRange 的 io.ReadAll 堆分配（日志 159–166 行），根因 = 每流内存预算漏账（ReadAll 中转 4 MiB + 密文块 1 MiB 未计入，实际 ~10 MiB/流 vs 预算 6 MiB），4 流峰值 50–60 MiB 超路由器 ~30 MB 空闲；修复方案 A（直接读入窗口缓冲）/B（密文缓冲复用）/C（默认并发 4→3）/D（可选 SetMemoryLimit）已写成报告 docs/review/2026-11-18-mobile-crash-report.md，**等审核 + PM 确认后动手**；PM 临时防线：config 加 `"max_streams": 2` |
| 2026-11-17 | **Phase 1 代码评审闭环（RV-2026-11-17-11 通过）**：五轮共 36 条意见（RV-07 → RV-11）全部闭环；P19 升级机遗留默认 .bak 顺带刷新、P20 isDefaultBackup 字段级精确化（local-only 不再误伤）、P21 拒绝自愈错误带原因——三条「可选优化」已顺手全部落实（各约 5 行 + 测试）；`go vet` + `go test -race` 全绿、ARM64 交叉编译通过。**后续节奏：移动站真机验收 → 55GB 压测（可选）→ 真机运行 ≥1 个月 → Phase 2 设计评审** |
| 2026-11-17 | **Phase 1 复核 RV-2026-11-17-10（通过 → 处置完成）**：RV-09 九题闭环确认；I8 默认配置不写 .bak + 默认备份拒绝自愈（防 admin/admin 回退）+ 两用例；I9 README 百度措辞与 P11 行为对齐（密文可下载副作用明示）；P14 不可 seek 流式驱动跳过测试；P15 .bak 损坏自动刷新；P16 备份刷新策略写明（只在缺失/损坏时）；P17 排序前提注释；P18 design §4.3 补第三态 |
| 2026-11-17 | **Phase 1 复核 RV-2026-11-17-09（通过 → 处置完成）**：RV-08 十一题闭环确认；I6 启动补 .bak（未用过 /admin 的机器自愈生效）+ 测试；I7 空文件自愈（副本仅非空时保留）+ 测试；P7 Content-Range 总额未知容错；P8 遗漏两处 502 包装补齐；P9 合法短窗口续拉（块读不再误判 EOF）；P10 损坏副本保留最近 3 份；P11 PM 拍板：百度挂载完全不启用 Vault（不建虚拟树，铁律 2 写明「加密不支持百度」）；P12/P13 注释去重与百度包 legacy 标注 |
| 2026-11-17 | **Phase 1 审核 RV-2026-11-17-08（有条件通过 → 处置完成）**：RV-07 九条闭环确认；I1 传输截断检测（按 Content-Range 声称区间比对）+ 重试测试；I2 416→EOF 语义（manifest 恰为整块时不再整树变空）；I3 PM 拍板：只支持天翼/移动（铁律 2 收紧，百度退出支持目标、Vault 返回 502）；I4 自愈强化（broken 副本 + 先验后写 + 日志明示）；I5 自愈范围与旧凭证风险写入 design §3.3/README；P1 复用已获取 Link；P2 driver.ErrUpstream→502；P3 绑定校验 fail-closed；P4 超时 90s；P5/P6 文档与注释 |
| 2026-11-17 | **Phase 1 天翼家庭站真机验收通过**：Edge 浏览虚拟树正常（原始目录/文件名）；_mc 403；播放器正常播放 + 来回拖动 10 次以上无感知延迟（顺序流式 + 单块 Seek 在真实 CDN 生效）；播放中 CPU 5.2%（AX6）；剩余：55GB 压测（可选）、移动云盘验证、百度适配、外部审核（闸门③） |
| 2026-11-17 | **Phase 1 真机首验（部署成功）**：配置逗号笔误修复（sed 补逗号 + vault.key 0600）；部署脚本成功部署 Phase 1 开发版（3,005,112 B）；**天翼家庭 backup 虚拟树真机显示正常（Edge 浏览器确认 Game of Thrones S01 原始目录名）**；version 升 v1.1.0-dev；待 PM 补验 _mc 403 与播放器播放/拖动 |
| 2026-11-17 | **Windows 版加密工具（PM 拍板，铁律 5 跨平台）**：云盘上传在 Windows 完成——mcrypt 随版本交付 Windows amd64 版（deploy-prepare.sh 自动编译到暂存目录）；内置 MIME 表优先于系统表（.ts 等部分平台系统表返回错误类型）；已编译 mcrypt.exe（4.2 MB）入暂存目录 |
| 2026-11-17 | **Phase 1 接线完成**：Vault.Link 走云盘取数层（URL 型驱动自动启用，manifest 亦经 Range 读取）；U3 信号量前置 raw.Link（超限拒绝零 API 调用，测试锁定）；I3 对象绑定强校验落地（metadata.p 与请求路径比对）；修复对象末尾正常短读被误判为失败的缺陷（manifest 读取路径）；集成测试 4 组（URL 全链路解密/信号量前置/绑定拒绝/不可读降级） |
| 2026-11-17 | **Phase 1 第一块审核 RV-2026-11-17-07（不通过 → 已修复）**：U1（跨窗口短读必然触发「截断」误报）ReadAt 循环补齐 + 完整覆盖判定 + 块长×8 跨窗口测试；U2（跳跃后预读永久失效）单块也写缓冲 + 跳跃恢复测试；U4 选方案 (a) 删单飞、串行契约注释化；P1 200 先换签名重试（I10 对齐）；P2 416 不换签名；P3 请求头白名单（UA/Referer）；P4 补缓冲中途 403 与 Content-Range 错位测试；P5 200 路径注释。**注：取数层已入库、未接线（Vault.Link 仍走本地 ReaderAt，下一块接线）** |
| 2026-11-17 | **Vault Phase 0 评审闭环（RV-2026-11-17-06）**：4 轮共 30 条意见（I 9 条 / P 21 条）全部关闭，无未决事项；Phase 1 输入清单移交（P9 顺序流式口径 / P21 信号量前置 / P22 Warning 接 /admin / 移动驱动过期+无缓存启动健壮性）；Phase 0 可进入 Phase 1 设计评审 |
| 2026-11-17 | **Vault Phase 0 复核 RV-2026-11-17-05（通过，有条件）**：RV-04 全部闭环；I5（空 MIME → nil map panic，I3 修复引入的回归）一行修复 + 空 MIME 测试；P19 示例删 v.Start；P20 JSON 注释移出代码块；P21 信号量前置入 design §4.1 契约；P22 List 返回副本 + Warning 去向记技术债（Phase 1 接 /admin） |
| 2026-11-17 | **Vault Phase 0 接线复核 RV-2026-11-17-04（通过，有条件）**：P7–P10 闭环确认；I1（URL 型驱动跳过 vault 装配，天翼/移动同名目录不受影响 + 告警不再覆盖显示名）、I2（ErrTooManyStreams → 503 + 注释修正）、I3（GET ETag 与 PROPFIND 同源）、I4（孤立围栏修复）全部处置；P11–P18 落实（VaultConfig 指针、重载口径、签名统一、示例标注、children 索引、信号量幂等+ctx、验收口径记录、Obj 前缀校验） |
| 2026-11-17 | **Vault Phase 0 验收通过**：AX6 实测 AES-256-GCM Seal 384.6 MB/s / Open 383.8 MB/s（远超 40 MB/s 验收线；4K 播放约占单核 3%）；验收清单全部闭环——SHA-256 一致、Range 206 字节级精确、随机 Seek 只读 1-2 块、_mc 403、空文件/头篡改拒绝、fileID 一致性、go test -race 全绿 |
| 2026-11-17 | **Vault Phase 0 接线完成**：Vault 包装层（虚拟树/_mc 403/信号量/降级告警）+ model.Obj ETag 唯一来源 + webdav ErrForbidden→403 + conf VaultConfig + main 装配；本地端到端全通（加密→PROPFIND 原始名→GET 明文→Range 206 字节级精确→_mc 403→普通文件不受影响）；mcrypt-arm64 bench 已入暂存目录待 AX6 执行 |
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
| P12 | 流程 | 归档件铁律条数、env 模板 GOPATH 两处小不一致 | 已改 | 归档件 r1 加「aeb928e 快照」注记；CLAUDE.md GOPATH 表述修正为 ~/go-path | 已关闭 | RV-04 | 2026-11-16 |

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
| I1 | 重要 | 元数据先于哈希计算加密，对象内嵌 H 恒空 | 已改 | Metadata.H 废弃（字段保留兼容）；EncryptObject 返回权威哈希，manifest.Entry.SHA256 唯一真值源；删除无效回写 | 已关闭 | RV-06 | 2026-11-17 |
| I2 | 重要 | CipherSize 对非对齐明文多算 | 已改 | 公式修正 DataOffset+PlainSize+ChunkCount*TagSize；补非对齐 1.5MiB 用例 | 已关闭 | RV-06 | 2026-11-17 |
| I3 | 重要 | 顺序读=每块一次 ReadAt，与报告承诺相悖 | 已拍板（方案 b） | PM 拍板：Phase 1 upstream.go 实现顺序流式 RangeSource（长连接+缓冲）；随机 Seek 单块；报告 §7.4/design §4.1 同步 | 已裁决（RV-06 确认） | RV-06 | 2026-11-17 |
| P1 | 建议 | 读 meta 未检查 ReadAt 的 n | 已改 | n != MetaLen → 「元数据被截断」+ shortMetaSource 测试 | 已关闭 | RV-06 | 2026-11-17 |
| P2 | 建议 | keygen 未收紧存量 vault.key 权限 | 已改 | WriteFile 后显式 Chmod 0600 | 已关闭 | RV-06 | 2026-11-17 |
| P3 | 建议 | encrypt 未拒绝 out 位于 in 内部 | 已改 | 绝对路径前缀检查，拒绝并明确报错 | 已关闭 | RV-06 | 2026-11-17 |
| P4 | 建议 | 符号链接静默跳过 | 已改 | 打印「跳过 <路径>（非普通文件）」 | 已关闭 | RV-06 | 2026-11-17 |
| P5 | 建议 | Read 无 c==0 防御 | 已改 | copy 结果为 0 时返回 io.ErrUnexpectedEOF | 已关闭 | RV-06 | 2026-11-17 |
| P6 | 建议 | meta AAD 零占位原因补文档 | 已改 | 可行性报告 §7.3 补注（含 I1 的 h 字段废弃说明） | 已关闭 | RV-06 | 2026-11-17 |


### RV-2026-11-17-03（复核 + 文档一致性，4 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| P7 | 建议 | 报告版本号四处不一致（v1.1/v1.2/v1.3） | 已改 | 统一 v1.3：头部/版本表（补 v1.3 行）/审阅表（补 RV-17-02/03 行）/页脚/§7.3 示例 JSON（h 标注废弃恒空） | 已关闭 | RV-06 | 2026-11-17 |
| P8 | 建议 | PROGRESS 进行中与当前版本区滞后 | 已改 | 当前版本单一化（代码/文档各一行）；进行中据实改写（库层完成、待接线） | 已关闭 | RV-06 | 2026-11-17 |
| P9 | 建议 | Phase 1 顺序流式契约缺实现细节 | 已改 | design §4.1 补 4 条验收口径：缓冲 4–8 MiB / 相邻偏移差判定 / 失效时机（反向 Seek、跳跃、403 重签）/ 与 max_streams 信号量同一账 | 已关闭 | RV-06 | 2026-11-17 |
| P10 | 建议 | meta.S 失去约束；失败残留孤儿对象 | 已改 | EncryptObject 在 SealMeta 前强制 meta.S=size；cmdEncrypt 加密失败删除半成品对象并注明 | 已关闭 | RV-06 | 2026-11-17 |


### RV-2026-11-17-04（接线首轮审核 + P7–P10 复核，12 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | Vault 无条件装配误伤天翼/移动 | 已改 | loadRoot 对 URL 型 Link（Reader==nil）跳过该根并 INFO 日志（不接管同名目录）；撤销 VaultWarning 覆盖显示名（仅日志）；补 urlOnly 驱动测试 | 已关闭 | RV-06 | 2026-11-17 |
| I2 | 重要 | max_streams 超限 500 应为 503 | 已改 | 新增 driver.ErrTooManyStreams → webdav 映射 503；注释改为「最多等待 acquireTimeout 后拒绝」；acquire 增 ctx 取消响应 | 已关闭 | RV-06 | 2026-11-17 |
| I3 | 重要 | GET 无 ETag 与 PROPFIND 不同源 | 已改 | etagFor 统一生成，Link Header 增同源 ETag；补同源测试 | 已关闭 | RV-06 | 2026-11-17 |
| I4 | 重要 | 报告 §7.3 孤立围栏渲染错位 | 已改 | 删除多余围栏；全文档围栏计数核对为偶数 | 已关闭 | RV-06 | 2026-11-17 |
| P11 | 建议 | VaultConfig struct 值 omitempty 无效 | 已改 | 改指针 *VaultConfig + omitempty；main 判空 | 已关闭 | RV-06 | 2026-11-17 |
| P12 | 建议 | manifest 无重载与文档不符 | 已改 | 报告 §6.4 与 design §4.3 改为「启动加载、重启生效；定时重载 Phase 2」 | 已关闭 | RV-06 | 2026-11-17 |
| P13 | 建议 | vault.New 签名三处不一致 | 已改 | design §4.1 与报告 §8.2 统一为 New(cached, raw, vaultKey, vaultCfg)，以代码为准 | 已关闭 | RV-06 | 2026-11-17 |
| P14 | 建议 | 配置示例含未实现字段 | 已改 | 报告 §7.8 示例逐字段标注（已实现 / Phase 2 预留） | 已关闭 | RV-06 | 2026-11-17 |
| P15 | 建议 | List 全表扫描 | 已改 | loadRoot 一次性构建 children 索引（目录→子项，排序），List O(子项)；manifest 缺失分支同样初始化 | 已关闭 | RV-06 | 2026-11-17 |
| P16 | 建议 | 信号量三处细节 | 已改 | ① semDecryptReader.Close sync.Once 幂等 + 双关闭测试 ② acquire 响应 ctx 取消 ③ 获取时机前置列入 Phase 1 设计（P9 契约） | 已关闭 | RV-06 | 2026-11-17 |
| P17 | 建议 | 验收口径需写清 | 已改 | 报告 §9 注明 55GB 用例执行环境为开发机（AX6 存储限制），真机验证在 Phase 1；PROGRESS 验收行补 bench 口径（单线程/1 MiB 分块/纯内存无 I/O） | 已关闭 | RV-06 | 2026-11-17 |
| P18 | 建议 | Obj 前缀校验、manifest 路径硬编码 | 已改 | loadRoot 校验 e.Obj 前缀 _mc/o/（非法条目跳过+告警）；manifest 固定 _mc/manifest.mcf 随 P14 示例标注写清 | 已关闭 | RV-06 | 2026-11-17 |


### RV-2026-11-17-05（第七轮复核，5 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I5 | 重要 | 空 MIME → httpHeader 返回 nil → hdr.Set panic（I3 回归） | 已改 | Link 改用 http.Header{} 显式构造（空 MIME 不设 Content-Type，ETag 照常）；删除 httpHeader；补 TestVaultLinkEmptyMIME | 已关闭 | RV-06 | 2026-11-17 |
| P19 | 建议 | design §4.1 示例含不存在的 v.Start | 已改 | 删除该行并注明「示例仅示意装配顺序，实际接口以代码为准」 | 已关闭 | RV-06 | 2026-11-17 |
| P20 | 建议 | §7.8 JSON 示例含 // 注释（非法 JSON） | 已改 | 注释移出代码块（字段实现状态列表置于块后），JSON 保持合法 | 已关闭 | RV-06 | 2026-11-17 |
| P21 | 建议 | 信号量前置未写入 design §4.1 | 已改 | §4.1 增「信号量必须在 raw.Link 之前获取」（Phase 1 验收口径） | 已关闭 | RV-06 | 2026-11-17 |
| P22 | 建议 | List 共享切片；Warning 无调用方 | 已改 | List 返回副本（防污染索引）；Warning 注明现状并记 TODO 技术债（Phase 1 接 /admin） | 已关闭 | RV-06 | 2026-11-17 |


### RV-2026-11-17-07（Phase 1 第一块：上游取数层，9 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| U1 | 阻断 | 缓冲命中短读不补齐，每约 4 MiB 误报「截断」 | 已改 | ReadAt 循环补齐至填满或 EOF；缓冲命中改为完整覆盖判定；补块长×8 跨窗口测试（断言 3 请求、逐块一致） | 文档修订完成 | 待复核 | 2026-11-17 |
| U2 | 重要 | 随机 Seek 后预读永久失效 | 已改 | 单块读取同样写入缓冲（替换旧缓冲），后续顺序读自然延续；补跳跃恢复测试（3 连续块仅 1 请求） | 文档修订完成 | 待复核 | 2026-11-17 |
| U3 | 重要 | 信号量仍在 raw.Link 之后 | 已改 | 接线时落实（下一块 Vault.Link 走 Upstream 时前移，P21 契约） | 文档修订完成 | 待复核 | 2026-11-17 |
| U4 | 重要 | 锁外写状态；单飞与串行契约矛盾 | 已改（方案 a） | 删除单飞逻辑；注释明确串行契约（DecryptReader 单 goroutine，ServeContent 保证）；全部状态写入均在 mu 内 | 文档修订完成 | 待复核 | 2026-11-17 |
| P1 | 建议 | 200 大偏移不重试与 I10 不符 | 已改 | 200 → 换签名重试一次；两次 200 后分级（≤2 MiB 丢弃、否则 ErrRangeNotSupported） | 文档修订完成 | 待复核 | 2026-11-17 |
| P2 | 建议 | 416 触发无谓换签名 | 已改 | errRangeInvalid 哨兵：416 直接返回不刷新 | 文档修订完成 | 待复核 | 2026-11-17 |
| P3 | 建议 | Link.Header 全量透传 | 已改 | 白名单透传（User-Agent、Referer），响应语义头不带入请求 | 文档修订完成 | 待复核 | 2026-11-17 |
| P4 | 建议 | 测试缺口 | 已改 | 补缓冲中途 403（换签名恢复）与 Content-Range 错位（重试成功）测试；并发刷新测试随 U4(a) 不再适用 | 文档修订完成 | 待复核 | 2026-11-17 |
| P5 | 建议 | 200 路径放弃 keep-alive 应注释 | 已改 | discardServe 注明「读完即弃，放弃连接复用（可接受取舍）」 | 文档修订完成 | 待复核 | 2026-11-17 |


### RV-2026-11-17-08（Phase 1 代码审核，11 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 206 短 body 未区分截断与 EOF | 已改 | 按 Content-Range 声称区间长度比对实际字节（<声称即截断 → 换签名重试）；补 TestUpstreamTruncatedBodyRetries | 文档修订完成 | 待复核 | 2026-11-17 |
| I2 | 重要 | 416 语义与 EOF 不统一 | 已改 | 416 解析 bytes */N：off ≥ N → (0, io.EOF)；TestUpstream416 更新为 EOF 预期 | 文档修订完成 | 待复核 | 2026-11-17 |
| I3 | 重要 | 百度挂载 Vault 必然失败 | 已拍板 | PM 拍板：只支持天翼/移动（铁律 2 收紧）；百度退出支持目标；Vault 对代理流驱动返回 502「不在支持范围」；README 支持表与已知限制更新 | 已裁决 | PM 已拍板 | 2026-11-17 |
| I4 | 重要 | 自愈静默覆盖编辑、备份坏时先覆盖后失败 | 已改 | 损坏文件另存 config.json.broken-<时间戳>（0600）；备份先内存验证再替换；日志 ERROR + broken 路径 + bak mtime + mounts 数；补两用例（备份好→副本保留/备份坏→原文件不动） | 文档修订完成 | 待复核 | 2026-11-17 |
| I5 | 重要 | 自愈复活旧凭证需明示 | 已改 | 日志明示「凭证可能已过期，请在 /admin 核对」；design §3.3 写明自愈仅针对 JSON 语法错误 | 文档修订完成 | 待复核 | 2026-11-17 |
| P1 | 建议 | loadRoot 对 URL 驱动两次 Link | 已改 | NewUpstreamWithLink 复用已获取的 Link | 文档修订完成 | 待复核 | 2026-11-17 |
| P2 | 建议 | ErrUpstream 未映射 502 | 已改 | driver.ErrUpstream 哨兵 + 上游错误统一 %w 包装 + webdav 映射 502 | 文档修订完成 | 待复核 | 2026-11-17 |
| P3 | 建议 | 绑定校验放行空路径 | 已改 | fail-closed：空 metadata.p 一律拒绝 | 文档修订完成 | 待复核 | 2026-11-17 |
| P4 | 建议 | httpClient 30s 超时偏紧 | 已改 | 90s（4 MiB 窗口弱网余量，注释说明） | 文档修订完成 | 待复核 | 2026-11-17 |
| P5 | 建议 | URL 驱动接管同名目录需记录 | 已改 | README 已知限制补「vault 根名匹配即接管」边界语义 | 文档修订完成 | 待复核 | 2026-11-17 |
| P6 | 建议 | rangeReadCloser 依赖外层限长 | 已改 | 注释说明依赖 LoadManifest 的 LimitReader | 文档修订完成 | 待复核 | 2026-11-17 |


### RV-2026-11-17-09（第十轮复核，9 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I6 | 重要 | .bak 仅管理页产生，未用过 /admin 的机器自愈无效 | 已改 | Load 成功后无 .bak 则补写（0600）；默认配置创建分支同样；补 TestLoadCreatesBackupIfMissing | 文档修订完成 | 待复核 | 2026-11-17 |
| I7 | 重要 | 空 config.json 不自愈 | 已改 | 自愈拆两步：副本仅非空时保留、恢复无论空否均执行；补 TestLoadSelfHealEmptyFile | 文档修订完成 | 待复核 | 2026-11-17 |
| P7 | 建议 | Content-Range bytes a-b/* 不容错 | 已改 | 总额未知容错（仅校验起点、跳过截断判定）；补 TestUpstreamUnknownTotal | 文档修订完成 | 待复核 | 2026-11-17 |
| P8 | 建议 | 部分上游失败未包装 502 | 已改 | 206 读取失败与签名刷新失败统一 %w driver.ErrUpstream | 文档修订完成 | 待复核 | 2026-11-17 |
| P9 | 建议 | 合法短窗口误判 EOF | 已改 | 未到文件尾/总额未知的短读返回 (n, nil) 由 ReadAt 续拉；补 TestUpstreamShortWindowContinuation（128 KiB 窗口续拉整块） | 文档修订完成 | 待复核 | 2026-11-17 |
| P10 | 建议 | .broken 无清理 | 已改 | pruneBroken 保留最近 3 份；补测试 | 文档修订完成 | 待复核 | 2026-11-17 |
| P11 | 建议 | 百度挂载仍建虚拟树 | 已拍板 | PM 拍板：加密不支持百度（写入铁律 2）——loadRoot 对非 ReaderAt 流式驱动跳过（不建树、物理视图）；补逻辑 | 已裁决 | PM 已拍板 | 2026-11-17 |
| P12 | 建议 | httpClient 注释重复 | 已改 | 删重 | 文档修订完成 | 待复核 | 2026-11-17 |
| P13 | 建议 | 百度驱动标 legacy | 已改 | drivers/baidu 包注释标注「legacy：不做新开发/Vault 适配/勿误改」 | 文档修订完成 | 待复核 | 2026-11-17 |


### RV-2026-11-17-10（第十一轮复核，7 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I8 | 重要 | 默认配置写入 .bak，自愈可能回退 admin/admin | 已改 | 默认配置创建分支不写 .bak；自愈前检查备份为默认配置则拒绝并提示手工修复；补两用例（默认不备份/默认备份拒自愈） | 文档修订完成 | 已闭环 | RV-11 通过 | 2026-11-17 |
| I9 | 重要 | README 百度措辞与 P11 行为不符 | 已改 | 改为「不启用任何 Vault 行为；_mc 密文对象可被直接下载（无密钥无法解密）」，副作用明示 | 文档修订完成 | 已闭环 | RV-11 通过 | 2026-11-17 |
| P14 | 建议 | 跳过分支无测试 | 已改 | fakeRaw 增 streamOnly（仅 Read 无 ReadAt）+ TestVaultStreamOnlyDriverSkipped | 文档修订完成 | 已闭环 | RV-11 通过 | 2026-11-17 |
| P15 | 建议 | .bak 损坏时永不刷新 | 已改 | ensureBackup：缺失或解析失败即用当前好配置覆盖；补测试 | 文档修订完成 | 已闭环 | RV-11 通过 | 2026-11-17 |
| P16 | 建议 | .bak 陈旧度 | 已改 | 选方案②：只在缺失/损坏时刷新（保护管理页回滚链）；design §4.3 写明 + 日志已有 bak_mtime | 文档修订完成 | 已闭环 | RV-11 通过 | 2026-11-17 |
| P17 | 建议 | pruneBroken 排序前提注释 | 已改 | 注释说明 10 位秒级时间戳字典序=时间序的前提与失效条件 | 文档修订完成 | 已闭环 | RV-11 通过 | 2026-11-17 |
| P18 | 建议 | design §4.3 缺第三态 | 已改 | 补「驱动能力不足 → 完全不建 vault 根（物理视图）」 | 已闭环 | RV-11 通过 | 2026-11-17 |

### RV-2026-11-17-11（第十二轮复核，3 条——Phase 1 代码评审闭环）

> 结论：**通过**。RV-10 七题全部实质关闭；新增 P19–P21 三条建议（均为配置自愈边界），本轮已顺手全部处置。**Phase 1 代码评审循环就此关闭**（RV-07 → RV-11 五轮共 36 条意见，全部闭环）。

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| P19 | 建议 | 升级机器遗留的「默认 .bak」不会被刷新，自愈被永久拒绝 | 已改 | ensureBackup：现有 .bak 是默认配置而当前配置非默认时，顺带刷新一次（恢复自愈能力）；补 TestEnsureBackupRefreshesLegacyDefault | 已闭环 | 通过 | 2026-11-17 |
| P20 | 建议 | isDefaultBackup 语义偏宽（local-only 用户自愈不可用） | 已改 | 改为与 DefaultConfig() 字段级精确比较（admin/admin + 单个 local + 无 vault）；local-only 自定义配置可正常自愈；补 TestSelfHealLocalOnlyCustom | 已闭环 | 通过 | 2026-11-17 |
| P21 | 建议 | 拒绝自愈返回的错误不带原因 | 已改 | fmt.Errorf("备份为默认配置，拒绝自愈…：%w", err)，stderr 与日志一致；测试断言错误文案 | 已闭环 | 通过 | 2026-11-17 |

### RV-2026-11-18-01（崩溃缺陷评审，7 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| A | 重要 | doRange 读入中转产生瞬时双份（崩溃根因） | 已改 | 直接读入复用窗口缓冲；填充前置 bufStart/bufEnd=0、一次性预扩、截断判定用实际 n、网络读取不持 mu | 已实施 | 待真机压测复验 | 2026-11-18 |
| B | 建议 | 解密块缓冲每块新建 | 已改（扩展） | cbuf+pbuf 复用（gcm.Open(dst[:0])），每流 −2 MiB + 降 GC 压力 | 已实施 | 待真机压测复验 | 2026-11-18 |
| C | 建议 | max_streams 默认 4→3 | 暂缓 | 先 A+B 实测峰值再定；降默认属功能缩减 | 待实测 | | 2026-11-18 |
| D | 可选 | SetMemoryLimit | 有条件采纳 | A+B 实测后加，建议 64 MiB + gctrace 验证 | 待实测后拍板 | | 2026-11-18 |
| E | 重要 | ctx 取消被当上游异常触发换签重试 | 已改 | fetchRange 入口/失败后判 ctx.Err() 直接返回；webdav 取消类降 Debug；补 TestUpstreamCancelNoRefresh | 已实施 | 待复核 | 2026-11-18 |
| F | 重要 | 崩溃即长期宕机（空窗 ~14 分钟） | 已交付 | scripts/procd-mini-cloud.init（respawn 3600 5 5，日志经 sh -c 重定向兼容健康检查）；deploy-router.sh 三处 procd 感知 | 待 PM 安装 | | 2026-11-18 |
| G | 建议 | 缺运行时内存观测 | 已改 | -debug 下每 5 分钟 slog MemStats（HeapInuse/HeapSys/NumGC/goroutines） | 已实施 | 待复核 | 2026-11-18 |

### RV-2026-11-18-02（崩溃修复复核，6 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | procd 下健康检查可能假成功（crash-loop 判通过不回滚） | 随 F 关闭 | PM 拍板移除 procd 守护（见 TODO），假成功场景不存在 | 已闭环 | 通过 | 2026-11-18 |
| P1 | 建议 | A 读循环缺零进展防御 | 已改 | 连续 100 次零字节读取 → ErrUpstream（io.Copy 同款阈值） | 已实施 | 待复核 | 2026-11-18 |
| P2 | 建议 | B 缺零分配回归断言 | 已改 | TestLoadChunkReusesBuffers：Benchmark AllocedBytesPerOp < 64 KiB（锁定复用、防回退） | 已实施 | 待复核 | 2026-11-18 |
| P3 | 建议 | procd 脚本未自动安装 | 随 F 关闭 | 守护整体移除 | 已闭环 | 通过 | 2026-11-18 |
| P4 | 建议 | 崩溃报告数字口径不一致 | 已改 | §2.2 段落与 §3 预算统一为评审复核值（12–13 MiB/流）与最终设计（默认 3 流 + 64 MiB 软上限） | 已实施 | 待复核 | 2026-11-18 |
| P5 | 建议 | memstats 采样间隔偏粗 | 已改 | -debug 下 5 分钟 → 1 分钟（压测可画出内存曲线） | 已实施 | 待复核 | 2026-11-18 |

### RV-2026-11-18-03（崩溃修复复核第三轮，6 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | 默认 3 与「重放 4 流压测」预期冲突；HEAD 也占槽 | 已改 | 验收口径定稿（允许 503、统计来源、RSS<60 MB 判定线）；503 加 Info 日志；HEAD 占槽列为观察项（实测影响使用再移出信号量） | 已实施 | 待复核 | 2026-11-18 |
| I2 | 重要 | SetMemoryLimit 在 manifest 解析前生效，大清单可能 GC 抖动 | 已改 | 选方案①：生效点移到挂载装配（vault.Init/manifest 解析）之后，只护运行期 | 已实施 | 待复核 | 2026-11-18 |
| P6 | 建议 | feasibility §7.7 内存口径未同步 | 已改 | 整段重写：6 MiB/流、默认 3 流 ≈18 MiB、64 MiB 软上限兜底、运行期总内存 25–35 MB | 已实施 | 待复核 | 2026-11-18 |
| P7 | 建议 | 全仓「4 路」表述扫描 | 已改 | design §4.1 每流 ≤8 MiB → ≈6 MiB；feasibility §4 历史分析行保留（记录当时事实） | 已实施 | 待复核 | 2026-11-18 |
| P8 | 建议 | 移除守护的运维后果记录 | 已改 | README 已知限制补「fatal 后需手工重启（PM 拍板不装守护）+ 重启命令」 | 已实施 | 待复核 | 2026-11-18 |
| P9 | 建议 | 复用断言改 AllocsPerRun | 已改 | 改 TotalAlloc 增量断言（轻量、字节级，3 次块加载 <256 KiB） | 已实施 | 待复核 | 2026-11-18 |

### RV-2026-11-19-01（Phase 1 验收收官复核，3 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| P12 | 建议 | 「RSS<60 MB」判据与实测不符 | 已改 | 崩溃报告复验判定线正式更新为「RSS ≤ ~70 MB（64 MiB 软上限+运行时开销）且 GC 回落正常、MemFree ≥ ~20 MB、无 OOM」 | 已闭环 | 通过 | 2026-11-19 |
| P13 | 重要 | 天翼家庭 CDN ~50 Mbps 限速未入文档 | 已改 | README 已知限制 + feasibility §5.1 补「4K 高码率必然卡顿、与网关无关」；TODO 技术债记 Phase 2 limitrate 调研 | 已闭环 | 通过 | 2026-11-19 |
| P14 | 建议 | 复验时长口径 | 已改 | 崩溃报告「≥10 分钟」改为「≥5 分钟（4.5min 下载 + 15min 720P + 5min 4K 三组互补覆盖）」 | 已闭环 | 通过 | 2026-11-19 |

### RV-2026-11-19-02（Phase 2 设计评审，闸门① 有条件通过，10 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 拍板 | 容器方案使一级目录名明文可见 | PM 拍板 | 接受明文目录名（影视库不敏感）；高敏感场景的「编号+显示名」间接层后续可加 | 已闭环 | 待复核 | 2026-11-19 |
| I2 | 拍板 | 增量粒度=子目录，子目录内仍全量重加密 | PM 拍板 | 接受，按剧集/专辑/年份适度粒度分子目录；合并模式留技术债 | 已闭环 | 待复核 | 2026-11-19 |
| I3 | 重要 | 懒扫描时延与阻塞边界未定 | 已补 | 触发 List 立即返回旧树 + 一次性 goroutine 异步换树（refreshMu 去重）；manifest 读独立 ≤5s 短超时客户端；单目录失败隔离 | 已实施（设计） | 待复核 | 2026-11-19 |
| I4 | 重要 | 变更摘要口径未写明 | 已补 | 摘要 = 解密后清单内容 SHA-256（解析时顺手算）；不做 generation 字段（避免格式与 mcrypt 联动） | 已实施（设计） | 待复核 | 2026-11-19 |
| P1 | 建议 | findRoot/_mc 层级需重构说明 | 已补 | §5.1 写明两级结构（容器→子根）与 isMcPath 第二段判定 | 已实施（设计） | 待复核 | 2026-11-19 |
| P2 | 建议 | mcrypt 用法变化需写明 | 已补 | 逐子目录各跑一次 mcrypt + 先传对象后传清单 | 已实施（设计） | 待复核 | 2026-11-19 |
| P3 | 建议 | 旧单根迁移方式未说明 | 已补 | 单组：云盘移 _mc 零重加密；多组：逐组重跑；README 实现期同步 | 已实施（设计） | 待复核 | 2026-11-19 |
| P4 | 建议 | 容器根 stray 文件行为未定义 | 已补 | stray 文件按普通文件处理（302 下载），不影响加密目录 | 已实施（设计） | 待复核 | 2026-11-19 |
| P5 | 建议 | PROGRESS 进行中拼接残留 | 已改 | 清理为两条独立进行中（观察期 / Phase 2 设计） | 已闭环 | 通过 | 2026-11-19 |
| P6 | 建议 | 章节重编号交叉引用 | 已核实 | README/feasibility 引用均为 §4.x（未重编号），无残留 | 已闭环 | 通过 | 2026-11-19 |

### RV-2026-11-19-03（Phase 2 设计复核——闸门① 放行，4 条 P 级）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| P7 | 建议 | 嵌套 _mc（第三层）被静默普通化 | 实现期补 | 扫描时对子目录再 List 一层，发现更深层 _mc 即告警（防「加密未生效 + 元数据暴露」） | 待实现 | | 2026-11-19 |
| P8 | 建议 | 整块换新残留旧对象 | 实现期补 | 文档提示「换新后清理旧 _mc/o」；mcrypt --clean 评估入技术债（TODO 已记） | 待实现 | | 2026-11-19 |
| P9 | 建议 | 异步扫描 goroutine 的 ctx 语义 | 实现期补 | 扫描用后台 ctx（非请求 ctx），仅受每目录 ≤5s 超时约束；代码注释注明 | 待实现 | | 2026-11-19 |
| P10 | 建议 | 「就地覆盖 vs 就地修改」措辞 | 已改 | design 使用模型补一句：整块覆盖=换新（允许）、单文件就地改=拒绝 | 已闭环 | 通过 | 2026-11-19 |

### RV-2026-11-19-04（Phase 2 代码审核，闸门③ 有条件通过，5 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| P5 | 需确认 | CommitCredential 是否清除 hostWarn | 已核实 | credential.go:106 已清 + TestHostWarnClearedOnCommit 已断言（审核 diff 遗漏该文件） | 已闭环 | 通过 | 2026-11-19 |
| P1 | 建议 | 容器级 raw.List 无短超时 | 已改 | scan 的容器 List 包 manifestReadTimeout（≤5s），与每目录口径一致 | 已闭环 | 待复核 | 2026-11-19 |
| P2 | 建议 | 初始同步扫描串行 N×5s 阻塞启动 | 已改 | 子目录并发扫描（WaitGroup），总时长 ≈ 单目录 5s | 已闭环 | 待复核 | 2026-11-19 |
| P3 | 建议 | disableVault 后 scan 循环继续 | 已改 | isDisabled() 早退（refreshRoot 入口 + 扫描循环 break） | 已闭环 | 待复核 | 2026-11-19 |
| P4 | 建议 | 有效 token + host 失败时 List 仍 500 | 已改 | List/Link 对 host 为空降级（空目录/明确错误，与过期语义对齐）+ 测试 | 已闭环 | 待复核 | 2026-11-19 |

### RV-2026-11-19-06（真机反馈处置复核，2 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| I1 | 重要 | design §5.1 point 3 仍写「无 _mc → 普通模式」，与代码 404 矛盾 | 已改 | point 3 改为「摘除旧树后未找到（404）」，与 point 1 口径一致 | 已闭环 | 通过 | 2026-11-19 |
| P1 | 建议 | 验收清单补「非单元路径 404」用例 | 已改 | 第 5 项旁补：任意非单元路径 PROPFIND/GET 均 404 且不泄露密文 | 已闭环 | 通过 | 2026-11-19 |

### RV-2026-11-19-07（真机二轮反馈处置复核，2 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| P1 | 建议 | TestVaultSupported 改真实驱动断言 | 已改 | 对 tianyi（个人/家庭）/mobile/local/baidu 真实实例断言 Name() ↔ 允许列表 | 已闭环 | 通过 | 2026-11-19 |
| P2 | 建议 | scan List 失败分支粘滞语义注释 | 已改 | 注释说明「有意粘滞：失败无法验证修复，保留更安全；成功后自然清除」 | 已闭环 | 通过 | 2026-11-19 |

### RV-2026-11-20-01（外部审核意见二次核实，12 条 + e2e）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| 1 | P0 | keygen 覆盖已有 master key | 阻断 | O_CREATE\|O_EXCL + --force 覆盖 + 4 组测试（存在拒绝/内容不变/force/失败无残留） | 已闭环 | 待复核 | 2026-11-20 |
| 7 | P1 | vault.key 未入 .gitignore | 阻断 | 加 vault.key、*.key | 已闭环 | 待复核 | 2026-11-20 |
| 4 | P1 | manifest 并发扫描峰值 + 软上限时机 | 补充 | 全局解析并发限 2 + SetMemoryLimit 前移 vault.Init 前 | 已闭环 | 待复核 | 2026-11-20 |
| 5 | P1 | EncryptObject 不校验总字节 | 补充 | total!=size 报错 + size<0 校验 | 已闭环 | 待复核 | 2026-11-20 |
| 2 | P1 | vault.paths 缺严格校验 | 补充 | 启动严格校验（含 MaxStreams 天花板 4，PM 拍板） | 已闭环 | 待复核 | 2026-11-20 |
| 3 | P1 | buildRoot 相对路径死循环 | 补充 | ValidateVirtualPath + validateManifestPaths fail-closed | 已闭环 | 待复核 | 2026-11-20 |
| 10 | P2 | getObj 伪装目录（含 403） | 补充 | List 错误如实上抛，ErrForbidden 透传 403 | 已闭环 | 待复核 | 2026-11-20 |
| 8 | P2 | verify/服务端忽略 hex 解码错误 | 补充 | 解码失败/长度≠32 判校验失败（两处） | 已闭环 | 待复核 | 2026-11-20 |
| 6 | P1 | SaveManifest 无约束/非原子 | 可选 | 三层尺寸校验（16 MiB，PM 拍板）+ temp+rename | 已闭环 | 待复核 | 2026-11-20 |
| 9 | P2 | decrypt 路径逃逸 | 可选 | ValidateVirtualPath + 输出边界校验 | 已闭环 | 待复核 | 2026-11-20 |
| 11 | P2 | 告警残留（root 级） | 可选 | 读取成功即 clearRootWarning | 已闭环 | 待复核 | 2026-11-20 |
| 12 | P2 | 双重 raw.Link | 改判成立 | 常驻审核记错位置（NewUpstreamWithLink 仅在清单加载）；Vault.Link 已改为复用已取链接 | 已闭环 | 待复核 | 2026-11-20 |
| — | — | 端到端测试 | 补充 | e2e_test.go 5 组 9 场景（PROPFIND/GET/Range/Seek/SHA-256/错钥/篡改/清单缺失/损坏） | 已闭环 | 待复核 | 2026-11-20 |

### RV-2026-11-20-03（外部审核第二轮核实，5 条）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| R2-1 | P1 | 两种信号量非全局（每挂载独立额度） | 阻断 | 进程级共享信号量 + 全局默认 3（PM 拍板）+ SetGlobalStreamLimit + 跨挂载共享测试 | 已闭环 | 待复核 | 2026-11-20 |
| R2-2 | P1 | keygen --force 失败丢旧钥 + 权限不改 | 阻断 | 原子替换（temp 同目录 + 0600 + fsync + rename），失败旧钥不动 + 3 组测试 | 已闭环 | 待复核 | 2026-11-20 |
| R2-3 | P1 | 配置语义错误误触发 .bak 自愈 | 阻断 | semanticError 哨兵 + Load 分流（仅语法错误自愈）+ 2 组测试 | 已闭环 | 待复核 | 2026-11-20 |
| R2-4 | P2 | decrypt 符号链接/e.Obj 同源/Abs 错误 | 补充 | e.Obj == ObjectPath(EncodeFileID(fileID)) 不变量校验 + Abs 错误如实返回 | 已闭环 | 待复核 | 2026-11-20 |
| R2-5 | P2 | mcrypt encrypt 未复用路径校验 | 补充 | 加密时即校验虚拟路径规范（超长/超深提前报错） | 已闭环 | 待复核 | 2026-11-20 |

### RV-2026-11-20-04（外部审核第三轮，3×P1 闭合确认 + 3×P2）

| 编号 | 级别 | 意见摘要 | 处置类别 | 处置说明 | 状态 | 复核 | 日期 |
|---|---|---|---|---|---|---|---|
| — | P1 | R2-1/R2-2/R2-3 复核确认闭合 | 确认 | 审核员逐条核对代码确认（全局信号量生命周期/原子替换/自愈分流） | 已闭环 | 通过 | 2026-11-20 |
| P2-1 | P2 | decrypt 输出目录符号链接逃逸 + Dirs 未校验 | 已改 | safeOutPath 统一处理（Files/Dirs 同函数、拒反斜杠、逐级 Lstat 符号链接检查）+ 2 组测试（Linux 全测，Windows 自动跳过） | 已闭环 | 待复核 | 2026-11-20 |
| P2-2 | P2 | keygen 原子替换缺父目录同步 | 已改 | rename 后父目录 Sync（与 conf 同口径，降级忽略）+ 目录只读写失败旧钥不变测试（root 跳过） | 已闭环 | 待复核 | 2026-11-20 |
| P2-3 | P2 | manifest 额度等待不响应取消 | 已改 | select mctx.Done() 退出；双实例争抢/预算超时退出 2 组测试（manifestReadTimeout 改 var） | 已闭环 | 待复核 | 2026-11-20 |
| — | 测试 | 审核 5 条缺失测试可行性评估 | 已评估 | 4 全可测 + 1 条件可测（root/Windows 跳过）+ 1 不可测（同步失败，审查覆盖） | 已闭环 | — | 2026-11-20 |
