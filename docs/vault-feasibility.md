# 个人文件夹功能可行性报告（Vault 加密方案）

> mini-cloud 加密视频/文件 WebDAV 还原方案  
> 项目地址：https://github.com/lamaged/mini-cloud（分支 feature/file-encryption）  
> 文档版本：v1.3  
> 日期：2026-11-16（v1.0 发布于 2026-11-14）  
> 状态：可行性验证完成；已按外部文档评审 RV-2026-11-16-01/02、RV-2026-11-17-02/03 修订  
> 说明：本报告由原《个人文件夹功能-测试总结》升级而来，整合了需求、AX6 加密性能、CDN Range 实测、URL 时效分析和代码核实结论。

## 版本与审阅记录

| 版本 | 日期 | 变更 |
|---|---|---|
| v1.0 | 2026-11-14 | 首版（AX6 性能、CDN Range、URL 时效实测与代码核实） |
| v1.1 | 2026-11-16 | 按评审 RV-2026-11-16-01 修订：URL 缓存策略改为 Vault 自管（D1）；响应头唯一来源（D3）；包装示例修正（D4）；格式不变量与对象绑定字段（I1–I3）；`_mc` 屏蔽（I4）；上游异常分支与并发语义（I6）；Stop 幂等（I7）；改名并入 docs/ 并统一「文件+符号」引用（P6） |
| v1.2 | 2026-11-16 | 按评审 RV-2026-11-16-02 修订：链接随改名修正（I8）；`vault.paths` 格式与匹配规则定稿（I9）；上游 200 分级策略（I10）；Vault 路径分流与重试语义（I11）；fileID 一致性校验提前 Phase 0（I12）；结论表口径（I13）；头部字段语义（I14）；getObj 修改项恢复（P11） |
| v1.3 | 2026-11-17 | 按评审 RV-2026-11-17-02/03 修订：元数据 `h` 字段废弃、权威哈希唯一真值源 = manifest（I1）；CipherSize 公式说明（I2）；顺序读承诺更新为 Phase 1 顺序流式 RangeSource（I3，PM 拍板方案 b）；meta AAD 占位头原因（P6） |

| 审阅 | 结论 | 意见处置 |
|---|---|---|
| RV-2026-11-16-01（外部文档评审） | 有条件通过 | 见 PROGRESS.md「审核意见处置记录」 |
| RV-2026-11-16-02（复核评审） | 有条件通过（第二轮） | 见 PROGRESS.md「审核意见处置记录」 |
| RV-2026-11-17-02（Vault Phase 0 核心审核） | 有条件通过（I1/I2 修复） | 见 PROGRESS.md「审核意见处置记录」 |
| RV-2026-11-17-03（复核） | 通过 | 见 PROGRESS.md「审核意见处置记录」 |

---

## 0. 执行摘要

**结论：功能可行，可以进入开发。**

本方案的核心不是“把文件加密后上传”这么简单，而是要让标准 WebDAV 客户端在不解密、不装插件的前提下，直接播放云盘上的加密视频。可行性取决于四个条件：

| 关键条件 | 验证结果 | 结论 |
|---|---|---|
| AX6 的 AES-256-GCM 性能 | 约 136 MB/s ≈ 1.09 Gbps | 充足 |
| 云盘 CDN 是否支持 HTTP Range | 天翼个人/家庭、移动均返回 206 | 通过 |
| 是否支持 64 位文件偏移 | 55 GB 文件在 50 GB 偏移处正常 | 通过 |
| URL 过期是否可管理 | 15 分钟–4.9 天不等，可 403 刷新 | 可控 |

真正的工作量集中在三处：

1. **分块加密格式**：必须支持随机访问，不能把整个视频当一条密文流。
2. **Vault 代理层**：加密文件不能再 302，必须由路由器代理解密并转发 Range。
3. **虚拟目录与 manifest**：WebDAV 要看到原始文件名、大小、目录结构，而不是随机对象名。

未加密路径的 302 重定向完全保留，只对配置的 `/backup`、`/Archive` 等根路径启用 Vault。

---

## 1. 需求与范围

### 1.1 原始需求

1. 用户先用自行开发的软件把原视频/文件/文件夹加密；加密后的文件名、扩展名都不是原有的；即使改名、改扩展名也无法打开。
2. 用官方工具把加密后的视频/文件夹上传到网盘。
3. 客户端通过 mini-cloud 访问时，路由器提供的 WebDAV 服务能复原加密视频，供客户端直接调用。
4. 未加密文件原有的 302 重定向继续保留。
5. 加密文件和文件夹放在网盘根目录下的 `/backup` 或 `/Archive`（可配置；配置值为**无前导斜杠的目录名**，如 `"backup"`，匹配规则见 §7.8）。
6. 以红米 AX6 为目标设备，已测 AES-256-GCM 性能。

### 1.2 本期范围

- 只读 WebDAV：不做 WebDAV 上传、删除、改名。
- 服务端解密：客户端仍是标准 WebDAV 播放器，不安装插件。
- 单 master key、单用户体系：沿用 mini-cloud 现有 Basic Auth。
- 逐文件加密：文件夹表现为虚拟目录树，视频可单独播放。
- 支持天翼个人、天翼家庭、移动云盘；百度沿用现有代理模式。

### 1.3 非目标

- 多用户/多租户权限。
- 客户端侧解密（需要自定义播放器）。
- 云端在线转码、缩略图、字幕转码。
- 写回云盘、增量同步、版本管理。
- 防止路由器被入侵后的明文泄露（服务端解密无法做到）。

---

## 2. 前置信息：AX6 加密性能

### 2.1 CPU 特性

```text
root@XiaoQiang:~# grep -m 1 '^Features' /proc/cpuinfo
Features : fp asimd evtstrm aes pmull sha1 sha2 crc32
```

- `aes`：ARMv8 硬件 AES 指令。
- `pmull`：多项式乘法指令，GCM 的 GHASH 可硬件加速。
- 结论：AES-256-GCM 可以走硬件加速，不是纯软件查表。

### 2.2 OpenSSL 实测

```text
openssl speed -elapsed -evp aes-256-gcm
OpenSSL 1.0.2q

type             16 bytes     64 bytes    256 bytes   1024 bytes   8192 bytes
aes-256-gcm      60940.90k   104626.73k   126247.85k   133902.68k   136306.69k
```

换算：

| 块大小 | 吞吐 | 说明 |
|---:|---:|---|
| 16 B | 60.9 MB/s | 每次 GCM 调用开销占比高 |
| 64 B | 104.6 MB/s | 小包场景 |
| 256 B | 126.2 MB/s | 接近上限 |
| 1 KiB | 133.9 MB/s | 接近上限 |
| 8 KiB | **136.3 MB/s ≈ 1.09 Gbps** | 大块稳定吞吐 |

