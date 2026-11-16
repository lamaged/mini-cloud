# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

Mini Cloud WebDAV 网关 —— 一个只读 WebDAV 桥接服务，将国内云盘（天翼 `tianyi`、移动 `mobile`、百度 `baidu`，另有 `local` 本地文件系统驱动）统一暴露为 WebDAV 服务，专为资源受限的路由器（OpenWrt ARM64）设计。纯 Go 标准库实现（`go 1.22+`），零外部依赖，无 Makefile、无 CI。代码注释与面向用户的字符串均为中文。

**开发环境是 x86 Ubuntu 虚机，真实运行设备是红米 AX6（OpenWrt ARM64）**：性能数据（AES-GCM、代理吞吐）必须以 AX6 实测为准；发版/真机验收必须用静态交叉编译（CGO_ENABLED=0）。

**进行中的路线**（详见 [docs/design.md](docs/design.md)）：
- v0.10.0：凭证网页管理 —— 浏览器页面直接更新移动云盘 authorization，免 SSH（解决移动 token 约 30 天硬过期痛点）；附带独立修复 3 处现存转义缺陷（href 编码、目录页 HTML 转义、根 PROPFIND XML 转义）。
- v1.0+：Vault 加密文件 WebDAV 还原播放（可行性实测见 [docs/vault-feasibility.md](docs/vault-feasibility.md)）。

开发与审核流程见 [docs/process.md](docs/process.md)：角色分工（PM 非程序员，对话用交易语言）、10 条铁律、沟通协议、审核闭环（外部审核员每功能审一次）、6 道闸门、回滚策略。每个功能必须先过设计评审再写代码；进度与审核意见留痕在 PROGRESS.md / TODO.md。

## 常用命令

Go 工具链不在默认 PATH 中 —— 先 `source env.sh`（GOROOT 指向 `/home/test/go`、GOPATH 指向 `/home/test/go-path`）。

```bash
source ./env.sh

# 构建与运行
go build -o mini-cloud .
./mini-cloud -config config.json        # -debug 调试日志，-quiet 仅错误日志

# 交叉编译（OpenWrt ARM64 路由器，发版/真机验收必用）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o mini-cloud-arm64 .
~/upx-5.2.1-amd64_linux/upx --best -o mini-cloud-arm64-upx mini-cloud-arm64

# 测试（提交前跑 -race；云盘驱动用 httptest 假服务器，不得使用真实凭证）
go vet ./...
go test ./... -race
go test ./internal/webdav -run TestXxx   # 运行单个测试
```

如需访问外部网络，请按开发机本地笔记配置代理（代理地址属本机环境，不入库）。

## 架构

每个云盘挂载的请求链路：

```
main.go（Basic Auth、路由、根索引页）
  → webdav.Handler（OPTIONS / PROPFIND / GET / HEAD）
    → fs.CachedReader（目录 + 链接缓存）
      → fs.RetryReader（失败重试，"not found" 不重试）
        → drivers/<type>.Driver（List / Link）
          → 云盘 HTTP API
```

- `Driver` 接口（[internal/driver/driver.go](internal/driver/driver.go)）非常精简：`Name()`、`Init(ctx)`、`List(ctx, path)`、`Link(ctx, path)`。`List` 返回 `[]model.Obj`（Name/Size/ModTime/IsDir）；`Link` 返回 `model.Link`，且恰好只有两种模式之一：
  - `URL != ""` → handler 返回 302 重定向到云盘 CDN（天翼、移动）。客户端直连下载，无法转发 Range。
  - `Reader != nil` → handler 在服务端代理数据流，通过 `http.ServeContent` 支持 Range（百度，必须设置 `User-Agent: netdisk`）。`Link.StatusCode` 仅用于非 seekable 数据流（`streamReader`）；seekable Reader 的 200/206/416 由 `http.ServeContent` 依据 Range 自行计算。
