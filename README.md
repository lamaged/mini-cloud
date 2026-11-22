# mini-cloud —— 把云盘变成装在你路由器上的只读硬盘

## 这是什么

把云盘变成装在你路由器上的**只读硬盘**：局域网里的电视、电脑、手机、平板，用任何一个支持 WebDAV
的程序，就能浏览、播放、下载云盘里的文件；需要保密的文件，可以在上传之前先加密，云盘上只留密文。

**主要功能：**

- **文件共享**：不装客户端、不登录各家网盘 App——家里任何一台设备连上路由器，看到的是同一份文件列表。
  照片、视频、设计稿、文档、素材包都能放，只读，不会被误删。
- **大文件边播边取**：视频、音频支持拖动进度条（Range），不用等整个文件缓冲下来；
  **加密过的视频同样能边解密边播放、照常拖动**；
- **文件加密**：加密在**上传之前**于本地完成，云盘上只有密文。钥匙（`vault.key`）不上传云盘、不入代码仓库，
  **务必离线备份一份**（丢钥匙 = 密文永久解不开；备份别放进存密文的那个网盘账号里）。

**它不是什么：**

- 不是网盘客户端：**只读**，不能上传、删除、改名（加密文件及文件夹写入只有一条路：`mcrypt` 加密后，用网盘自己的 App 上传）；
- 不是公网服务：默认只在局域网可用；想从外网访问要自己配反向代理，不在支持范围；

**安全及风险**

云盘上只有密文。没有路由器上的 `vault.key`，所有加密文件都无法解密
（唯一的例外：连得上你的服务、并且知道 WebDAV 账号密码的人，可以让路由器替他解密）。

| 安全 | 风险 |
|---|---|
| 云盘上只有密文，服务商看不到文件内容 | 拿到 `vault.key` 文件——可离线解密 |
| 分享链接被转发，对方也只能拿到密文 | 能连上路由器所在网络、且知道 WebDAV 账号密码可在线读取 |
| 云端文件被篡改会解密失败（GCM 校验），不会悄悄换成别的内容 | 文件大小、修改时间等元数据仍可推断 |
| 仓库公开、只含密文的备份外泄，都不会泄露内容 | 弱口令被撞开（默认 `admin`/`admin`，部署第一步必须改） |

> 只有放在加密目录（`vault.paths`）里的文件才受保护，其余目录仍是明文直读。

---

## 快速开始（第一次安装，照做 5 步）

> 前提：红米 AX6 这类 **ARM64 架构的 OpenWrt 路由器**，已开启 SSH。下文命令里的 `<router-ip>` 换成你路由器的局域网地址（一般是 192.168.31.1，小米路由器出厂基本如此）。

### 第 1 步：拿到程序

两个文件：

| 文件 | 用途 |
|---|---|
| `mini-cloud-arm64-upx` | 路由器上运行的主程序 |
| `mcrypt.exe` | Windows 上的加密工具（不用加密功能可不要） |

### 第 2 步：写配置文件