### 2.3 对视频播放的意义

- 100 Mbps 视频 = 12.5 MB/s，解密约占单核 **9%**。
- 加上云盘侧 TLS 解密，按 2 倍估算约 **18–25% 单核**。
- AX6 是四核 A53，单路 4K（40–100 Mbps）CPU 余量足够。
- 千兆级持续解密单核不够，但家庭场景不需要。
- 1 MiB 分块时，每次 GCM 调用处理 1 MiB，调用开销可忽略。

### 2.4 Go 实现的注意事项

OpenSSL 的数字不能直接等同于 Go `crypto/aes` + `crypto/cipher` 的吞吐。Go 在 ARM64 上也会使用硬件 AES/PMULL，但必须在 AX6 上跑一个 1 MiB 块的 `Seal/Open` benchmark 确认。

预期量级：100–300 MB/s。即使只有 100 MB/s，也足够 4K；如果低于 40 MB/s，才需要重新评估并发策略。

---

## 3. 测试环境与方法

### 3.1 环境

| 项目 | 值 |
|---|---|
| 服务端 | Redmi AX6 / OpenWrt / mini-cloud，监听 `:8088` |
| 测试客户端 | Ubuntu 24.04 + curl |
| WebDAV 认证 | mini-cloud Basic Auth（用户名 `admin`，密码以路由器配置为准） |
| 测试驱动 | 天翼个人、天翼家庭、移动云盘；百度为既有代理，本轮未复测 |
| 测试时间 | 2026-11-14 |

### 3.2 方法

1. 对 mini-cloud 的 WebDAV 文件 URL 发 **GET**，确认 `302`。
2. 用 `curl -w '%{redirect_url}'` 取 `Location`（CDN 地址）。
3. 对 CDN 地址发 `Range: bytes=1000000-1001023`，检查 `206`、`Content-Length`、`Content-Range`。
4. 用 `Go-http-client/1.1` 重复 10 次，验证 URL 可复用、UA 不受限。
5. 请求 `bytes=0-33554431`（32 MiB），测持续吞吐。
6. 请求 `bytes=0-0`，从 `Content-Range` 读取文件总大小。
7. 对 55 GB 文件额外请求 `bytes=50000000000-50000001023`，验证 64 位偏移。
8. 解析 `Location` 中的 `Expires` / `X-Amz-Date` + `X-Amz-Expires`，换算绝对过期时间。

> **重要**：不能用 `curl -I` 取 302。mini-cloud 对 URL 类型文件的 HEAD 请求直接返回 `200`，不返回 `Location`；只有 GET 才 `http.Redirect`。

### 3.3 数据口径

- 吞吐测试在 Ubuntu 测试机上完成，测到的是 Ubuntu 的网络路径，不完全等于路由器。
- 但 CDN 侧能力、Range 行为、URL 时效与客户端无关，结论可直接用于 AX6。
- 路由器侧 Go 代理吞吐需要 Phase 0 实测。

---

## 4. 测试结果

### 4.1 天翼个人

- 路径：`/189Personal/我的视频/M2TS_ATMOS测试/DTS_OBJECT_EMULATOR.mkv`
- 请求 URL：

```text
http://192.168.2.1:8088/189Personal/%E6%88%91%E7%9A%84%E8%A7%86%E9%A2%91/M2TS_ATMOS%E6%B5%8B%E8%AF%95/DTS_OBJECT_EMULATOR.mkv
```

| 项目 | 结果 |
|---|---|
| GET 状态 | `302` |
| Range 状态 | `206 Partial Content` |
| `Content-Length` | `1024` |
| `Content-Range` | `bytes 1000000-1001023/428657011` |
| 文件总大小 | `428,657,011 B` ≈ 408.8 MiB |
| `Accept-Ranges` | `bytes` |
| CDN Server | `XOS` |
| `Content-Type` | `application/x-www-form-urlencoded; charset=utf-8` |
| `ETag` | `"5f70d5602b56be1fae0a203249201a09-9"`（分片 ETag） |
| `Last-Modified` | `Mon, 01 Nov 2021 07:47:36 GMT` |
| URL 签名参数 | `Signature` + `Expires=1794671461` |
| 过期时间 | `2026-11-14 15:51:01 UTC` |
| 剩余时间 | `1680s` ≈ 28 分钟 |

备注：

- 本轮没有单独用 Go UA 重复 10 次；同一签名体系下预计无差异。
- 文件小于 4 GB，不需要 64 位偏移测试。
- 未实际等待 28 分钟验证过期后的 403，结论由 `Expires` 推导。

### 4.2 天翼家庭

- 路径：`/189Family/电影/The.Hobbit.An.Unexpected.Journey.2012.EXTENDED.2160p...mkv`
- 请求 URL：

```text
http://192.168.2.1:8088/189Family/%E7%94%B5%E5%BD%B1/The.Hobbit.An.Unexpected.Journey.2012.EXTENDED.2160p.BluRay.x265.10bit.SDR.DTS-HD.MA.TrueHD.7.1.Atmos-SWTYBLZ/The.Hobbit.An.Unexpected.Journey.2012.EXTENDED.2160p.BluRay.x265.10bit.SDR.DTS-HD.MA.TrueHD.7.1.Atmos-SWTYBLZ.mkv
```

| 项目 | 结果 |
|---|---|
| GET 状态 | `302` |
| Range 状态 | `206 Partial Content` |
| `Content-Length` | `1024` |
| `Content-Range` | `bytes 1000000-1001023/55302966261` |
| 文件总大小 | `55,302,966,261 B` ≈ 51.5 GiB |
| `Accept-Ranges` | `bytes` |
| CDN Server | `XOS` |
| `Content-Type` | `application/x-www-form-urlencoded; charset=utf-8` |
| `ETag` | `"eb16282ad5d991b81f5ab0012945c37c-1055"` |
| `Last-Modified` | `Wed, 13 Jan 2021 09:15:03 GMT` |
| URL 签名参数 | `Signature` + `Expires=1795091763` |
| 过期时间 | `2026-11-19 12:36:03 UTC` |
| 剩余时间 | `421,980s` ≈ 4.9 天 |

补充测试：

| 测试 | 结果 |
|---|---|
| Go UA 重复 10 次 | 10 次全部 `206 1024` |
| `bytes=0-33554431`（32 MiB） | `206`，`33,554,432 B` / `1.064474s` |
| 持续吞吐 | `31,522,077 B/s` ≈ **31.5 MB/s ≈ 252 Mbps** |
| `bytes=50000000000-50000001023` | `206`，`Content-Range: bytes 50000000000-50000001023/55302966261` |
| 64 位偏移 | 正常 |

