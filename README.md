# Liquid Formula

**OpenWrt subscription conversion, DPI obfuscation and LuCI management suite**

[简体中文](#zh-cn) · [English](#english)

**当前源码版本 / Current source version:** `1.8.5`

[1.8.5 双语发布说明 / Bilingual release notes](docs/RELEASE_NOTES_1.8.5.md)

<a id="zh-cn"></a>

## 简体中文

### 项目简介

Liquid Formula 是面向 OpenWrt 的订阅转换与网络辅助工具套件，通过 LuCI 统一管理
订阅转换、JSON 模板、FakeHTTP、FakeSIP、自定义 LuCI Logo/Favicon，以及可选的
内核网络调优。

它会将订阅转换成可供 **sing-box** 使用的 JSON 配置，但**不包含、不启动也不管理
sing-box 本身**。如果系统中已有兼容的 `sing-box` 命令，更新流程会额外运行
`sing-box check` 校验输出；否则仍会执行 JSON 结构校验。你仍需安装
[OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo) 或其他 sing-box
运行时来处理代理、路由、防火墙、访问控制和配置调度。

FakeHTTP 和 FakeSIP 只是数据包混淆辅助工具，不是 VPN、代理或加密层，也不能保证
对抗人工流量分析。

### 1.8.5 更新摘要

- Actions 先将 `.git` 外的所有普通文件规范化为 `0644`，再仅把经过审核的 36 个路径恢复为
  `0755`。
- 仓库中的所有跟踪文件必须以 `100644` 提交；若 `core.fileMode` 等操作把虚假执行位写入
  Git 索引，源码包校验会失败并直接列出相关文件，而不是只报告难以定位的 tree digest。
- 订阅网关统一将不同 Linux 架构的 `Stat_t.Nlink` 转换为 `uint64`，并在 SDK 矩阵前增加
  `amd64`、`arm`、`arm64` 交叉编译覆盖。
- 转换器源码核验使用离线的路径、mode 和 SHA-256 清单，不再在浅克隆中运行时依赖 1.8.3
  Git 对象。
- 冻结的转换器 Go 源码没有任何字节变化；源码/权限测试覆盖正确的全 `100644` 索引、对
  全 `100755` 错误提交的明确拒绝，以及单提交历史，完整发布验证矩阵另行记录。

### 软件包组成

项目只发布两个同步版本的软件包：

| 软件包 | 架构 | 内容 |
| --- | --- | --- |
| `liquid-formula` | 与设备 CPU 架构对应 | `sb-sub-c` 0.7.2-formula 转换器、订阅聚合网关、procd 服务、Momo 与 Local DNS 模板、配置生成与更新脚本、FakeHTTP 0.9.21、FakeSIP 0.9.8 |
| `luci-app-liquid-formula` | `all` | LuCI 页面、rpcd 后端、ACL、安全上传接口、自定义 Logo 和内核调优工具 |

FakeHTTP 和 FakeSIP 已合并到 `liquid-formula` 主包，不再单独发布 DPI 软件包。

### 支持的 OpenWrt 版本

| OpenWrt | 包管理器 | 主包构建目标 | LuCI 包 | 文件格式 |
| --- | --- | ---: | ---: | --- |
| 24.10 | `opkg` | 21 个架构 | 1 个 `all` 包 | `.ipk` |
| 25.12 | `apk` | 22 个架构 | 1 个 `all` 包 | `.apk` |

共计 43 个主包构建目标。Actions 会在构建前解析对应版本系列最新发布的点版本，并
校验 CPU 架构与 OpenWrt target/subtarget 的对应关系；不构建 SNAPSHOT。完整矩阵见
[`.github/arch-matrix.json`](.github/arch-matrix.json)。

本项目并非只支持 Linksys E8450 / Belkin RT3200；它们只是
`aarch64_cortex-a53`、`mediatek/mt7622` 构建目标中的设备示例。

### 主要功能

- 在 LuCI 中配置 1–8 个有序订阅地址、User-Agent、转换端口、密码、刷新周期、模板和
  输出路径。
- 支持 sing-box JSON、base64 URI 列表、明文 URI 列表，以及根级包含 inline
  `proxies` 的 Clash/Mihomo YAML。
- 支持 `ss`、`vmess`、`vless`、`trojan`、`hysteria2`/`hy2`、`tuic`、
  `anytls`、`socks`/`socks5` URI；SSR 会被跳过。
- Clash/Mihomo 的 `proxy-providers` 不会被读取、下载或递归展开；只有 provider、没有
  inline `proxies` 的文档会失败。
- 多订阅按 URL 顺序、再按每个来源中的节点顺序合并。精确重复节点保留首项；同名但
  内容不同的节点会稳定编号，而不会被误删。
- 某个来源失败时使用其自身的 URL 绑定旧缓存继续合并；没有有效旧缓存时整次操作失败，
  不发布半成品。LuCI 仅显示安全的来源序号、格式、计数、fresh/fallback 状态和固定
  错误分类，不暴露完整 URL、令牌或原始上游错误。
- 转换器由 procd 管理，支持开机延迟、健康检查、自动刷新和真实运行状态展示。
- Refresh、Check 和 Update 在后台执行，不占用 LuCI 的短时 RPC 请求。
- JSON 模板可上传、编辑、启用、停用和删除；单个模板最大 1 MiB。成功变更后，模板
  表格与默认模板下拉框会立即一起刷新，并保留页面中其他未保存值。
- 更新输出文件前会生成并校验 JSON，再原子替换正式文件，最多保留 5 份历史备份。
- 同时显示本机环回转换 URL 和 LAN 转换 URL。
- 检测到 OpenWrt-momo 时，可将 FakeHTTP/FakeSIP 当前配置的 mark/mask 同步到
  momo 的 `bypass_fwmark`。
- 内置 FakeHTTP TCP 混淆与 FakeSIP UDP 混淆，两项服务默认均关闭。
- 支持官方 Bootstrap、Fluent，以及版本严格为 2.4.3 的 Argon 主题
  Logo/Favicon 替换。
- 提供可选的 TCP Fast Open、默认 qdisc、拥塞控制、SYN backlog 和
  irqbalance 设置。

### 安装

从 [Latest Release](../../releases/latest) 下载同一 Release 中、与 OpenWrt
版本和设备架构匹配的两个文件：

```text
liquid-formula_<version>_<arch>_openwrt-24.10.ipk
luci-app-liquid-formula_<version>_all_openwrt-24.10.ipk
```

或：

```text
liquid-formula_<version>_<arch>_openwrt-25.12.apk
luci-app-liquid-formula_<version>_all_openwrt-25.12.apk
```

设备架构可从 `/etc/openwrt_release` 中的 `DISTRIB_ARCH` 查看。安装前请用 Release
中的 `SHA256SUMS` 校验文件，并通过 `BUILD_MANIFEST.txt` 核对实际使用的 OpenWrt
点版本、target、Git SHA 和构建时间。

先安装 `liquid-formula`，再安装 `luci-app-liquid-formula`。依赖会从设备已配置的
OpenWrt 软件源中解析。

OpenWrt 24.10：

```sh
opkg update
opkg install /tmp/liquid-formula_<version>_<arch>_openwrt-24.10.ipk
opkg install /tmp/luci-app-liquid-formula_<version>_all_openwrt-24.10.ipk
```

OpenWrt 25.12：

```sh
apk update
apk add --allow-untrusted /tmp/liquid-formula_<version>_<arch>_openwrt-25.12.apk
apk add --allow-untrusted /tmp/luci-app-liquid-formula_<version>_all_openwrt-25.12.apk
```

OpenWrt 25.12 要求 APK 具有受信任签名。直接安装 GitHub Release 中的第三方构建包
时，通常需要 `--allow-untrusted`；请务必先核对 SHA-256。生产环境更推荐将软件包
放入由路由器信任的签名软件源，此时不应使用该选项。更多说明见
[OpenWrt 的 opkg → apk 对照表](https://openwrt.org/docs/guide-user/additional-software/opkg-to-apk-cheatsheet)。

安装后如菜单没有立即出现，请强制刷新浏览器缓存。安装脚本会清理 LuCI 缓存并重启
`rpcd`，不会重启 `uhttpd`。

### 快速开始

1. 打开 **Services → Liquid Formula → Singbox Formula**。
2. 按合并顺序填写 1–8 个订阅地址，并选择订阅服务商能够识别的 User-Agent。
3. 修改默认访问密码 `890716`。
4. 选择一个已启用的模板；首次安装会同时启用 `Momo Template` 和
   `Local DNS Template`，默认仍为 `Momo Template`。
5. 开启转换服务并点击 **Save & Apply**。
6. 等待状态显示为 **Running**，而不是 **Running (not ready)**。
7. 让 sing-box 运行时读取默认输出文件
   `/etc/momo/profiles/config.json`，或请求页面显示的本机/LAN 转换 URL。
8. 如果使用输出文件方式，请点击 **Update output file**。它只更新 JSON 文件，
   不会自动重启 sing-box。

### LuCI 页面

入口：**Services → Liquid Formula**

| 页面 | 功能 |
| --- | --- |
| **Tuning Utility** | 自定义 Logo/Favicon、实时内核参数、可选网络调优和 irqbalance |
| **Singbox Formula** | 转换器配置、集成 URL、momo 绕过、运行状态、后台操作、更新日志和模板管理 |
| **FakeHTTP** | TCP DPI 混淆、payload 管理、接口/方向/协议栈/TTL/NFQUEUE/mark 配置 |
| **FakeSIP** | UDP DPI 混淆、端口过滤、SIP 身份、接口/方向/协议栈/TTL/NFQUEUE/mark 配置 |

模板管理位于 **Singbox Formula** 页面底部，不是独立菜单或独立标签页。
上传、保存、重命名、启用状态变化或删除成功后，模板表格与 **Default template**
下拉框会立即同步更新；无需刷新整个页面，其他尚未保存的设置保持不变。

#### 转换器操作

| 操作 | 作用 |
| --- | --- |
| **Restart converter** | 重启转换器并等待健康检查 |
| **Generate config.yaml** | 根据已保存的 UCI 配置重新生成转换器 YAML |
| **Refresh subscription** | 让正在运行的转换器重新拉取订阅 |
| **Check generated config** | 生成并校验最终 JSON，但不安装 |
| **Update output file** | 生成、校验并原子更新输出文件 |

Check 和 Update 在转换器未运行时会临时启动它；完成后只停止本次临时启动的实例，
不会停止原本已运行的转换器。

#### Tuning Utility

- 使用内置 SVG，或上传不超过 512 KiB 的 PNG Logo、PNG/ICO Favicon。
- 用户上传的 SVG 会被拒绝；图片尺寸上限为 2048 × 2048。
- `cake` 需要 `kmod-sched-cake`，`bbr` 需要 `kmod-tcp-bbr`。
- 内核调优默认关闭；开启后使用包独占的
  `/etc/sysctl.d/99-liquid-formula.conf`。
- Apply、Disable 和卸载恢复共享同一个事务锁；配置与备份使用原子替换，并保留原文件
  模式。读取错误、符号链接目标或不完整回滚会明确失败，而不是报告成功。
- 每个调优键独立备份和恢复，旧备份会在安全时退役；卸载脚本必须先成功恢复持久化
  调优状态，之后才继续其他清理。

#### FakeHTTP

- 默认：关闭、开机延迟 60 秒、出站、IPv4+IPv6、NFQUEUE `8970`、
  mark/mask `0x8000/0x8000`、自动跟随 OpenWrt 默认 WAN。
- 自动模式分别调用官方 `network_find_wan()`/`network_find_wan6()` 和
  `network_get_device()`，因此 PPPoE、DHCP 与静态 WAN 都使用实际 L3 设备。它只跟随
  每个地址族的默认 WAN，不解释 mwan3 或其他多 WAN 策略。
- 手动模式始终覆盖自动解析，并保留原有一个或多个实际设备选择；支持出站、入站或
  双向流量。
- payload 支持 HTTP Host、HTTPS SNI 和 1–1200 字节的 `.bin` 文件，并按
  LuCI 列表顺序轮转。

#### FakeSIP

- 默认：关闭、开机延迟 40 秒、出站、IPv4+IPv6、排除 UDP 53、
  NFQUEUE `8971`、mark/mask `0x10000/0x10000`、自动跟随 OpenWrt 默认 WAN。
- 自动解析和手动优先规则与 FakeHTTP 相同；FakeSIP 不提供不受限制的 all-device
  模式，也不扩展多 WAN 策略。
- 支持 UDP 端口包含、排除或全部匹配，以及自动生成或自定义 SIP URI。
- 不解析 IPv6 扩展头，只处理基础 IPv6 头后直接出现的 UDP 数据包。

### 主要配置

转换器 UCI 文件：`/etc/config/liquid_formula`

| 选项 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `0` | 转换器总开关 |
| `boot_delay` | `90` | 开机自动启动延迟，单位为秒；手动启动不等待 |
| `port` | `9716` | 转换器 HTTP 监听端口 |
| `password` | `890716` | 转换地址访问密码 |
| `subscription_url` | 空 | 0–8 项有序 HTTP(S) 订阅地址；服务启用时至少需要 1 项 |
| `user_agent` | `sing-box 1.11.0` | 拉取订阅时使用的 User-Agent |
| `subscription_timeout` | `60` | 订阅请求超时，单位为秒 |
| `refresh_interval` | `360` | 自动刷新周期，单位为分钟 |
| `default_template` | `momo_template` | 默认模板 ID |
| `momo_bypass_fakehttp` | `1` | 将 FakeHTTP 当前 mark/mask 加入 momo 绕过列表 |
| `momo_bypass_fakesip` | `1` | 将 FakeSIP 当前 mark/mask 加入 momo 绕过列表 |
| `cache_dir` | `/var/lib/liquid-formula/cache` | 转换器缓存目录 |
| `log_file` | `/var/log/liquid-formula/server.log` | 转换器日志 |
| `output_config` | `/etc/momo/profiles/config.json` | 校验后的输出文件 |
| `template_base_url` | `http://127.0.0.1/liquid-formula/templates` | 仅允许指向本机环回地址的模板 URL 前缀 |

订阅刷新采用策略 B。每个 URL 的成功结果与其 URL 摘要绑定；上游失败时只允许回退到
该 URL 自己的已验证旧缓存。只要任一失败来源没有有效缓存，就不会选择新 generation，
也不会安装部分输出，上一份完整配置继续生效。来源顺序变化不会把一个 URL 的缓存错误
借给另一个 URL。

`output_config` 只允许位于以下目录并以 `.json` 结尾：

- `/etc/momo/profiles/`
- `/etc/sing-box/`
- `/var/lib/liquid-formula/output/`

其他配置文件：

- `/etc/config/fakehttp`
- `/etc/config/fakesip`
- `/etc/config/customlogo`
- `/etc/config/tuning`

关键路径：

- 转换服务：`/etc/init.d/liquid-formula`
- FakeHTTP/FakeSIP：`/etc/init.d/fakehttp`、`/etc/init.d/fakesip`
- rpcd 后端：`/usr/libexec/rpcd/liquid_formula`
- 转换器 YAML：`/etc/liquid-formula/config.yaml`
- 模板目录：`/www/liquid-formula/templates/`
- 更新日志：`/var/log/liquid-formula/update.log`
- 转换器日志：`/var/log/liquid-formula/server.log`

### 通过 GitHub 网页上传并自动构建

本仓库支持完全通过 GitHub 网页上传源码。不要沿用旧版 README 中固定的“两批上传、
110 个文件”方案。

1. 解压完整源码包。
2. 进入 `Liquid-Formula-1.8.5`，把其**内部内容**上传到仓库根目录；不要上传 ZIP，
   也不要额外保留一层 `Liquid-Formula-1.8.5`。
3. 显示并上传所有隐藏文件。macOS Finder 可按 `Command + Shift + .`，Windows
   Explorer 可启用“显示隐藏的项目”。
4. 如果网页限制单次文件数量，可按目录分成多轮上传，但必须保持原相对路径。
5. 不得遗漏：
   - 根目录 `.github/`、`.gitignore` 与 `.gitattributes`
   - Go 源码和第三方源码中的 `.gitignore`、`.github`、`.clang-format`
   - `third_party/sources/` 中两个源码压缩包与 `third_party/SHA256SUMS`
6. 仓库中的所有文件必须以 Git mode `100644` 提交。Actions 会在 checkout 后立即通过
   `.github/scripts/restore-executable-modes.sh` 将 `.git` 外的普通文件规范化为 `0644`，再
   仅恢复经过审核的 36 个运行时路径为 `0755`。若提交包含虚假 `100755` mode，源码包
   校验会列出相关文件并失败。
7. 两份 Makefile 的 `PKG_VERSION` 必须一致。通过多轮上传发布新版本时，建议将
   版本号变更放在最后一轮，避免中间状态提前触发正式发布。

Actions 行为：

- Pull Request：运行完整测试和 OpenWrt 25.12 `aarch64_cortex-a53` smoke build。
- 普通 `main`/`master` push：
  - 若 `v<PKG_VERSION>` 尚无 Release，运行完整 43 目标矩阵并自动发布；
  - 若该 Release 已存在，只运行测试和单架构 smoke build。
- `v*` tag：tag 必须等于源码中的 `v<PKG_VERSION>`。
- `workflow_dispatch`：运行完整矩阵，可选择是否发布。
- Release 包含主包、两个 LuCI 包、`SHA256SUMS` 和 `BUILD_MANIFEST.txt`。

所有 `sb-sub-c`、FakeHTTP 和 FakeSIP 可执行文件都由匹配目标的 OpenWrt SDK
从源码交叉编译；仓库不包含预编译 ELF。第三方维护源码、对应归档和 SHA-256
信息见 [`THIRD_PARTY_SOURCES.md`](THIRD_PARTY_SOURCES.md)。

1.8.5 没有修改冻结的原转换器 Go 树 `openwrt-feed/liquid-formula/src`。多订阅网关源码
放在 `src-subscription-gateway`，构建时才复制到现有 Go module 的独立 `cmd` 包中；
这既复用锁定依赖，也保持原转换器源码可逐字节核验。

### 安全与行为声明

- 转换器实际监听 `:<port>`，即路由器所有接口，并非只监听 `127.0.0.1`。
- 页面显示的本机 URL 使用 `127.0.0.1`，但这不会缩小实际监听范围。
- 首次安装密码固定为 `890716`，密码按需求以明文保存在 UCI 和生成配置中。
- 转换 URL 通过 HTTP 查询参数携带明文密码；LAN URL 只应在受信任网络中使用。
- 按项目需求，诊断日志可能保留完整密码、订阅 URL、令牌和查询参数。请严格限制
  LuCI、SSH 和日志读取权限。
- FakeHTTP 和 FakeSIP 默认关闭。错误的 WAN 接口、NFQUEUE、mark/mask 或流量方向
  配置可能导致网络异常，并可能与 mwan3、策略路由、VPN 或 QoS 冲突。
- 自动 WAN 解析只使用 OpenWrt 官方当前默认 IPv4/IPv6 WAN。它不是多 WAN 负载均衡
  或故障转移策略；需要指定其他出口时应使用手动模式。
- 自定义 Logo 会直接在支持的 LuCI 主题头部模板中加入带标记的加载片段；关闭功能
  或卸载 LuCI 包时会移除这些标记。
- 内核调优默认关闭。启用时会备份并移出 `/etc/sysctl.conf` 中由本包管理的同名键；
  关闭时撤销持久化设置，但不会强制恢复当前运行中的 sysctl 值。
- 卸载 LuCI 包前会尝试恢复原有持久化内核设置；恢复失败时会拒绝继续卸载。

### 更新与卸载

安装新版本时，重新安装同一 OpenWrt 系列和同一设备架构的两个新包。不要对整个
OpenWrt 系统执行盲目的 `apk upgrade` 或 `opkg upgrade`。

OpenWrt 24.10：

```sh
opkg remove luci-app-liquid-formula
opkg remove liquid-formula
```

OpenWrt 25.12：

```sh
apk del luci-app-liquid-formula
apk del liquid-formula
```

卸载会停止相关服务、清理项目创建的防火墙对象、移除 Logo 模板标记，并撤销持久化
调优配置。用户生成的数据、上传资源、自定义模板、日志、缓存及输出 JSON 可能不会
自动删除；请在备份后按需手动清理。

卸载 `liquid-formula` 或 `luci-app-liquid-formula` **不会**执行 `apk del`/`opkg remove`
来删除 `momo` 或 `luci-app-momo`，也不会把它们声明为可连带删除的依赖。momo 配置和
运行时的后续清理由 momo 自身软件包负责。

### 致谢与许可证

- 订阅转换器：
  [haierkeys/singbox-subscribe-convert](https://github.com/haierkeys/singbox-subscribe-convert)，
  固定于提交 `8222509aff98229886d304ef72e1d0affb087a62`，本项目标识为
  `0.7.2-formula`。
- 推荐运行时：
  [OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo)。
- FakeHTTP 和 FakeSIP 的来源、维护差异与校验信息见
  [`THIRD_PARTY_SOURCES.md`](THIRD_PARTY_SOURCES.md)。

项目包含 Apache-2.0、GPL-3.0-or-later 和 MIT 授权内容，具体以各源码目录中的
许可证文件为准。

[返回顶部](#liquid-formula)

---

<a id="english"></a>

## English

### Overview

Liquid Formula is an OpenWrt subscription-conversion and network utility suite. Its LuCI
interface manages subscription conversion, JSON templates, FakeHTTP, FakeSIP, custom LuCI
logo/favicon assets, and optional kernel network tuning.

It produces JSON profiles for **sing-box**, but it **does not bundle, start, or manage
sing-box**. When a compatible `sing-box` command is already installed, the update path also runs
`sing-box check`; otherwise it still performs the JSON structure check. A runtime such as
[OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo) is still
required for proxying, routing, firewall policy, access control, and profile scheduling.

FakeHTTP and FakeSIP are packet-obfuscation helpers only. They are not a VPN, proxy, or
encryption layer and do not guarantee resistance to manual traffic analysis.

### 1.8.5 highlights

- Actions first normalizes every regular file outside `.git` to `0644`, then restores exactly the
  36 reviewed executable-allowlist paths to `0755`.
- Every tracked repository file must be committed as `100644`. If `core.fileMode` or another tool
  records false executable bits, source-package validation fails and lists the offending paths
  instead of reporting only an opaque tree digest.
- The subscription gateway converts architecture-dependent `Stat_t.Nlink` fields to `uint64` and
  adds `amd64`, `arm`, and `arm64` cross-compilation coverage before the SDK matrix.
- Converter-source verification uses an offline path/mode/SHA-256 manifest and no longer needs a
  runtime 1.8.3 Git object in a shallow checkout.
- The frozen converter Go source is byte-for-byte unchanged. Source/permission tests cover a valid
  all-`100644` index, explicit rejection of an invalid all-`100755` commit, and single-commit
  history; final release-matrix verification is recorded separately.

### Packages

Only two packages are released, and they always share the same version:

| Package | Architecture | Contents |
| --- | --- | --- |
| `liquid-formula` | Device-specific | `sb-sub-c` 0.7.2-formula, subscription gateway, procd service, Momo and Local DNS templates, configuration/update helpers, FakeHTTP 0.9.21, and FakeSIP 0.9.8 |
| `luci-app-liquid-formula` | `all` | LuCI views, rpcd backend, ACL, secure upload endpoint, custom-logo support, and kernel tuning |

FakeHTTP and FakeSIP are part of the main `liquid-formula` package. There is no separate DPI
package.

### Supported OpenWrt releases

| OpenWrt | Package manager | Main-package targets | LuCI package | Format |
| --- | --- | ---: | ---: | --- |
| 24.10 | `opkg` | 21 architectures | one `all` package | `.ipk` |
| 25.12 | `apk` | 22 architectures | one `all` package | `.apk` |

The matrix contains 43 main-package targets. Before each full build, Actions resolves the newest
published point release in each supported series and verifies every architecture-to-target mapping.
SNAPSHOT is intentionally excluded. See
[`.github/arch-matrix.json`](.github/arch-matrix.json) for the authoritative list.

Support is not limited to the Linksys E8450 / Belkin RT3200; those devices are examples of the
`aarch64_cortex-a53`, `mediatek/mt7622` target.

### Features

- Configure one to eight ordered subscription URLs, User-Agent, port, password, refresh interval,
  templates, and output path from LuCI.
- Accept sing-box JSON, base64-encoded URI lists, plain URI lists, and Clash/Mihomo YAML containing
  root-level inline `proxies`.
- Parse `ss`, `vmess`, `vless`, `trojan`, `hysteria2`/`hy2`, `tuic`, `anytls`, and
  `socks`/`socks5` URIs; SSR entries are skipped.
- Never read, download, or recurse through Clash/Mihomo `proxy-providers`; a provider-only
  document fails because it has no inline nodes.
- Merge sources in URL order and then node order. Exact semantic duplicates keep their first
  occurrence, while same-name nodes with different content remain under deterministic numbering.
- Fall back per URL to that URL's bound old cache. If a failed source has no valid cache, the whole
  attempt fails without publishing a partial generation. LuCI exposes only safe source indices,
  format/count data, fresh/fallback state, and fixed failure categories—not complete URLs, tokens,
  or raw upstream errors.
- Run the converter under procd with boot delay, health checks, automatic refresh, and truthful
  readiness status.
- Run Refresh, Check, and Update as background actions outside LuCI's short RPC lifetime.
- Upload, edit, enable, disable, and delete JSON templates up to 1 MiB each. Successful mutations
  immediately refresh both the table and default-template dropdown while retaining unrelated
  unsaved form values.
- Validate generated JSON and atomically replace the output file, retaining up to five backups.
- Display both loopback and LAN conversion URLs.
- Synchronize the current FakeHTTP/FakeSIP mark and mask into momo's `bypass_fwmark` when
  OpenWrt-momo is installed.
- Ship optional FakeHTTP TCP and FakeSIP UDP obfuscation services, both disabled by default.
- Replace logo/favicon assets in Bootstrap, Fluent, and exactly Argon 2.4.3.
- Optionally manage TCP Fast Open, the default qdisc, congestion control, SYN backlog, and
  irqbalance.

### Installation

Download both matching assets from the same
[Latest Release](../../releases/latest):

```text
liquid-formula_<version>_<arch>_openwrt-24.10.ipk
luci-app-liquid-formula_<version>_all_openwrt-24.10.ipk
```

or:

```text
liquid-formula_<version>_<arch>_openwrt-25.12.apk
luci-app-liquid-formula_<version>_all_openwrt-25.12.apk
```

Read `DISTRIB_ARCH` in `/etc/openwrt_release` to identify the device architecture. Verify the
downloads against `SHA256SUMS`, and use `BUILD_MANIFEST.txt` to check the exact OpenWrt point
release, target, Git SHA, and build time.

Install `liquid-formula` first, then `luci-app-liquid-formula`. Dependencies are resolved from
the OpenWrt feeds configured on the router.

OpenWrt 24.10:

```sh
opkg update
opkg install /tmp/liquid-formula_<version>_<arch>_openwrt-24.10.ipk
opkg install /tmp/luci-app-liquid-formula_<version>_all_openwrt-24.10.ipk
```

OpenWrt 25.12:

```sh
apk update
apk add --allow-untrusted /tmp/liquid-formula_<version>_<arch>_openwrt-25.12.apk
apk add --allow-untrusted /tmp/luci-app-liquid-formula_<version>_all_openwrt-25.12.apk
```

OpenWrt 25.12 requires trusted APK signatures. Direct installation of third-party GitHub Release
assets normally needs `--allow-untrusted`; verify SHA-256 first. For production, prefer a signed
package feed trusted by the router and omit this flag. See OpenWrt's
[opkg-to-apk cheat sheet](https://openwrt.org/docs/guide-user/additional-software/opkg-to-apk-cheatsheet).

If the menu does not appear immediately after installation, hard-refresh the browser. The
post-install script clears LuCI caches and restarts `rpcd`; it deliberately does not restart
`uhttpd`.

### Quick start

1. Open **Services → Liquid Formula → Singbox Formula**.
2. Enter one to eight subscription URLs in merge order and choose a User-Agent accepted by the
   providers.
3. Replace the default access password `890716`.
4. Choose an enabled template. A new installation enables both `Momo Template` and
   `Local DNS Template`; `Momo Template` remains the default.
5. Enable the converter and click **Save & Apply**.
6. Wait for **Running**, rather than **Running (not ready)**.
7. Point the sing-box runtime at `/etc/momo/profiles/config.json`, or use the loopback/LAN URL
   shown by LuCI.
8. When using the output-file method, click **Update output file**. This writes JSON but does not
   restart sing-box.

### LuCI pages

Path: **Services → Liquid Formula**

| Page | Purpose |
| --- | --- |
| **Tuning Utility** | Custom logo/favicon, live kernel values, optional network tuning, and irqbalance |
| **Singbox Formula** | Converter configuration, integration URLs, momo bypass, runtime status, background actions, update log, and template management |
| **FakeHTTP** | TCP DPI obfuscation, payload management, interface/direction/family/TTL/NFQUEUE/mark settings |
| **FakeSIP** | UDP DPI obfuscation, port filters, SIP identity, interface/direction/family/TTL/NFQUEUE/mark settings |

Template management is embedded at the bottom of **Singbox Formula**; it is not a separate menu
entry or tab.
After an upload, save, rename, enable/disable change, or delete succeeds, the table and
**Default template** dropdown update together immediately. The page is not reloaded, so other
unsaved settings remain intact.

#### Converter actions

| Action | Effect |
| --- | --- |
| **Restart converter** | Restart the converter and wait for its health check |
| **Generate config.yaml** | Regenerate converter YAML from committed UCI settings |
| **Refresh subscription** | Ask the running converter to fetch the subscription again |
| **Check generated config** | Generate and validate final JSON without installing it |
| **Update output file** | Generate, validate, and atomically replace the output file |

Check and Update temporarily start the converter when it is stopped. They stop only an instance
started for that operation and never stop a converter that was already running.

#### Tuning Utility

- Use the built-in SVG, or upload a PNG logo and PNG/ICO favicon up to 512 KiB.
- Uploaded SVG files are rejected; the maximum image size is 2048 × 2048.
- `cake` requires `kmod-sched-cake`; `bbr` requires `kmod-tcp-bbr`.
- Kernel tuning is disabled by default and uses the package-owned
  `/etc/sysctl.d/99-liquid-formula.conf` when enabled.
- Apply, Disable, and uninstall restoration share one transaction lock. Configuration and backup
  writes are atomic and preserve the original file mode. Read failures, symlink targets, and
  incomplete rollback fail explicitly instead of reporting success.
- Each managed key is backed up and restored independently, with safe stale-backup retirement.
  Package removal must restore persistent tuning successfully before later cleanup proceeds.

#### FakeHTTP

- Defaults: disabled, 60-second boot delay, outbound, dual stack, NFQUEUE `8970`, mark/mask
  `0x8000/0x8000`, and automatic OpenWrt default-WAN selection.
- Auto mode uses the official `network_find_wan()`/`network_find_wan6()` plus
  `network_get_device()`, so PPPoE, DHCP, and static WANs resolve to their actual L3 device. It
  follows only the default WAN per address family and does not interpret mwan3 or other multi-WAN
  policy.
- Manual mode always overrides auto detection and retains the existing selection of one or more
  actual devices. Outbound, inbound, and bidirectional processing remain available.
- Payload rows may use HTTP Host, HTTPS SNI, or validated 1–1200 byte `.bin` files, rotated in
  their LuCI order.

#### FakeSIP

- Defaults: disabled, 40-second boot delay, outbound, dual stack, exclude UDP 53, NFQUEUE `8971`,
  mark/mask `0x10000/0x10000`, and automatic OpenWrt default-WAN selection.
- Auto resolution and manual precedence match FakeHTTP. FakeSIP has no unrestricted all-device
  mode and does not extend multi-WAN policy handling.
- Supports include, exclude, or all-port UDP filters and automatic or custom SIP URIs.
- Does not parse IPv6 extension headers; only UDP directly following the base IPv6 header is
  processed.

### Configuration

Converter UCI file: `/etc/config/liquid_formula`

| Option | Default | Description |
| --- | --- | --- |
| `enabled` | `0` | Converter master switch |
| `boot_delay` | `90` | Boot-only autostart delay in seconds; manual starts are immediate |
| `port` | `9716` | Converter HTTP listening port |
| `password` | `890716` | Conversion URL access password |
| `subscription_url` | empty | Ordered list of 0–8 HTTP(S) URLs; at least one is required while enabled |
| `user_agent` | `sing-box 1.11.0` | User-Agent sent to the provider |
| `subscription_timeout` | `60` | Subscription request timeout in seconds |
| `refresh_interval` | `360` | Automatic refresh interval in minutes |
| `default_template` | `momo_template` | Default template ID |
| `momo_bypass_fakehttp` | `1` | Add the current FakeHTTP mark/mask to momo bypass |
| `momo_bypass_fakesip` | `1` | Add the current FakeSIP mark/mask to momo bypass |
| `cache_dir` | `/var/lib/liquid-formula/cache` | Converter cache directory |
| `log_file` | `/var/log/liquid-formula/server.log` | Converter log |
| `output_config` | `/etc/momo/profiles/config.json` | Validated output file |
| `template_base_url` | `http://127.0.0.1/liquid-formula/templates` | Loopback-only template URL prefix |

Subscription refresh uses policy B. Each successful source result is bound to a digest of its
exact URL. If an upstream request fails, only that URL's own validated old cache may be used. If
any failed source lacks a valid cache, no new generation or partial output is selected and the
previous complete configuration remains active. Reordering URLs cannot lend one URL's cache to
another.

`output_config` must end in `.json` and reside under one of:

- `/etc/momo/profiles/`
- `/etc/sing-box/`
- `/var/lib/liquid-formula/output/`

Other UCI files:

- `/etc/config/fakehttp`
- `/etc/config/fakesip`
- `/etc/config/customlogo`
- `/etc/config/tuning`

Key paths:

- Converter service: `/etc/init.d/liquid-formula`
- FakeHTTP/FakeSIP: `/etc/init.d/fakehttp`, `/etc/init.d/fakesip`
- rpcd backend: `/usr/libexec/rpcd/liquid_formula`
- Generated converter YAML: `/etc/liquid-formula/config.yaml`
- Template directory: `/www/liquid-formula/templates/`
- Update log: `/var/log/liquid-formula/update.log`
- Converter log: `/var/log/liquid-formula/server.log`

### GitHub web upload and automatic builds

The repository supports a source-only workflow through GitHub's web uploader. Do not reuse the
obsolete fixed “two batches / 110 files” instructions from older README versions.

1. Extract the complete source archive.
2. Enter `Liquid-Formula-1.8.5` and upload its **contents** to the repository root. Do not upload
   the ZIP or retain `Liquid-Formula-1.8.5` as an extra directory level.
3. Reveal and include hidden files. Press `Command + Shift + .` in macOS Finder, or enable hidden
   items in Windows Explorer.
4. If the browser limits one upload, split it by directory across several commits while preserving
   every relative path.
5. Do not omit:
   - root `.github/`, `.gitignore`, and `.gitattributes`;
   - nested `.gitignore`, `.github`, and `.clang-format` files in Go/third-party source;
   - both source archives in `third_party/sources/` and `third_party/SHA256SUMS`.
6. Every repository file must be committed with Git mode `100644`. Immediately after checkout,
   Actions invokes `.github/scripts/restore-executable-modes.sh` to normalize every regular file
   outside `.git` to `0644`, then restore only the 36 reviewed runtime paths to `0755`. If a commit
   contains false `100755` modes, source-package validation lists the offending paths and fails.
7. The two package Makefiles must contain the same `PKG_VERSION`. In a multi-round release upload,
   upload the version bump last to avoid publishing an incomplete intermediate tree.

Actions behavior:

- Pull requests run the full test suite and an OpenWrt 25.12 `aarch64_cortex-a53` smoke build.
- An ordinary `main`/`master` push:
  - runs the complete 43-target matrix and creates a release when `v<PKG_VERSION>` does not exist;
  - otherwise runs tests and a single-target smoke build.
- A `v*` tag must exactly equal `v<PKG_VERSION>`.
- `workflow_dispatch` runs the full matrix and can optionally publish.
- Release assets include the main packages, two LuCI packages, `SHA256SUMS`, and
  `BUILD_MANIFEST.txt`.

All `sb-sub-c`, FakeHTTP, and FakeSIP executables are cross-compiled from source by the matching
OpenWrt SDK. No precompiled ELF is stored in the repository. See
[`THIRD_PARTY_SOURCES.md`](THIRD_PARTY_SOURCES.md) for maintained-source provenance, archives,
and SHA-256 records.

Release 1.8.5 does not modify the frozen original converter Go tree at
`openwrt-feed/liquid-formula/src`. The multi-subscription gateway lives in
`src-subscription-gateway` and is copied into a separate `cmd` package in the existing Go module
only at build time. This reuses the locked dependency set while keeping the original converter
source byte-verifiable.

### Security and behavior disclosures

- The converter listens on `:<port>` on all router interfaces, not only on `127.0.0.1`.
- The loopback URL shown by LuCI does not narrow the actual listening scope.
- The initial password is the fixed value `890716`, stored in plaintext in UCI and generated
  configuration by design.
- Conversion URLs carry the plaintext password in an HTTP query parameter. Use the LAN URL only on
  a trusted network.
- By project requirement, diagnostics may retain complete passwords, subscription URLs, tokens,
  and query parameters. Restrict LuCI, SSH, and log access.
- FakeHTTP and FakeSIP are disabled by default. Incorrect WAN, NFQUEUE, mark/mask, or direction
  settings can disrupt networking or conflict with mwan3, policy routing, VPNs, or QoS.
- Automatic WAN resolution uses only OpenWrt's current default IPv4/IPv6 WAN. It is not a
  multi-WAN load-balancing or failover policy; select devices manually when another egress is
  required.
- Custom Logo directly inserts marked loader snippets into supported LuCI theme header templates.
  Disabling the feature or uninstalling the LuCI package removes those markers.
- Kernel tuning is disabled by default. When enabled, matching keys in `/etc/sysctl.conf` are backed
  up and moved aside. Disabling removes persistent management but does not forcibly reset live
  sysctl values.
- Before uninstalling the LuCI package, the package attempts to restore prior persistent kernel
  settings and refuses removal if that restoration fails.

### Updating and uninstalling

To update, install both newer packages for the same OpenWrt series and device architecture. Do not
blindly run a system-wide `apk upgrade` or `opkg upgrade`.

OpenWrt 24.10:

```sh
opkg remove luci-app-liquid-formula
opkg remove liquid-formula
```

OpenWrt 25.12:

```sh
apk del luci-app-liquid-formula
apk del liquid-formula
```

Removal stops the related services, cleans firewall objects created by the project, removes custom
logo markers, and reverses persistent tuning. User-generated data, uploaded assets, custom
templates, logs, caches, and generated output JSON may remain; back them up and remove them
manually if required.

Removing `liquid-formula` or `luci-app-liquid-formula` **does not** run `apk del`/`opkg remove`
for `momo` or `luci-app-momo`, and neither package is declared as a removable dependency. Any
later cleanup of momo configuration or runtime data belongs to momo's own package lifecycle.

### Credits and licenses

- Converter:
  [haierkeys/singbox-subscribe-convert](https://github.com/haierkeys/singbox-subscribe-convert),
  pinned to `8222509aff98229886d304ef72e1d0affb087a62` and identified here as
  `0.7.2-formula`.
- Recommended runtime:
  [OpenWrt-momo](https://github.com/nikkinikki-org/OpenWrt-momo).
- See [`THIRD_PARTY_SOURCES.md`](THIRD_PARTY_SOURCES.md) for FakeHTTP/FakeSIP provenance and
  maintained changes.

The source tree contains Apache-2.0, GPL-3.0-or-later, and MIT licensed components. Refer to the
license files in each source directory for authoritative terms.

[Back to top](#liquid-formula)
