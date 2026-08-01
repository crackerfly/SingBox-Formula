# Liquid Formula 1.8.6 单订阅回归与可靠性设计

## 目标

Liquid Formula 1.8.6 从用户存档的 1.8.3 源码重新向前构建，保留 1.8.4/1.8.5 中与多订阅无关的功能和修复，并彻底放弃多订阅聚合网关。升级后的运行时继续使用一个 `sb-sub-c` 进程、一个订阅 URL 和 9716 端口。

## 不在范围内

- 不包含多个订阅 URL、订阅聚合、订阅 generation、fallback source 或 9717 gateway。
- 不修改冻结的 `openwrt-feed/liquid-formula/src` 转换器 Go 源码。
- 不管理、卸载或清理 momo 自身的文件。
- 不引入 mwan3 或第三方 WAN 发现依赖。

## 单订阅运行时与 1.8.5 降级迁移

- UCI 继续使用单值 `option subscription_url`。
- procd 只发布 `main` 实例，直接运行 `/usr/bin/sb-sub-c`；健康端点保持 `127.0.0.1:9716/health`。
- 1.8.5 若保存了多个 `list subscription_url`，按 UCI 原始顺序保留第一条并转换成单值，明确丢弃其余条目；服务的 enabled 状态不变。
- 若现有 `/etc/liquid-formula/config.yaml` 带有 1.8.4/1.8.5 gateway 生成标记或 gateway 配置块，迁移脚本先以 0600 创建一次 `/etc/liquid-formula/config.yaml.pre-1.8.6`，再由单订阅生成器重建。普通用户自定义配置不因版本升级被覆盖。
- 删除包内 gateway 二进制、等待脚本、源码、配置段和 LuCI 状态展示；历史运行数据只在清理文档中列出，不由升级脚本递归删除。
- `wait-subscription-gateway.sh` 的 `IPKG_INSTROOT` 错误随脚本整体退役，不把该包装器带入 1.8.6。

## 模板

- 更新包内权威 `momo-template.json`，新增包内权威 `localdns-template.json`；根目录的
  `momo-template.json` 继续作为 1.8.3 存档来源证据保持逐字节不变，测试分别固定两类边界和摘要。
- 新安装默认启用两个模板，默认模板保持 momo；升级保留用户模板文件、section、enabled 状态和默认选择。
- 模板新建、编辑、删除或恢复成功后，LuCI 原子刷新模板表格及默认模板下拉框，不刷新整页，不丢失同页未保存字段。
- 模板事务失败继续回滚文件与对应 UCI section，并显示明确错误。

## 订阅 URL 和 User-Agent

- 单 URL 在 LuCI、生成器和 RPC/更新路径执行一致的严格校验：只允许 HTTP/HTTPS；拒绝空 host、首尾/内嵌空白、无效百分号转义、控制字符、非法 userinfo、端口和 IPv6 literal；合法大小写 scheme、合法 userinfo、标准 IPv6、IPv4-mapped IPv6、zone 和 `%20` 保持原样。
- 新安装 User-Agent 默认值为 `v2rayN/7.24.4`，以请求供应商返回 1.8.3 转换器可靠支持的节点列表。
- LuCI 保留可手工输入的 Value 控件，并只提供以下维护过的预设：
  - `v2rayN/7.24.4`
  - `v2rayNG/2.2.6`
  - `sing-box 1.13.15`
  - `SFI/1.13.15 (sing-box 1.13.15)`
  - `SFA/1.13.15 (sing-box 1.13.15)`
  - `SFM/1.13.15 (sing-box 1.13.15)`
  - `Karing/1.2.23.2605`
- 已知旧内置值做精确升级：sing-box/SFI/SFA/SFM 1.11.0、v2rayN 7.0.0、v2rayNG 1.9.16、Karing 1.0.0 分别映射到上述新值。任意自定义值、显式空值和其他旧预设保持原样，避免擅自改变供应商协商行为。
- 移除会诱导供应商返回 Clash YAML 或无法核实当前版本的旧下拉预设；用户仍可通过自定义输入使用任何供应商要求的 UA。

## FakeHTTP/FakeSIP WAN 自动选择