55 GB 文件的加密开销估算（1 MiB 分块）：

```text
55,302,966,261 / 1,048,576 ≈ 52,744 块
52,744 × 16 B GCM tag ≈ 824 KiB
文件头 + 元数据 ≈ 1 KiB
总开销 < 1 MiB，约 0.002%
```

按 3 小时片长估算平均码率约 41 Mbps，CDN 吞吐有约 6 倍余量。

### 4.3 移动云盘

- 路径：`/MobileCloudDrive/视频/20250905_140406.mp4`
- 请求 URL：

```text
http://192.168.2.1:8088/MobileCloudDrive/%E8%A7%86%E9%A2%91/20250905_140406.mp4
```

| 项目 | 结果 |
|---|---|
| GET 状态 | `302` |
| Range 状态 | `206 Partial Content` |
| `Content-Length` | `1024` |
| `Content-Range` | `bytes 1000000-1001023/385229243` |
| 文件总大小 | `385,229,243 B` ≈ 367.4 MiB |
| `Accept-Ranges` | `bytes` |
| CDN Server | `ChinaMobileEOS` |
| `Content-Type` | `video/mp4` |
| `ETag` | `"caccf74f15efc5a3eb54844f1717ed14-74"` |
| `Last-Modified` | `Sun, 12 Jul 2026 11:49:13 GMT` |
| URL 签名参数 | `X-Amz-Date=20261114T151008Z`、`X-Amz-Expires=900` |
| `X-Amz-SignedHeaders` | `host`（不绑定 UA/IP） |
| 过期时间 | 生成后 15 分钟 |

补充测试：

| 测试 | 结果 |
|---|---|
| Go UA 重复 10 次 | 10 次全部 `206 1024` |
| `bytes=0-33554431`（32 MiB） | `206`，`33,554,432 B` / `0.998664s` |
| 持续吞吐 | `33,599,320 B/s` ≈ **33.6 MB/s ≈ 269 Mbps** |
| `X-Amz-SignedHeaders=host` | 只签 Host，不校验 UA/IP，Go 客户端可用 |

### 4.4 百度网盘

百度驱动当前已经是服务端代理下载：

- [`drivers/baidu/driver.go` / `Link`](../drivers/baidu/driver.go) 获取 dlink 后由服务端发起 GET；
- 把客户端 `Range` 头转发给百度 CDN；
- 返回 `206` 给客户端。

本轮没有重新执行百度的 302/Range 测试，因为百度本身不走 302。若现有百度播放、拖动正常，则代理 + Range 路径已经被实际验证。

待办：对百度 WebDAV URL 直接发 `Range`，期望直接返回 `206`（不是 302）。

### 4.5 汇总

| 云盘 | 文件大小 | Range | Go UA 10 次 | 32 MiB 吞吐 | 64 位偏移 | URL 时效 |
|---|---:|---|---|---:|---|---|
| 天翼个人 | 428,657,011 B | 206 | 未单独复测 | 未测 | 不需要 | ≈ 28 分钟 |
| 天翼家庭 | 55,302,966,261 B | 206 | 10/10 | 31.5 MB/s | 通过 | ≈ 4.9 天 |
| 移动云盘 | 385,229,243 B | 206 | 10/10 | 33.6 MB/s | 文件 < 4 GB，未测 | 15 分钟 |
| 百度网盘 | 待测 | 既有代理 | — | — | — | 由 dlink 决定 |

---

## 5. URL 时效分析

这是本方案最容易被误解的一点：**CDN 的签名 URL 确实会过期，但现有 302 播放通常不会因此失败；加密代理模式则必须处理 403 刷新。**

### 5.1 实测 URL 时效

> **2026-11-19 补充（RV-2026-11-19-01 P13）**：天翼家庭站签名 URL 携带 `x-amz-limitrate=51200` 参数，实测单链接速率上限 ~50 Mbps——**天翼站 4K 高码率（>50 Mbps）播放必然卡顿，与网关无关**（真机定性：单独天翼 4K 卡顿时路由器 CPU 仅 8–23% 空等数据；移动站 3×4K 共 225 Mbps 同管线无卡顿）。Phase 2 待调研：能否通过 Link 请求参数提高 limitrate。

| 云盘 | 参数 | 过期时间（UTC） | 测试时剩余 |
|---|---|---|---:|
| 移动云盘 | `X-Amz-Expires=900` | 生成后 15 分钟 | 15 分钟 |
| 天翼个人 | `Expires=1794671461` | 2026-11-14 15:51:01 | ≈ 28 分钟 |
| 天翼家庭 | `Expires=1795091763` | 2026-11-19 12:36:03 | ≈ 4.9 天 |

解析规则：

- 天翼：`Expires` 是 Unix 秒；如果是 13 位毫秒，除以 1000。
- 移动：`X-Amz-Date` + `X-Amz-Expires`，`Expires` 是相对秒数。
- 不要写死 TTL；以 `Location` 查询参数为准。

### 5.2 为什么现有 302 播放没有遇到过期问题

代码核实如下。

**第一，handler 每次请求都重新取 Link。**

[`internal/webdav/webdav.go` / `handleGetHead`](../internal/webdav/webdav.go) 对每个 GET/HEAD 都调用：

```go
link, err := h.Driver.Link(ctx, reqPath)
```

拿到 URL 后立刻 302，不在 handler 里保存：

```go
if link.URL != "" {
    if r.Method == http.MethodHead {
        return http.StatusOK, nil
    }
    http.Redirect(w, r, link.URL, http.StatusFound)
    return 0, nil
}
```

所以播放器下一次请求 WebDAV URL 时，会重新走一遍 `Link`。

**第二，URL 缓存只有 10 分钟。**

[`internal/fs/cache.go` / `linkTTL` 常量](../internal/fs/cache.go)：

```go
linkTTL: 10 * time.Minute,
```

[`internal/fs/cache.go` / `CachedReader.Link`](../internal/fs/cache.go) 只缓存 URL 类型，10 分钟后缓存过期，下次请求重新调用驱动。

这正好比移动的 15 分钟、天翼个人的 28 分钟短：

- 移动：缓存命中时 URL 最多旧 10 分钟，至少还剩 5 分钟；
- 天翼个人：至少还剩 18 分钟；
- 天翼家庭：4.9 天，更不是问题。

**第三，驱动每次都向云盘要新地址。**

天翼 [`drivers/tianyi/driver.go` / `getDownloadLink`](../drivers/tianyi/driver.go)：