新建一个 `config.json`，照抄下面的模板，把占位符换成你的真实值（各网盘令牌的获取方式见[附录 A](#附录-a各云盘凭证怎么拿)）：

```json
{
    "server": { "listen": ":8088", "username": "admin", "password": "请改成你的密码" },
    "clouds": [
        { "name": "天翼个人", "type": "tianyi", "username": "手机号", "password": "密码" },
        { "name": "移动云盘", "type": "mobile", "authorization": "Base64令牌" }
    ],
    "vault": {
        "enabled": true,
        "key_file": "/data/mini-cloud/config/vault.key",
        "paths": ["backup"],
        "max_streams": 3
    }
}
```

说明：

- `clouds` 里每加一个 `{}` 就多挂一个网盘——**没有固定上限，同一网盘类型可以配多个**（比如两个天翼账号各配一个），只要 `name` 不重复（它会显示在页面上）；实际能挂几个受路由器资源和网盘接口限制；
- `vault.paths` = 加密容器根的名字（云盘根目录下的一个文件夹，只放目录不放文件），可多个；
- **不用加密功能**就把整个 `vault` 段删掉；
- 完整模板见 [config.example.json](config.example.json)（含百度网盘等所有支持的网盘）。

### 第 3 步：传到路由器

把 `mini-cloud-arm64-upx` 和 `config.json` 放到路由器的 `/data/mini-cloud/` 目录（目录不存在就先建）。两种传法：

- **图形界面（WinSCP）**：用 WinSCP 拖过去即可；
- **命令行**：在电脑的终端里执行：

```bash
scp mini-cloud-arm64-upx root@<router-ip>:/data/mini-cloud/
scp config.json root@<router-ip>:/data/mini-cloud/config/config.json
```

> **用加密功能的话**，启动前还要把钥匙传上去：先在 Windows 上生成钥匙（命令见[「上传加密文件」](#上传加密文件可选)里的 `keygen`，只做一次），再用 WinSCP 把 `vault.key` 拖到路由器的 `/data/mini-cloud/config/`（与 `key_file` 路径一致）。**钥匙没就位之前先别启动**（会直接报错退出）；不用加密就把 `config.json` 里的 `vault` 段删掉。

### 第 4 步：启动

SSH 登录路由器（`ssh root@<router-ip>`），执行：

```bash
chmod +x /data/mini-cloud/mini-cloud-arm64-upx
/data/mini-cloud/mini-cloud-arm64-upx -config /data/mini-cloud/config/config.json >> /data/mini-cloud/mini-cloud.log 2>&1 &
```

看到日志里有「Gateway 启动」就成功了：

```bash
tail -n 5 /data/mini-cloud/mini-cloud.log
```

### 第 5 步：验证

- 浏览器打开 `http://<router-ip>:8088/`，输入 `config.json` 里设的用户名密码——能看到你的网盘列表 = 成功；
- 电视/手机播放器：新建 WebDAV 源，地址填 `http://<router-ip>:8088/<网盘name>/`，账号密码同上（各家播放器的填法见[附录 B](#附录-b播放器怎么连)）。

> **重启后自动运行（小抄）**：建一个开机自启脚本（OpenWrt 标准做法），分三步：

第 1 步：用 WinSCP 新建文件 `/etc/init.d/mini-cloud`，内容如下：

```sh
#!/bin/sh /etc/rc.common
# mini-cloud 开机自启
START=99

PROG=/data/mini-cloud/mini-cloud-arm64-upx
CONFIG=/data/mini-cloud/config/config.json
LOG=/data/mini-cloud/mini-cloud.log
PID_FILE=/tmp/mini-cloud.pid
RUN_FLAG=/tmp/mini-cloud.run

start() {
    # 已经在跑就不重复启动；上次崩溃留下的标记要清掉重新拉起
    if [ -f ${RUN_FLAG} ] && kill -0 $(cat ${PID_FILE}) 2>/dev/null; then
        return 0
    fi
    rm -f ${RUN_FLAG} ${PID_FILE}
    touch ${LOG}
    echo "" >> ${LOG}
    echo "===== boot $(date) =====" >> ${LOG}
    touch ${RUN_FLAG}
    sleep 20    # 等网络就绪（如果启动时网盘没连上，可调大到 60）
    [ -f ${RUN_FLAG} ] || return 0    # 等待期间被 stop 了，就不启动
    ${PROG} -config ${CONFIG} >> ${LOG} 2>&1 &
    echo $! > ${PID_FILE}
}

stop() {
    rm -f ${RUN_FLAG}    # 先摘标记，防止 start 的等待醒来后又拉起进程
    [ -f ${PID_FILE} ] && kill $(cat ${PID_FILE}) >> ${LOG} 2>&1
    rm -f ${PID_FILE}
}
```

> 说明：`RUN_FLAG` 是运行标记——防止重复启动、也防止「stop 之后等待期醒来又自己拉起来」；`PID_FILE` 记着进程号，`stop` 只杀本程序的进程。

第 2 步：SSH 登录路由器，执行：

```bash
chmod +x /etc/init.d/mini-cloud
/etc/init.d/mini-cloud enable
/etc/init.d/mini-cloud start
```

第 3 步：验证

重启路由器，等 1 分钟，浏览器打开 `http://<router-ip>:8088/` 能看到网盘列表 = 自启成功。

---

## 日常使用

### 更新移动云盘的令牌

移动云盘令牌约 30 天过期。过期时页面会提示「令牌已过期，请更新令牌」——打开 `http://<router-ip>:8088/admin`，粘贴新令牌即可，免 SSH、立即生效。

### 上传加密文件（可选）

加密在 Windows 上做（`mcrypt.exe` 与 `vault.key` 放同一目录）：

```bat
:: 生成钥匙（只做一次！已存在会拒绝，--force 才覆盖——覆盖=旧加密文件全部解不开）
mcrypt.exe keygen -key vault.key

:: 加密一个文件夹：明文目录 → 上传目录（生成 上传目录\_mc\）
mcrypt.exe encrypt -key vault.key -in 明文目录 -out 上传目录
```

把 `vault.key` 放到路由器的 `/data/mini-cloud/config/`（与 config.json 里 `key_file` 一致），然后用**网盘自己的 App** 把「上传目录」传到云盘根的 `backup/` 下。**传完等 5 分钟**（期间浏览一次该目录），加密文件自动出现，免重启。

> 加密目录的结构规则：`backup/` 下只放文件夹；每个文件夹是一个独立加密单元（里面才有 `_mc`）。单元整体增删、不做内部修改。

---

## 常见问题（FAQ）

| 现象 | 为什么 | 怎么办 |
|---|---|---|
| 页面提示「令牌已过期，请更新令牌」 | 移动云盘令牌 30 天过期 | 打开 `/admin` 粘贴新令牌 |
| 下载/播放时某个请求失败（503） | 同时解密的流超过 3 路（全局上限，防路由器内存爆掉） | 关掉一路再试；这是保护不是故障 |
| 天翼的 4K 卡顿，移动的不卡 | 天翼 CDN 对单链接限速约 50 Mbps，高码率 4K 必然卡 | 换移动云盘播 4K，或等天翼策略变化 |
| 加密目录是空的 / 文件点开报错 | 多半是文件还没上传完（上传中间态） | 等上传完成，≤5 分钟自动恢复 |
| 上传了加密目录却不显示 | `_mc` 放错层级（应放在 `backup/<目录名>/_mc`） | 云盘里移动 `_mc` 到正确位置；`/admin` 有对应警告 |
| 想清日志 | 用覆盖清空 | `> /data/mini-cloud/mini-cloud.log`（别用 rm 删文件） |
| 忘记路由器地址/账号 | 看 config.json | `listen` 是端口，`username/password` 是登录账号 |

---

## 已知限制

- 只读：不支持上传/删除/改名（写操作一律通过网盘自己的 App）；
- 百度网盘挂载不启用加密（只读直连）；非会员高码率可能卡顿（百度 CDN 策略）；
- 加密播放仅支持天翼 / 移动；
- 天翼家庭 CDN 单链接限速 ~50 Mbps（4K 高码率必然卡顿，与网关无关）；
- 进程崩溃后需手工重启：SSH 登录路由器，执行 `/etc/init.d/mini-cloud restart`（脚本会自动清掉崩溃残留的标记）；
- 局域网内 HTTP 明文传输（含 Basic Auth 口令）；需要更安全请自行前置 HTTPS 反向代理；
- 仅支持单账户单用户。

---

## 给开发者

技术文档索引（README 面向使用者，设计与实现细节都在这里）：

| 文档 | 内容 |
|---|---|
| [docs/design.md](docs/design.md) | 功能设计（含 Vault 加密体系、Phase 2 容器方案） |
| [docs/vault-feasibility.md](docs/vault-feasibility.md) | 可行性实测（AX6 性能、CDN Range、URL 时效数据） |
| [docs/process.md](docs/process.md) | 开发与审核流程（角色、铁律、六道闸门） |
| [docs/review/](docs/review/) | 历次审核报告与验收报告归档 |
| [docs/testing-manual-v1.2.0.md](docs/testing-manual-v1.2.0.md) | 真机验收测试手册 |

---

## 附录 A：各云盘凭证怎么拿

| 网盘 | 要什么 | 怎么拿 |
|---|---|---|
| 天翼个人 / 家庭 | 手机号 + 密码 | 就是云盘 App 的登录账号密码 |
| 移动云盘 | authorization 令牌 | 电脑浏览器登录移动云盘网页 → F12 开发者工具 → 网络请求头里找 `authorization` 字段，复制整串 Base64 |
| 百度网盘 | refresh_token 等 | OAuth 获取（详见配置模板注释；百度不支持加密播放） |

## 附录 B：播放器怎么连

| 播放器 | 添加方式 |
|---|---|
| Nova Player（电视） | 设置 → 网络 → WebDAV → 地址 `http://<router-ip>:8088/<网盘name>/` → 输入账号密码 |
| Infuse / 网易爆米花 / Kodi | 同上：新建 WebDAV/SMB 源时选 WebDAV，填地址与账号密码 |
| 电脑浏览器 | 直接打开 `http://<router-ip>:8088/`，点目录浏览、点文件下载 |

## 附录 C：命令行参数（完整清单）

两个程序都有 `-h` 参数，随时可以看帮助。

### mini-cloud（路由器上的主程序）

| 参数 | 默认 | 说明 |
|---|---|---|
| `-config 路径` | `config.json` | 配置文件路径 |
| `-debug` | 关 | 调试日志：额外记录每个请求一行（谁访问、什么文件、成没成、传了多少字节）+ 每分钟一行内存占用 |
| `-quiet` | 关 | 静默模式：只记警告和错误 |

**日志档位**（同时给 `-debug` 和 `-quiet` 时，`-debug` 优先）：

| 启动方式 | 记什么 |
|---|---|
| 不带参数 | 关键节点：启动、挂载、停止、出错 |
| `-debug` | 最详细：上面全部 + 每个请求 + 内存观测 |
| `-quiet` | 最安静：只有警告和错误 |

所有日志都打到屏幕上（一行一条：时间、级别、消息、细节），启动命令里的 `>> mini-cloud.log` 就是把它们存进文件；网页 `/admin` 上的警告提示是另一条路，与日志开关无关。

```bash
# 默认（只记关键节点）
/data/mini-cloud/mini-cloud-arm64-upx -config /data/mini-cloud/config/config.json

# 排查卡顿/内存问题
/data/mini-cloud/mini-cloud-arm64-upx -config /data/mini-cloud/config/config.json -debug

# 长期运行（日志文件长得最慢）
/data/mini-cloud/mini-cloud-arm64-upx -config /data/mini-cloud/config/config.json -quiet

# 盯着日志看实时输出
tail -f /data/mini-cloud/mini-cloud.log
```

### mcrypt.exe（Windows 加密工具）

子命令制：`mcrypt.exe <子命令> <参数>`。加密解密都用同一把钥匙（`-key` 指向同一个 `vault.key`）。

| 子命令 | 参数 | 说明 |
|---|---|---|
| `keygen` | `-key 路径`（默认 `vault.key`）、`-force` | 生成钥匙（**只做一次**；已存在会拒绝，`-force` 覆盖 = 旧加密文件全部解不开） |
| `encrypt` | `-key 路径`、`-in 明文目录`、`-out 上传目录` | 加密文件夹（生成 `上传目录\_mc\`）；`-out` 不能放在 `-in` 里面 |
| `decrypt` | `-key 路径`、`-in 加密目录`、`-out 明文目录` | 把加密目录（含 `_mc` 的目录）解回明文 |
| `verify` | `-key 路径`、`-in 加密目录` | 校验加密目录是否完整、能否解开（不上传也能自检） |
| `bench` | `-size MB`（默认 64） | 测本机加密速度（看看有没有硬件加速） |

---

## 变更记录

### v1.2.0-beta (2026-11-21)

- **新增** 容器自动发现（加密目录即传即用）：`vault.paths` 为容器根（只放目录），子目录各自持有 `_mc` 为独立加密单元——新增/删除/整块换新 ≤5 分钟自动生效，免配置免重启；容器下非单元路径一律「未找到」（404，不认识即不管）；上传中间态自愈（不崩溃、不泄露、传完自动完整）
- **新增** 管理页体验：全部挂载状态卡（驱动告警 + Vault 告警双通道）；移动凭证标签动态显示剩余有效期/获取指引；过期文案「令牌已过期，请更新令牌」
- **新增** 移动驱动启动健壮性：云主机发现失败不阻断启动（受限模式 + 管理页提示，凭证更新后自动恢复）
- **加固**（外部审核四轮 20+ 条全闭环）：keygen 默认拒绝覆盖（`--force` 原子替换）；`vault.paths` 启动严格校验 + `max_streams` 天花板 4；清单路径规范校验（防死循环）；加密实际字节数校验；清单写入尺寸校验 + 原子写；主密钥入 .gitignore；`header_sha256` fail-closed；decrypt 路径/符号链接/交接点防护；配置语义错误不触发自愈回滚；**并发额度进程级共享**（解密流默认 3 / 天花板 4 + 清单解析并发 2 + 64 MiB 软上限前移）
- **测试** 外部审核 4 轮 + 常驻复核多轮 + 端到端 9 场景全链路 + 真机验收全通过（跨挂载并发 C1–C4、自动发现、中间态、管理页、稳定性；AX6 硬件加速 A/B 实测 50×）

### v1.1.0-beta (2026-11-19)

- **新增** Vault 加密文件 WebDAV 还原播放（Phase 0+1）：MCF1 格式（AES-256-GCM 1 MiB 分块 + HKDF 密钥派生 + manifest 清单）；`cmd/mcrypt` 加密工具（Windows 版随版本交付）；虚拟目录树按原始文件名展示、`_mc` 密文对象 403 屏蔽；随机 Seek 块级按需解密（视频拖动只读 1–2 块）
- **新增** 云盘上游取数层：签名 URL 自管（到期前 60s 主动换签 + 403 被动换签，**真机实测播放中途换签零中断**）；4 MiB 顺序读窗口缓冲；传输截断检测；客户端取消不触发重试（不白刷云盘 API）
- **支持范围（PM 拍板）**：加密播放仅支持天翼 / 移动；百度挂载不启用 Vault（密文目录按普通目录可见）；全局并发解密流默认 3 路（可调）
- **修复** 真机压测崩溃：内存预算缺陷（每流瞬时双份 12 MiB → 复用缓冲后 6 MiB）+ 64 MiB 内存软上限兜底，4 并发流下路由器不再被拖垮
- **已知限制**：天翼家庭 CDN 单链接限速 ~50 Mbps，4K 高码率在天翼站必然卡顿（移动站 3×4K 共 225 Mbps 实测流畅）
- **测试** 真机验收全通过（天翼/移动双站、3×4K 并发、3×720P 15 分钟跨签名刷新、内存峰值与回落观测）；外部审核 12+ 轮全闭环；`go test -race` 全绿

### v0.10.0 (2026-11-17)

- **新增** 凭证网页管理：浏览器访问 `/admin` 直接更新移动云盘 authorization，免 SSH。两阶段更新（联网验证 10s 超时 → 原子写配置 → 生效），任何一步失败零变化；配置文件原子写入（临时文件+落盘+改名）、写前自动备份（.bak）、写后权限 0600；剩余有效期不足 24 小时的凭证拒绝接收
- **修复** 3 处现存转义缺陷：PROPFIND href 逐段百分号编码（中文/空格/`#`/`?` 文件名在严格客户端 404）、目录页文件名 HTML 转义（XSS）、根 PROPFIND 挂载名 XML 转义
- **增强** 更新成功后立即清空该挂载的目录/链接缓存；401 自动刷新与网页更新互不覆盖（代次防护）；配置文件缺失时写回路径严格报错（不重建默认配置，防认证降级）；HTTP 服务增加读请求头超时
- **新增** 部署脚本 `scripts/`：开发机一键编译+UPX+暂存（deploy-prepare.sh），路由器一键拉取+备份+重启+健康检查+失败自动回滚（deploy-router.sh，busybox ash 兼容）
- **测试** 新增 40+ 用例（凭证两阶段更新、并发串行化、401×提交竞态、配置原子写/权限/备份/还原、转义），全部通过竞态检测；外部代码审核五轮通过

### v0.9.2 (2026-10-05)

- **变更** 移动云盘 authorization 过期后不再返回 500，改为显示空目录 + 提示名：
  - 新增 `HealthReporter` 可选接口，驱动可向上层报告健康提示
  - 移动驱动 `List` 在 authorization 过期时返回空目录、`Link` 返回明确错误，不再发起网络请求
  - 根导航页与根 PROPFIND 的显示名在过期时变为「移动云盘authorization已经过期，请更新」
  - 明确移动云盘 RCS token 约 30 天硬过期、服务端不接受过期后续期，需用户手动更新 `authorization`

---

## License

MIT
