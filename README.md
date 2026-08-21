# Mini Cloud WebDAV Gateway

轻量级只读 WebDAV 网关，将多个云盘（百度网盘、天翼云盘、移动云盘）桥接为统一的 WebDAV 服务，专为路由器（OpenWrt ARM64）等资源受限设备设计。

## 支持的云盘

| 云盘 | 驱动类型 | 认证方式 | 视频播放 |
|------|---------|---------|---------|
| 天翼云盘（个人/家庭） | `tianyi` | 用户名+密码 | 302 重定向，直连 CDN |
| 移动云盘（和彩云） | `mobile` | Authorization 令牌 | 302 重定向，直连 CDN |
| 百度网盘 | `baidu` | refresh_token (OAuth2) | 服务端代理下载，支持 Range |
| 本地文件系统 | `local` | 无 | 直接读取 |

## 架构

```
TV / Nova Player / 网易爆米花 / Infuse
          │
          ▼
┌─────────────────────────┐
│   Mini Cloud WebDAV     │  ← 路由器 (OpenWrt ARM64)
│   :8088                 │
│                         │
│  ┌─────────────────────┐│
│  │ WebDAV Handler      ││  PROPFIND / GET / HEAD / OPTIONS
│  │ (Basic Auth)        ││
│  └──────┬──────────────┘│
│         │               │
│  ┌──────▼──────────────┐│
│  │ CachedReader        ││  目录缓存 + 链接缓存 (30s TTL)
│  │ RetryReader         ││  失败重试 (最多3次)
│  └──────┬──────────────┘│
│         │               │
│  ┌──────▼──────────────┐│
│  │ Drivers             ││  ┌─────────┬──────────┬─────────┐
│  │                     ││  │ tianyi  │ mobile   │ baidu   │
│  │                     ││  │ session │ auth     │ OAuth2  │
│  │                     ││  └─────────┴──────────┴─────────┘
│  └─────────────────────┘│
└─────────────────────────┘
          │
          ▼
   ☁️ 天翼云盘 / 移动云盘 / 百度网盘
```

### 认证刷新策略（各驱动独立）

| 驱动 | 策略 | 说明 |
|------|------|------|
| **天翼** | 被动响应 | API 返回 session 过期时触发刷新，**含家庭云 session 同步更新**；重试上限 1 次防死循环 |
| **移动** | 被动响应 | 收到 401 时触发 token 刷新，token 有效期约 30 天 |
| **百度** | 主动 + 被动 | OAuth2 access_token 过期前 5 分钟主动刷新 + 运行时 401 自动重试 |

### 变更记录

#### v0.9.1 (2026-08-21)

- **修复** 移动云盘 token 过期后无法自动恢复的问题：
  - `Init()` 的 401 自动刷新回调之前被放在缓存恢复块之后，存在有效缓存时提前 return 导致回调**从未注册**，运行时 401 无法自救；现已前置到任何 early return 之前
  - `refreshToken()` 之前拒绝刷新已过期 token，现已改为过期也尝试刷新（由服务端决定是否接受）
  - 缓存恢复时新增 config 与缓存 token 的过期时间比较，选择更晚（更新）的 token，解决「用户更新 config 但旧缓存覆盖新 token」的问题
- **增强** `TokenState.ExpiresAt` 现在存储从 Authorization 真实提取的过期时间（之前是伪造的「30 天后」值）
- **增强** 刷新失败从静默返回 nil 改为返回明确错误，便于调用方感知

#### v0.5.1 (2026-07-22)

- **修复** 天翼家庭云 session 过期后无法恢复的问题：`refreshSession()` 现在同步更新个人云和家庭云 session key；API 重试上限 1 次防止无限递归
- **增强** 天翼云 API 错误检测：新增 `FamilySessionKey`、`familySession` 等家庭云专用过期标志的检测
- **增强** 家庭云诊断日志：配置了 `family_id` 但未获取到 familySessionKey 时输出警告

## 快速开始

### 配置文件

```json
{
    "server": {
        "listen": ":8088",
        "username": "admin",
        "password": "your_password"
    },
    "clouds": [
        {
            "name": "天翼个人",
            "type": "tianyi",
            "username": "手机号",
            "password": "密码"
        },
        {
            "name": "天翼家庭",
            "type": "tianyi",
            "username": "手机号",
            "password": "密码",
            "family_id": "家庭云ID"
        },
        {
            "name": "移动云盘",
            "type": "mobile",
            "authorization": "Base64认证令牌"
        },
        {
            "name": "百度网盘",
            "type": "baidu",
            "refresh_token": "从百度OAuth获取的refresh_token"
        }
    ]
}
```
### 配置说明

**`clouds` 数组非常灵活：**

- **最少配 1 个，最多不限** — 按需选择云盘类型和数量，不需要全部配齐
- **同名云盘可配多个** — 通过 `name` 区分，`name` 即 URL 路径前缀，各条目 `name` 必须唯一

典型场景：

```json
// 只配一个天翼云盘
"clouds": [
    { "name": "天翼云", "type": "tianyi", "username": "...", "password": "..." }
]

// 天翼个人云 + 家庭云（同类型两个实例）
"clouds": [
    { "name": "天翼个人", "type": "tianyi", "username": "...", "password": "..." },
    { "name": "天翼家庭", "type": "tianyi", "username": "...", "password": "...", "family_id": "..." }
]

// 混合：天翼 + 百度
"clouds": [
    { "name": "天翼云",  "type": "tianyi", "username": "...", "password": "..." },
    { "name": "百度网盘", "type": "baidu",  "refresh_token": "..." }
]
```