```go
d.client.apiRequestEx("GET", fullUrl, params, &result, d.isFamily())
...
return &model.Link{URL: redirectURL, ...}, nil
```

移动 [`drivers/mobile/driver.go` / `Link`](../drivers/mobile/driver.go)：

```go
d.client.personalRequest("/file/getDownloadUrl", ...)
...
return &model.Link{URL: downloadURL}, nil
```

缓存未命中时，驱动重新调用云盘 API，签名和 `Expires` 都会更新。

**第四，已经建立的连接可以超过 Expires。**

签名只在请求开始时校验。顺序播放的单个 HTTP 连接可以继续传完；这解释了为什么一部长电影即使播放超过 28 分钟，只要不拖动、不断线，也能正常播完。

**第五，302 默认没有显式缓存头。**

Go 的 `http.Redirect` 写 302 时没有设置 `Cache-Control` / `Expires`。客户端通常不会永久缓存 302；很多播放器在拖动时会重新请求原始 WebDAV URL，于是拿到新的 302。

**结论：现有 302 路径没有“URL 过期”bug。** 它不长期持有 URL，且 URL 缓存 10 分钟小于签名有效期。实际使用中没有遇到问题，是符合代码行为的。

但也要明确：**302 路径没有 403 刷新能力。** [`internal/fs/retry.go` / `RetryReader.Link`](../internal/fs/retry.go) 只重试 `Link` 返回的 error；天翼/移动的 `Link` 只是拿 URL，并没有向 CDN 发请求，所以 CDN 的 403 它看不到。如果播放器自己缓存了 CDN URL 并在过期后继续用，mini-cloud 不在链路里，也无法刷新。

### 5.3 加密代理模式的不同

加密文件不能再 302。Vault 必须自己向 CDN 发 Range，因此 mini-cloud 会**长期持有上游 URL**：

```text
播放器 → mini-cloud（Vault）→ CDN
```

这时：

- 每次拖动/重连，Vault 都要发新的 Range；
- 如果缓存的 URL 过期，CDN 返回 403；
- Vault 必须捕获 403，清掉旧 URL，重新 `Link`，用新 URL 重试一次；
- 否则播放器会看到 502/截断。

所以 URL 时效不是现有 302 的问题，而是 **Vault 代理层必须实现的逻辑**。

### 5.4 Vault 的 URL 管理策略

1. `Link` 返回的不再是 URL，而是解密 Reader；Vault 内部通过原始驱动的 `Link` 拿上游 URL。
2. 解析 `Expires` / `X-Amz-Date` + `X-Amz-Expires`，计算绝对过期时间。
3. URL 缓存由 Vault 自建「URL + 绝对过期时间」表实现，TTL = `min(10min, expires - now - 60s)`。现有 `CachedReader` 的链接缓存只支持固定 10 分钟 TTL、无按条目指定 TTL 的扩展点，**Vault 路径不走它的链接缓存**——Vault 直接调用原始驱动的 `Link`（包装顺序见 §8.2）。
4. 403 时：Vault 内部失效旧 URL → 重新 `Link` → 重试一次；第二次仍失败返回 502。
5. 同一文件的并发 Range 共用一个刷新锁（标准库 mutex 自实现），避免同时打爆云盘 API。
6. 单条已打开的流不主动中断；等它自然结束；新的 Range 再走刷新逻辑。

---

## 6. 可行性评估

### 6.1 技术可行性

| 维度 | 结论 | 依据 / 风险 |
|---|---|---|
| 加密性能 | 可行 | AX6 AES-GCM 约 136 MB/s；Go 需实测 |
| 随机访问 | 可行 | 1 MiB 分块 GCM；CDN 支持 Range |
| 64 位大文件 | 可行 | 55 GB 文件 50 GB 偏移测试通过 |
| 代理吞吐 | 预计可行 | CDN 31.5–33.6 MB/s；4K 只需 5–12 MB/s |
| 内存 | 可行 | 1 MiB 分块，每流约 3–5 MB，4 流约 20 MB |
| 存储开销 | 可行 | GCM tag + 头/元数据 < 0.002% |
| WebDAV 协议 | 可行 | `http.ServeContent` 已支持 Range/206/HEAD |
| 虚拟目录 | 可行 | 加密 manifest + 内存树 |
| 云端上传 | 可行 | 官方工具上传 `_mc` 目录；manifest 最后上传 |
| 客户端兼容 | 待验证 | 标准 WebDAV；需 Infuse/Nova/爆米花实测 |
| URL 时效 | 可控 | 403 刷新；移动 15 分钟最紧 |
| 安全 | 可行 | AES-GCM + 每文件密钥；密钥管理是关键 |

### 6.2 性能可行性

**CPU：**

- 136 MB/s 解密；100 Mbps 视频占单核约 9%；
- 加 TLS 约 18–25%；
- AX6 四核，单路 4K 足够；
- 多路 4K 需要限制并发（建议 4 路）。

**CDN：**

- 天翼家庭 31.5 MB/s，移动 33.6 MB/s；
- 4K remux 平均约 41 Mbps = 5.1 MB/s，峰值约 100 Mbps = 12.5 MB/s；
- CDN 侧有 2.5–6 倍余量。

**路由器：**

- 代理会成为数据通道，但目标码率不高；
- 需要 Phase 0 在 AX6 上实测 Go 代理吞吐；
- 如果只有 8 MB/s，仍可支撑 41 Mbps 平均码率，但 100 Mbps 峰值可能吃紧。

### 6.3 安全可行性

| 威胁 | 结果 |
|---|---|
| 云盘账号泄露 | 攻击者只拿到密文、随机文件名、文件大小；无法解密 |
| 云盘篡改数据 | GCM 每块认证失败，服务端拒绝返回明文 |
| 云盘截断/重排 | 块序号 nonce + AAD + 总大小校验，失败关闭 |
| 路由器被入侵 | 攻击者可拿到 master key 和明文；服务端解密固有限制 |
| WebDAV 密码泄露 | 可访问明文；建议强密码 + HTTPS |
| 云端回滚旧版本 | 可能看到旧内容；可选用本地 manifest 哈希固定 |
| 密钥丢失 | 数据不可恢复；必须离线备份 master key |

**安全边界：云盘不可信，路由器可信。** 如果路由器本身不可信，只能做客户端解密，那就不能满足“标准 WebDAV 播放器直接播放”的要求。

### 6.4 运维可行性

- 加密软件离线运行，生成 `_mc` 目录；官方工具上传。
- 服务端只读，不需要云盘写权限。
- 新增一个 `vault` 配置段和一个 key 文件；不影响现有云盘配置。
- manifest 仅在服务启动时加载，更新后需重启服务生效（Phase 0 现状）；定时重载列入 Phase 2。
- 日志不打印 key、nonce、明文内容。