- 新增共享的官方 OpenWrt 网络解析 helper，默认使用 WAN 自动模式。
- 自动模式通过 ubus/network helper 解析实际 L3 device，兼容 DHCP、静态、PPPoE、IPv4、IPv6、重拨和热插拔。
- 用户手工选择的接口始终优先，不被自动解析覆盖。
- hotplug 仅协调受影响服务，IPv4 或 IPv6 单族暂时消失时不误停仍可工作的另一族；不使用 mwan3。
- 从旧配置升级时仅补缺失的 WAN 模式字段，不覆盖已有接口选择。

## Tuning 固定契约

- `tcp_congestion_control` 使用固定 ListValue：`bbr`、`cubic`、`reno`；新安装默认 `bbr`。
- `default_qdisc` 使用固定 ListValue：`cake`、`cake_mq`、`fq_codel`；新安装默认 `cake`。
- `cake_mq` 仅在 OpenWrt 25.12 或更高版本显示。版本从 `/etc/openwrt_release` 的 `DISTRIB_RELEASE` 经只读 RPC 状态返回；无法可靠解析时隐藏。
- Shell 后端使用同一门槛：低于 25.12 或版本未知时拒绝 `cake_mq`，避免绕过界面写入不可支持值。
- `tcp_max_syn_backlog` 使用固定 ListValue：`128`、`512`、`1024`、`2048`；新安装默认 `512`。
- UCI defaults 只补真正缺失的字段，不覆盖已有值或显式空值。旧值若不在白名单，不静默改写；保存/应用时给出明确拒绝信息，用户自行选择有效值。
- Tuning 的事务性 sysctl 写入、回滚、irqbalance 处理和卸载恢复行为保持不变。

## 升级安全性

- 主包迁移只填补真正缺失的 DPI 值，不用默认值覆盖用户已有值。
- 未带 gateway 标记的现有 `config.yaml` 作为 conffile 保留。
- 所有配置生成、模板写入和输出安装继续使用同文件系统 staging、0600 权限、原子 rename 和失败回滚。

## GitHub Actions 与源码完整性

- 仓库约定所有跟踪文件提交为 mode 100644；需要执行的脚本由 `.github/scripts/restore-executable-modes.sh` 在 checkout 后按审核白名单恢复为 0755。
- `test_source_package.sh` 在 mode 错误时直接列出违规路径。
- 冻结转换器使用离线 manifest 校验路径、mode 和 SHA-256，不依赖 1.8.3 Git 历史对象或 tree digest，因此兼容 `fetch-depth: 1` 和 GitHub 网页上传。
- 模拟“全部工作树文件 0755”和“错误 Git 索引 100755”两种场景；前者可恢复，后者由清晰诊断拒绝。
- Actions 继续覆盖 amd64、arm、arm64 的 Go 编译检查；第三方源码哈希与转换器源树内容保持 1.8.3 不变。

## 清理文档

新增 `docs/CLEANUP_1.5.0_TO_1.8.5.md`，按“必须保留、可明确清理、仅条件清理”分类列出准确路径、停服条件、敏感性和推荐顺序。文档必须特别说明：

- 不可整目录删除 `/etc/liquid-formula`、`/var/lib/liquid-formula/cache`、模板目录、合法输出目录、momo 命名空间或 Tuning 恢复文件。
- 聚合器专属的 gateway 二进制、包装器、`subscriptions` 状态树和锁仅在 1.8.6 已验证且旧进程完全停止后清理。
- 1.5.0–1.8.3 的旧命名空间、payload、上传 staging、锁和包管理冲突备份只能逐项比较后清理。

## 测试与交付

- 所有行为变更先增加失败测试，再实施最小修复。
- 至少覆盖生成器、迁移、procd、RPC、模板事务、Tuning、WAN/DPI、LuCI 24.10/25.12 render、源码包、web-upload 权限和发布工作流。
- 原转换器 Go tree 与 1.8.3 逐文件字节一致；第三方树和 SHA-256 不变。
- 版本统一升为 1.8.6，生成完整源码包、1.8.3→1.8.6 增量包、精确文件清单和 SHA-256，并使用新的缓存安全文件名。