- 可选能力接口（全部通过类型断言检测，**断言对象必须是原始驱动，不是包装层**）：
  - `TokenManager`（`NeedRefresh`/`RefreshToken` —— 仅百度实现；[internal/auth/watcher.go](internal/auth/watcher.go) 的后台维护器每 5 分钟轮询一次）。
  - `HealthReporter`（`HealthWarning` —— 移动云盘上报 authorization 过期；挂载显示空目录 + 提示性显示名）。
  - `CredentialUpdater`（规划中，v0.10.0）—— 支持运行时更新凭证，服务 `/admin` 管理页；main 负责把新凭证持久化回 config.json（原子写 + .bak 备份）。
- 中间件链，在 main.go 中为每个挂载构建：`fs.CachedReader`（[internal/fs/cache.go](internal/fs/cache.go)）目录缓存 TTL 30 秒、链接缓存 TTL 10 分钟，容量上限硬性约束（500 个目录 / 1000 个链接，淘汰最久未访问条目），5 分钟一次的清理 goroutine（通过 `Handler.Close()` 停止）；`fs.RetryReader`（[internal/fs/retry.go](internal/fs/retry.go)）最多重试 3 次、间隔 500ms，对包含 "not found"/"no such file"/"does not exist"/"cannot find" 的错误绝不重试。
- 挂载：`clouds` 配置数组的每一项对应一个挂载，路径为 `/<name>/`。`name` 同时是 URL 前缀，必须唯一；同一驱动类型配置多个实例是常态（如 天翼个人 + 天翼家庭）。根路径 `/` 的索引（HTML 页面 + PROPFIND）在 main.go 中合成，不属于 webdav 包。
- 云盘路径解析：逐级对照目录列表将路径解析为云盘原生 ID，每个驱动内部有 `path→ID` 内存缓存（见天翼/移动驱动的 `resolveFolderID`）。
- Vault（规划中，v1.0+）：包装层插在 CachedReader 之上、只拦截配置的 vault 路径（/backup、/Archive），其余路径原样委托 → 302 行为不变。`StartWatcher` 必须传原始驱动；Vault 与 CachedReader 不得实现 TokenManager/HealthReporter/CredentialUpdater。

## 认证与 token 状态

每个驱动将会话持久化到 `state/<挂载名>_token.json` —— 以挂载名为文件名，而非驱动类型。`state/` 与 `config.json` 已被 gitignore（含真实凭证）；`config.example.json` 是文档化模板。`conf.Load` 在配置文件不存在时自动创建默认配置，并将旧版单数 `cloud` 字段归一化为 `clouds`。

各驱动刷新策略因设计而异（见 README「设计决策」）：

- **天翼**：被动刷新 —— 仅当 API 返回 session 过期错误时才刷新；重试上限 1 次，防止无限递归。
- **移动**：无自动续期 —— RCS token 约 30 天硬过期。过期后驱动返回空目录 + `HealthWarning`，提示用户更新配置中的 `authorization`（v0.10.0 起改为网页直接更新，免 SSH）。
- **百度**：主动（5 分钟后台维护）+ 被动 —— 401 回调刷新 OAuth2 access token。

运行时安全网：每个驱动的 API `Client` 都有 `SetAuthRefreshCallback` 401 处理器。它**必须在 `Init()` 中、任何 early return 之前注册** —— 提交 5ab1f9d 修复过回调注册顺序导致的运行时 401 无法自救 bug。

驱动上的凭据字段由互斥锁保护，必须通过 getter/setter 访问（如百度的 `getRefreshToken`/`setRefreshToken`）—— 提交 be1c043 修复过相关数据竞争；请保持凭据状态受锁保护。

每次发版时，main.go 中的 `version` 常量与 README 变更记录同步更新。

## 文档

- README.md —— 产品文档、变更记录、设计决策。
- docs/design.md —— 功能设计文档（目标、接口、验收标准、里程碑）。
- docs/process.md —— 开发与审核流程（角色分工、铁律、沟通协议、审核闭环、6 道闸门、回滚策略）。
- PROGRESS.md —— 开发进度与审核意见处置（专业语言，外部审核员阅读）。
- TODO.md —— 待拍板事项、技术债、驳回待裁决。
- docs/vault-feasibility.md —— Vault 加密方案可行性报告（AX6 性能、CDN Range、URL 时效实测数据）。
- docs/review/ —— 外部评审报告归档。