### 6.5 总体结论

**可行，难度中等偏上。**

- 加密本身简单；
- CDN Range 和 64 位偏移已经被实测证明；
- 主要工程量在 Vault 代理层、分块 Reader、manifest 虚拟目录和 URL 刷新；
- 不是“能不能做”的问题，而是“如何把随机访问、URL 生命周期和播放器兼容性做稳”的问题。

---

## 7. 推荐架构

```text
播放器 / WebDAV 客户端
          │  GET + Range
          ▼
┌─────────────────────────────┐
│ webdav.Handler              │  PROPFIND / GET / HEAD
│  http.ServeContent          │  自动处理 206 / Content-Range
└──────────────┬──────────────┘
               │
        ┌──────▼──────┐
        │ Vault 包装层 │  只拦截 /backup、/Archive
        │ 虚拟目录树   │  其余路径原样委托 → 302
        │ 解密 Reader  │
        └──────┬──────┘
               │
        ┌──────▼──────┐
        │ Retry/Cache │  目录缓存 30s、URL 缓存 10min
        └──────┬──────┘
               │
        ┌──────▼──────┐
        │ Driver      │  tianyi / mobile / baidu / local
        └──────┬──────┘
               │ Range 请求
               ▼
            云盘 CDN
```

### 7.1 组件

| 组件 | 职责 |
|---|---|
| `cmd/mcrypt` | 加密软件：keygen、encrypt、verify、decrypt、repair |
| `internal/vault/format.go` | 文件头、分块格式、manifest 格式 |
| `internal/vault/crypto.go` | HKDF、AES-256-GCM 分块加解密 |
| `internal/vault/manifest.go` | manifest 解密、虚拟目录树 |
| `internal/vault/reader.go` | 解密 Reader（`io.ReadSeeker` + `Stat`） |
| `internal/vault/upstream.go` | 云盘 Range 请求、URL 刷新、403 重试 |
| `internal/conf/config.go` | `vault` 配置 |
| `main.go` | 包装驱动、初始化 Vault |
| `internal/webdav/*` | HEAD 长度、href 转义等兼容性修复 |

### 7.2 请求流程

**普通路径（未加密）：**

```text
GET /天翼个人/电影/a.mkv
  → Vault 判断不在 /backup、/Archive
  → 委托 CachedReader.Link
  → 返回 302
  → 播放器直连 CDN
```

行为与现在完全一致。

**Vault 路径（加密）：**

```text
GET /天翼个人/backup/电影/a.mkv
  → Vault 查虚拟目录树
  → Link 返回 decryptReader
  → http.ServeContent 解析客户端 Range
  → decryptReader.Seek 打开上游 Range
  → 解密、返回明文 206
```

**`_mc` 屏蔽（Phase 0 验收项）**：Vault 根为 100% 虚拟视图——`PROPFIND` 不出现 `_mc` 及其子项；`GET /天翼个人/backup/_mc/...` 及任何未出现在 manifest 中的物理路径一律返回 **403**，绝不回落 302 把密文对象直接交给客户端（否则对象名与大小可被枚举）。

### 7.3 加密格式

#### 文件头（64 B）

| 偏移 | 长度 | 字段 |
|---:|---:|---|
| 0 | 4 | magic `MCF1` |
| 4 | 1 | version |
| 5 | 1 | flags |
| 6 | 4 | chunk_size（默认 1 MiB） |
| 10 | 8 | plain_size |
| 18 | 16 | file_id |
| 34 | 4 | nonce_prefix |
| 38 | 4 | meta_len |
| 42 | 8 | data_offset |
| 50 | 8 | key_id |
| 58 | 6 | reserved |

**头部字段语义（v1.2 定稿）**：

- `key_id`：Phase 0 恒为全零；Phase 2 密钥轮换启用时 = `SHA-256(masterKey)` 前 8 字节。读取时忽略并保留（为轮换预留兼容）。
- `flags`：Phase 0 必须写零；读取时**非零即拒绝**（防未定义行为）。
- `reserved`：Phase 0 必须写零；读取时忽略但保留（向前兼容）。

#### 分块

- 明文按 1 MiB 分块，最后一块可短。
- 每块独立 AES-256-GCM，16 B tag。
- 文件密钥：`HKDF-SHA256(masterKey, salt=fileID, info="mc-data-v1")`。
- Nonce：4 B 随机前缀 + 8 B 块序号。
- AAD：`SHA256(header) + uint64(index)`。
- `cipher_size = data_offset + plain_size + chunk_count * 16`。
- 所有 offset/size 用 `int64`。

#### 元数据

文件头后是加密 JSON：

```json
{
  "v": 1,
  "p": "Movies/Interstellar (2014)/Interstellar.mkv",
  "n": "Interstellar.mkv",
  "s": 123456789,
  "m": 1712345678,
  "t": "video/x-matroska",
  "h": ""
}
```

> 注：`h` 字段已废弃（v1.3，I1）——单遍流式加密下无法自包含明文哈希，恒为空；权威 SHA-256 只存 manifest 条目。示例保留字段仅为格式兼容。

保存原始路径、原始文件名、大小、mtime、MIME。

> v1.3 修订（RV-2026-11-17-02）：
> - `h`（明文 SHA-256）字段**废弃**：单遍流式加密下元数据先于数据落盘，无法自包含哈希；
>   权威哈希只存 manifest 条目的 `sha256`。字段保留仅为格式兼容，读取方不得依赖（I1）。
> - 元数据加密的 AAD = SHA256(「MetaLen/DataOffset 置零的占位头」)：因为 MetaLen 依赖元数据密文
>   长度，无法在加密元数据前得到完整头；分块 AAD 绑定最终完整头（含 MetaLen/DataOffset），
>   空文件由 header_sha256 兜底——三者合起来覆盖整个头，勿改成「绑定完整头」破坏加密顺序（P6）。

#### 格式不变量（v1.1 评审后定稿）

1. **fileID 不变量**：每次加密必须新生成 `fileID`；`repair` / 重加密 / 续写工具**不得复用** `fileID`；工具层对「同一 fileID 重复加密」直接拒绝。同一 fileID 下重复加密会造成 GCM (key, nonce) 重用，安全性崩塌。
2. **空文件与头部认证**：manifest 条目增加 `header_sha256`（对象文件头的 SHA-256），服务端读取时校验。空文件（`plain_size=0`，`chunk_count=0`）没有任何分块 tag 覆盖头部，靠该哈希防止头部被篡改。
3. **头部字段校验**（服务端读取时强制执行，违反即拒绝、绝不夹取）：`chunk_size ∈ [64 KiB, 1 MiB]`；`data_offset == 64 + meta_len`；`meta_len ≤ 4 KiB`；`plain_size ≥ 0`。
4. **对象绑定（防跨对象替换，分两档）**：
   - **fileID 一致性校验**（manifest 条目的 `fileID` == 对象头部 `fileID`）：成本≈0（头部本来就要读），**Phase 0 必做**（RV-02 I12，PM 同意提前）；
   - **`metadata.p` 路径比对**与失败处置策略：**Phase 1 实施**（PM 拍板：防偷换/回滚的强校验放 Phase 1）。