访问路径对应关系：

| 配置 `name` | WebDAV 路径 |
|------------|-------------|
| `天翼个人` | `http://<IP>:8088/天翼个人/` |
| `天翼家庭` | `http://<IP>:8088/天翼家庭/` |
| `百度网盘` | `http://<IP>:8088/百度网盘/` |

### 获取认证凭证

- **天翼云盘**: 直接使用手机号+密码
- **移动云盘**: 从 alist 或其他工具获取 `authorization` 令牌
- **百度网盘**: 
  1. 访问 [百度 OAuth 授权页](https://openapi.baidu.com/oauth/2.0/authorize?response_type=code&client_id=hq9yQ9w9kR4YHj1kyYafLygVocobh7Sf&redirect_uri=oob&scope=basic,netdisk)
  2. 登录授权后获取 code
  3. 用 code 换取 refresh_token（内置默认 client_id/secret，也可自行配置）

### 运行

```bash
# 本地开发
./mini-cloud -config config.json

# 红米AX6路由器后台运行
    cd /data/mini-cloud
    ./mini-cloud-arm64-upx -config /data/mini-cloud/config/config.json \
    >> /dev/null 2>&1 &

# 调试模式
./mini-cloud -config config.json -debug

# 静默模式
./mini-cloud -config config.json -quiet
```

### 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-config` | `config.json` | 配置文件路径 |
| `-debug` | `false` | 启用调试日志 |
| `-quiet` | `false` | 静默模式（仅错误日志） |

## WebDAV 访问

### 客户端连接

- **地址**: `http://<路由器IP>:8088/`
- **认证**: Basic Auth（配置中设置的 username/password）
- **根路径** `/`: 云盘列表（浏览器打开显示导航页）
- **云盘路径** `/<云盘名称>/`: 如 `/天翼个人/视频/`

### 支持的客户端

已验证的设备/播放器：

- 路由器红米AX6
- 🎬 Nova Player
- 🍿 网易爆米花 (VidxPlayer)
- 🖥️ Edge 浏览器

### 支持的 HTTP 方法

- `OPTIONS` — WebDAV 能力发现
- `PROPFIND` — 目录浏览
- `GET` / `HEAD` — 文件下载（含 Range 断点续传）

## 编译

### 前置条件

- Go 1.22+
- 标准库即可，无外部依赖

### 编译命令

```bash
# 本地编译
go build -o mini-cloud .

# ARM64 交叉编译（OpenWrt 路由器）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o mini-cloud-arm64 .

# UPX 压缩（约 35% 压缩率）
upx --best -o mini-cloud-arm64-upx mini-cloud-arm64
```

编译产物大小：6.5MB → UPX 压缩后 2.3MB

## 项目结构

```
mini-cloud/
├── main.go                 # 入口：路由、认证、驱动加载
├── config.json             # 配置文件
├── go.mod                  # Go module (go 1.22)
├── state/                  # Token 缓存目录（自动生成）
├── drivers/
│   ├── tianyi/             # 天翼云盘驱动
│   │   ├── driver.go       # List / Link / 路径解析
│   │   ├── api.go          # API 客户端、签名、加密
│   │   ├── auth.go         # 登录、session 刷新
│   │   └── types.go        # 数据结构
│   ├── mobile/             # 移动云盘驱动
│   │   ├── driver.go
│   │   ├── api.go
│   │   ├── auth.go
│   │   └── types.go
│   ├── baidu/              # 百度网盘驱动
│   │   ├── driver.go       # 代理下载 + Range 支持
│   │   ├── api.go          # API 客户端 + 401 自动刷新
│   │   ├── auth.go         # OAuth2 token 刷新
│   │   └── types.go
│   └── local/              # 本地文件驱动（开发用）
│       └── driver.go
└── internal/
    ├── auth/watcher.go     # Token 后台维护器（仅 OAuth2 驱动）
    ├── conf/config.go      # 配置加载与验证
    ├── driver/driver.go    # Driver 接口定义
    ├── fs/
    │   ├── cache.go        # 目录/链接缓存层
    │   └── retry.go        # 失败重试层
    ├── model/obj.go        # 数据模型
    └── webdav/
        ├── webdav.go       # WebDAV 协议处理
        ├── prop.go         # PROPFIND XML 生成
        └── xml.go          # XML 工具
```

## 设计决策

### 为什么用 302 重定向而不是代理下载？

天翼云盘和移动云盘的下载链接是 CDN 地址，带宽充足。302 重定向让客户端直连 CDN：
- ✅ 不消耗路由器带宽/CPU
- ✅ 利用 CDN 就近加速
- ❌ 无法转发 Range 请求（CDN 自行处理）

### 为什么百度网盘用代理下载？

百度网盘 CDN 对非会员用户的 User-Agent 有严格校验，直接 302 会导致客户端收到限速或拒绝连接。代理下载可以：
- 设置 `User-Agent: netdisk` 绕过限制
- 转发客户端的 `Range` 头，支持视频拖动

### 为什么不用统一的 Token 刷新？

不同云盘的认证机制差异很大：
- **百度**: OAuth2，access_token 有明确过期时间 → 适合主动刷新
- **天翼**: Session 签名，过期不可预测 → 适合被动刷新（与 alist 一致）
- **移动**: 固定 Token，长期有效 → 只需被动处理 401

## 已知限制

- 百度网盘非会员用户高码率视频可能卡顿（百度 CDN 策略限制）
- 不支持文件上传/修改（只读）
- 天翼云盘频繁密码登录可能触发验证码（建议首次登录后保持 session）

## License

MIT