#### 对象命名

```text
/backup/_mc/o/7f/3a/7f3a9c...e1.bin
```

- `fileID` 的 Base32，26 字符；
- 两级分片；
- 扩展名统一 `.bin`，不是原始扩展名；
- 云端看不到原文件名/扩展名/目录名。

#### manifest

```text
/backup/_mc/manifest.mcf
```

`gzip(JSON)` 后 AES-256-GCM 加密，记录虚拟路径 → 对象路径、大小、mtime、MIME、fileID。服务端启动/定时加载，内存建树；`PROPFIND` 不逐个读文件头。

#### 文件夹

逐文件加密，保留相对路径到元数据/manifest；空目录记录在 `dirs`。不打成单个加密压缩包，否则 WebDAV 只能暴露一个无法直接播放的大文件。

### 7.4 Range / Seek

```text
chunkIdx  = plainOffset / chunkSize
cipherOff = dataOffset + chunkIdx * (chunkSize + 16)
```

- `Seek(0, End)` 直接返回 `plain_size`，不访问云盘；
- `Seek(start)` 关闭旧流，从块边界开新 Range；
- 顺序读时保持一个上游连接，不按块发请求——**Phase 1 由 upstream.go 的顺序流式 RangeSource 实现**（下发大段 Range、内部缓冲、按块切出；随机读走单块）。Phase 0 的 DecryptReader 对本地文件按块 ReadAt（无网络开销）。PM 拍板 2026-11-17 方案 (b)（RV-2026-11-17-02 I3）。
- 最多浪费一个块的前缀（1 MiB）。

#### 上游异常分支（→ 动作 → 对外状态码）

| 上游行为 | 动作 | 对外状态码 |
|---|---|---|
| Range 请求返回 200（CDN 忽略 Range） | 关闭流，**用新签名 URL 重发一次**（换节点/换签名常能恢复 206）；仍 200：偏移 ≤ 2 MiB → 丢弃到偏移后继续；偏移 > 2 MiB → 502 + 日志「上游不支持 Range」 | 206 或 502 |
| 返回 416 | 透传 | 416 |
| 206 的 Content-Range 与请求区间不符 | **先失效 URL**（与 403 刷新共用同一把刷新锁），关闭流并重试一次；仍不符 | 502 |
| 客户端断开 | ctx 传导，取消上游请求 | —（无响应） |
| Seek 切换流 | 旧 `resp.Body` 必须 Close | — |
| 上游 URL 过期（403） | 失效该 URL → 重新 Link → 重试一次；再失败 | 502 |

> 不做「整文件顺序转发」降级：会占满路由器带宽且破坏 seek 语义。200 持续出现时以 502 暴露，由日志供 PM 判断该盘是否可用（I10）。

### 7.5 密钥层级

```text
master key (32B)
  ├─ HKDF(fileID, "mc-data-v1") → 每文件数据密钥
  ├─ HKDF(fileID, "mc-meta-v1") → 每文件元数据密钥
  └─ HKDF("mc-manifest-v1")     → manifest 密钥
```

- 文件头只存 `file_id`，不存密钥。
- 每文件独立密钥 + 块序号 nonce，避免 GCM nonce 重用。
- master key 放路由器 `vault.key`（0600）或环境变量；不上传云盘。
- 丢失 master key = 数据不可恢复，必须离线备份。

### 7.6 响应头重写

| 上游头 | 代理处理 |
|---|---|
| `Content-Length` | 明文 Range 长度 |
| `Content-Range` | 明文偏移重新生成 |
| `Content-Type` | 虚拟文件名后缀 / manifest MIME |
| `Content-Disposition` | 虚拟文件名，或去掉 |
| `ETag` | `fileID + size` 生成 |
| `Last-Modified` | manifest 原始 mtime |
| `Accept-Ranges` | `bytes` |
| `Content-Encoding` | 强制 `identity` |

- **ETag 与 MIME 的唯一来源是 Vault 虚拟树**（manifest 元数据）：通过 `model.Obj` 新增可选字段（ETag、ContentType）供 `internal/webdav/prop.go` / `buildPropsXML` 使用，PROPFIND 与 GET 同源；`prop.go` 相应改造列入修改清单（§8.2）。
- Vault **自行构造** `Link.Header`；**禁止透传**上游的 `Content-Length`、`Content-Range`、`Content-Encoding`、`ETag`。

### 7.7 并发与内存

- 每流 ≈ 6 MiB：顺序窗口 4 MiB + 密文块 1 MiB + 明文块 1 MiB（RV-2026-11-18 A/B 缓冲复用后无瞬时双份，实测口径 6.02 MiB/流）。
- 默认 3 路并发 ≈ 18 MiB（PM 拍板 2026-11-18：2–3 路为极限）。
- 加 manifest 与 Go runtime 基线，运行期总内存约 25–35 MB；另设 64 MiB 内存软上限兜底（`debug.SetMemoryLimit`，挂载装配后生效，防内核 OOM——见崩溃缺陷报告 docs/review/2026-11-18-mobile-crash-report.md）。
- `max_streams`（默认 3，PM 拍板 2026-11-18：2–3 路为极限）为**全局信号量**（含获取超时），超出返回 503 防止 OOM；不做按挂载计数。
- URL 并发刷新锁用标准库 mutex 自实现（依赖铁律禁止引入外部库，如 `golang.org/x/sync/singleflight`）。

### 7.8 配置示例

已实现字段：`enabled`、`key_file`、`paths`、`max_streams`（0 = 默认 3）。Phase 2 预留（当前未实现、会被忽略）：`manifest`（实际固定 `_mc/manifest.mcf`）、`chunk_size`（实际固定 1 MiB）、`cache_mb`（P20：JSON 示例保持合法，标注移出代码块）。

**`vault.paths` 格式与匹配规则（v1.2 定稿）**：无前导斜杠的目录名列表（挂载内相对路径的首段）。Handler 已 strip 掉 `/<mount>` 前缀，Vault 收到的是挂载内相对路径，仅比较首段字符串，**大小写敏感**（与原盘目录名一致）。

```json
{
  "vault": {
    "enabled": true,
    "key_file": "/data/mini-cloud/vault.key",
    "paths": ["backup", "Archive"],
    "manifest": "_mc/manifest.mcf",
    "chunk_size": 1048576,
    "max_streams": 3,
    "cache_mb": 16
  }
}
```

---

## 8. 代码改动清单

### 8.1 新增

```text
internal/vault/
  format.go
  crypto.go
  manifest.go
  reader.go
  upstream.go
cmd/mcrypt/
  main.go
  keygen.go
  encrypt.go
  decrypt.go
  verify.go
  repair.go
```

### 8.2 修改

| 文件 | 改动 |
|---|---|
| [`internal/conf/config.go`](../internal/conf/config.go) | 增加 `VaultConfig`，校验 key/paths |
| [`main.go`](../main.go) | 包装驱动；`auth.StartWatcher` 传原始驱动；根 PROPFIND XML 转义（v0.10.0 独立修复） |
| [`internal/webdav/webdav.go`](../internal/webdav/webdav.go) | HEAD Content-Length；Reader Stat 兼容；`serveDirHTML` HTML 转义（v0.10.0 独立修复） |
| [`internal/webdav/prop.go`](../internal/webdav/prop.go) | href 百分号编码（v0.10.0 独立修复）；`getObj` 错误处理（List 失败不得返回伪目录，Vault 降级路径依赖）；ETag/MIME 唯一来源（`model.Obj` 新字段） |
| [`internal/model/obj.go`](../internal/model/obj.go) | `Obj` 增加可选 ETag / ContentType 字段（零值 = 现有行为） |
| [`internal/fs/cache.go`](../internal/fs/cache.go) | `Stop` 改 `sync.Once` 幂等（I7） |
| [`drivers/local/driver.go`](../drivers/local/driver.go) | 可选 `OpenRange` |
| [`drivers/baidu/driver.go`](../drivers/baidu/driver.go) | 可选 `OpenRange` + dlink 缓存 |

包装顺序（与 design.md §4.1 逐字一致，唯一出处）：

```go
raw := createDriver(cc)                      // 驱动以 cc.MountName() 命名（挂载名，非类型名）
if err := raw.Init(ctx); err != nil { ... }  // error 必须处理
auth.StartWatcher(ctx, raw, 5*time.Minute)   // 必须传 raw：TokenManager 断言在 raw 上

cached := fs.NewCachedReader(fs.NewRetryReader(raw))
var drv driver.Driver = cached
if vaultCfg != nil {
    v := vault.New(cached, raw, vaultKey, vaultCfg) // 实际签名以代码为准（P13）；/M/<vault> 前缀：List 读内存虚拟树、Link 走 raw（自管 URL 表）；其余路径：List/Link 全部委托 cached
    if err := v.Init(ctx); err != nil { ... }
    v.Start(ctx)
    drv = v
}
```

### 8.3 必须注意

- `auth.StartWatcher` 必须传 `raw`，不能传 Vault 包装层，否则 TokenManager 接口会丢。
- **Stop 幂等**：`CachedReader.Stop` 改为 `sync.Once`（当前二次调用 panic）；所有权链唯一：`main → Handler.Close → Vault.Stop → CachedReader.Stop`，任何中间层不得被 main 单独 Stop。补单元测试：重复 Stop 不 panic。
- 所有 offset/size 用 `int64`。
- 不要改现有 302 分支。
- `href` 要百分号编码；中文、空格、`#` 文件名在严格客户端上会 404（v0.10.0 独立修复）。
- `serveDirHTML` 要 HTML 转义，避免文件名 XSS（v0.10.0 独立修复）。

---

## 9. 实施计划

### Phase 0：本地 PoC（1–2 天）

- `cmd/mcrypt`：keygen、加密单文件/文件夹、manifest。
- `internal/vault`：文件头、分块 GCM、解密 Reader、`Stat`。
- 接 `local` 驱动，用 100 MB–2 GB 文件验证：
  - 全文件 SHA-256 一致；
  - `Range` 返回 206；
  - 随机 Seek 不重新下载整个文件；
  - `GET /<vault 根>/_mc/...` 返回 403（I4）；
  - 空文件可解密、头部被篡改时拒绝（I2）；
  - manifest 记录的 fileID 与对象头 fileID 一致性校验（I12，Phase 0 必做）。
- 用 55 GB 文件走一遍代理，测 4K 播放、拖动、`top`、`free`（**执行环境：开发机**——AX6 可用存储仅数 MB，无法在真机本地构造 55 GB 文件；真实 55 GB 用例在 Phase 1 接天翼家庭后真机验证，P17）。

### Phase 1：云端 MVP（1–2 周）

- 接天翼个人/家庭、移动、百度（首个接天翼家庭）。
- URL 过期解析 + 403 刷新。
- 响应头重写、并发限制、错误处理。
- 对象绑定强校验：manifest fileID 与对象头一致、metadata.p 与请求路径比对（I3，PM 拍板放 Phase 1）。
- 播放器兼容性测试。

### Phase 2：加固（1–2 周）

- 块缓存/预读，优化小 Range 和反复拖动。
- 百度 dlink 缓存、URL 失效重试。
- 分卷大文件（百度 4 GB 限制）。
- manifest 增量更新/修复工具。
- 多密钥/密钥轮换。
- GUI（可选）。

### 验收标准

1. 未加密路径的 302 行为完全不变。
2. 加密路径通过 WebDAV 返回明文文件名、大小、MIME。
3. 4K remux 可顺序播放，拖动 10 次以上不失败。
4. URL 过期后自动刷新，播放不中断。
5. 解密后的文件 SHA-256 与原始文件一致。
6. 55 GB 文件可播放、可拖动，内存不超限。

---

## 10. 风险与对策

| 风险 | 影响 | 概率 | 对策 |
|---|---|---|---|
| 移动 URL 15 分钟过期 | 拖动 403 | 高 | 解析 `X-Amz-Expires`；403 刷新；缓存 ≤10 分钟 |
| 天翼个人 URL 28 分钟过期 | 长片拖动 403 | 中 | 解析 `Expires`；403 刷新 |
| 路由器 Go 代理吞吐不足 | 4K 卡顿 | 中 | Phase 0 benchmark；限制并发；必要时 256 KiB 块 |
| 播放器不复用 Range | 拖动失败 | 中 | 用 Infuse/Nova 实测；必要时增加 chunk cache |
| 百度非会员限速/4 GB 限制 | 4K 卡顿/无法上传 | 高 | 分卷；或改用天翼/移动 |
| 55 GB 文件 int32 溢出 | 大偏移失败 | 低 | 全链路 int64；测试 50 GB 偏移 |
| manifest 过大 | 内存不足 | 低 | 1 万文件内可接受；10 万需分片 |
| 上游 Content-Type 错误 | 播放器不识别 | 高 | 代理覆盖 MIME |
| href 未转义 | 中文/特殊字符 404 | 高 | 修 `buildHref` |
| TLS CA 缺失 | CDN 请求失败 | 低 | 更新 ca-bundle；不要关闭校验 |
| 官方工具跳过 `_mc` | manifest/对象缺失 | 低 | 先小文件验证；必要时改目录名 |
| 密钥丢失 | 数据不可恢复 | 中 | 强制备份；导出恢复密钥 |

---

## 11. 最终结论

| 问题 | 结论 |
|---|---|
| 功能能不能做 | **能** |
| AES 性能是否够 | **够**，AX6 约 136 MB/s |
| CDN Range 是否支持 | **支持**（已测三家：天翼个人/家庭、移动均 206；百度走代理路径，Range 待实测，见 §4.5） |
| 55 GB 大文件是否可行 | **可行**，64 位偏移通过 |
| URL 过期是否阻塞 | **不阻塞**，Vault 做 403 刷新 |
| 是否影响现有 302 | **不影响**，只拦截 vault 路径 |
| 主要工作量 | Vault Reader、manifest、URL 管理、播放器兼容 |
| 建议 | **批准进入 Phase 0**，先本地 PoC，再单云盘，最后全量 |

**建议开发顺序：**

1. 先做本地 PoC，证明分块解密 Reader 的 Range/seek 正确；
2. 再接一个云盘（建议天翼家庭，URL 时效长、55 GB 样本现成）；
3. 再接移动（15 分钟 URL，验证 403 刷新）；
4. 最后接百度（现有代理，补 `OpenRange`/dlink 缓存）；
5. 播放器兼容性放在每个阶段末尾，不要最后才测。

---

## 12. 附录

### A. 测试数据速查

```text
AX6 AES-256-GCM 8 KiB : 136,306.69k B/s ≈ 136 MB/s ≈ 1.09 Gbps
天翼个人总大小         : 428,657,011 B
天翼家庭总大小         : 55,302,966,261 B
移动总大小             : 385,229,243 B
天翼家庭 32 MiB 吞吐   : 31,522,077 B/s ≈ 31.5 MB/s
移动 32 MiB 吞吐       : 33,599,320 B/s ≈ 33.6 MB/s
移动 URL 有效期        : 900 s
天翼个人测试时剩余     : 1,680 s
天翼家庭测试时剩余     : 421,980 s
```

### B. 可复现命令

> 注：命令中的 `192.168.2.1` 为测试环境地址（历史实测记录），非产品配置。

```bash
AUTH='admin:<WebDAV密码>'
URL='http://192.168.2.1:8088/189Personal/%E6%88%91%E7%9A%84%E8%A7%86%E9%A2%91/M2TS_ATMOS%E6%B5%8B%E8%AF%95/DTS_OBJECT_EMULATOR.mkv'

# 1. 确认 302（不要用 -I，HEAD 不返回 Location）
curl -s -o /dev/null -w 'status=%{http_code}\n' -u "$AUTH" "$URL"

# 2. 取 CDN 地址
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' -u "$AUTH" "$URL")
printf 'LOC=%s\n' "$LOC"

# 3. Range 测试
curl -s -D - -o /dev/null -A 'Mozilla/5.0' \
  -H 'Range: bytes=1000000-1001023' "$LOC"

# 4. Go UA 重复 10 次
for i in $(seq 1 10); do
  curl -s -o /dev/null -w "%{http_code} %{size_download}\n" \
    -A 'Go-http-client/1.1' \
    -H 'Range: bytes=1000000-1001023' "$LOC"
done

# 5. 32 MiB 吞吐
curl -s -o /dev/null \
  -w 'code=%{http_code} size=%{size_download} time=%{time_total}s speed=%{speed_download}B/s\n' \
  -A 'Go-http-client/1.1' \
  -H 'Range: bytes=0-33554431' "$LOC"

# 6. 总大小
TOTAL=$(curl -s -D - -o /dev/null -A 'Go-http-client/1.1' \
  -H 'Range: bytes=0-0' "$LOC" |
  awk -F'/' '/^Content-Range:/{gsub("\r",""); print $2; exit}')
echo "TOTAL=$TOTAL"

# 7. 64 位偏移（55 GB 文件）
curl -s -D - -o /dev/null -A 'Go-http-client/1.1' \
  -H 'Range: bytes=50000000000-50000001023' "$LOC" | head -n 12

# 8. 查看过期参数（不要打印完整签名）
echo "$LOC" | sed 's/^[^?]*?//' | tr '&' '\n' |
  grep -Ei 'Expires=|X-Amz-(Date|Expires)|Signature' |
  sed -E 's/(Signature)=[^&]*/\1=***/'
```

天翼 `Expires` 是 Unix 秒：

```bash
date -u -d @1794671461
date -u -d @1795091763
```

移动 `X-Amz-Date` + `X-Amz-Expires`：

```bash
date -u -d '2026-11-14 15:10:08' +%s
```

### C. 相关代码位置（统一「文件 + 符号」引用，避免行号漂移）

- [`main.go` / 驱动包装循环](../main.go)：驱动包装
- [`internal/webdav/webdav.go` / `handleGetHead`](../internal/webdav/webdav.go)：GET/HEAD 处理
- [`internal/webdav/webdav.go` / `handleGetHead`（URL 分支）](../internal/webdav/webdav.go)：URL → 302
- [`internal/fs/cache.go` / `linkTTL` 常量](../internal/fs/cache.go)：URL 缓存 10 分钟
- [`internal/fs/cache.go` / `CachedReader.Link`](../internal/fs/cache.go)：Link 缓存实现
- [`internal/fs/retry.go` / `RetryReader.Link`](../internal/fs/retry.go)：Link 重试
- [`drivers/tianyi/driver.go` / `getDownloadLink`](../drivers/tianyi/driver.go)：天翼取下载地址
- [`drivers/mobile/driver.go` / `Link`](../drivers/mobile/driver.go)：移动取下载地址
- [`drivers/baidu/driver.go` / `Link`](../drivers/baidu/driver.go)：百度代理下载
- [`internal/webdav/prop.go` / `buildHref`](../internal/webdav/prop.go)：href 构建

---

*实测数据基于 2026-11-14 环境；v1.3 修订按评审 RV-2026-11-16-01/02、RV-2026-11-17-02/03，仅限文档与新增代码，未改动既有代码路径。*
