# Liquid Formula 1.8.6 发布说明 / Release Notes

## 中文

1.8.6 以用户存档的 1.8.3 源码为基线，补回 1.8.4/1.8.5 中已经确认、且不依赖
多订阅聚合的功能和可靠性修复。运行时重新固定为一个订阅 URL、一个 `sb-sub-c`
实例和端口 9716。

### 单订阅与 1.8.5 迁移

- 多订阅链接、聚合 generation、fallback source、9717 gateway 及其等待包装器不属于
  1.8.6。每个订阅服务商可能要求不同 User-Agent，因此不再声称单一 UA 能可靠聚合
  多家供应商。
- 采用已确认的**方案 A（Plan A）**：若 1.8.5 的 `liquid_formula.main` 保存了有序的
  多条 `list subscription_url`，升级时只保留原顺序第一条，转换为一个
  `option subscription_url`，明确丢弃其余条目；`enabled` 状态不变。
- 对带 1.8.4/1.8.5 gateway 标记的 `/etc/liquid-formula/config.yaml`，升级脚本仅在
  备份不存在时创建 mode 0600 的 `/etc/liquid-formula/config.yaml.pre-1.8.6`，然后用
  单订阅生成器重建。没有 gateway 标记的用户自定义 YAML 不会被升级脚本覆盖。
- 旧 `singbox-formula` 服务会被停止并停用，但其配置、日志、持久状态、运行目录和
  init 脚本会保留为旧命名空间，供用户按清理指南备份、比较后手工处理；安装过程不再
  递归删除潜在的唯一恢复来源。
- Check/Update 在服务关闭时仍可临时启动转换器；成功、失败、部分启动失败和停止失败
  路径均保持原总开关状态并释放本次生命周期资源。

### 订阅兼容性

- 新安装默认 User-Agent 更新为 `v2rayN/7.24.4`。
- LuCI 提供维护过的 v2rayN、v2rayNG、sing-box SFI/SFA/SFM 和 Karing 预设，同时
  保留自定义输入；已知旧内置值做精确升级，任意自定义值和显式空值保持原样。
- LuCI、Shell 生成器和更新入口统一严格校验单一 HTTP/HTTPS URL，包括 authority、
  空白、百分号转义、userinfo、hostname、端口和 IPv6 literal 边界。

### 模板、WAN 与 Tuning

- 更新包内权威 `momo-template.json`，新增并默认启用
  `localdns_template`；新安装仍以 `momo_template` 为默认模板。
- 模板新建、编辑、删除或恢复成功后，LuCI 原子刷新模板表和默认模板选项，不整页
  重载，也不丢失同页尚未保存的配置。
- FakeHTTP/FakeSIP 默认通过 OpenWrt 官方网络状态自动解析实际 WAN/L3 设备，兼容
  DHCP、静态、PPPoE、IPv4/IPv6、重拨和热插拔；用户手工指定的接口始终优先。
- `tcp_congestion_control` 固定为 `bbr` / `cubic` / `reno`，默认 `bbr`。
- `default_qdisc` 固定为 `cake` / `cake_mq` / `fq_codel`，默认 `cake`；`cake_mq`
  仅在 OpenWrt 25.12 及以上显示，Shell 后端也执行同一版本门槛。
- `tcp_max_syn_backlog` 固定为 `128` / `512` / `1024` / `2048`，默认 `512`。
- 升级只补真正缺失的 Tuning/DPI 字段，不覆盖已有值或显式空值；事务写入、回滚、
  irqbalance 和卸载恢复语义保持不变。

### Actions 与源码完整性

- Git 仓库约定所有跟踪文件提交为 mode `100644`，checkout 后再由审核白名单恢复所需
  脚本的 `0755`。mode 错误会直接列出文件，而不是只报 tree digest 不匹配。
- 冻结转换器源码改用离线的路径、mode 和 SHA-256 manifest 校验，不依赖 1.8.3 历史
  Git 对象，兼容浅克隆和 GitHub 网页上传。
- Actions 继续覆盖 amd64、arm、arm64 的转换器编译检查；转换器 Go 源码、第三方源码
  归档及其 SHA-256 仍以存档 1.8.3 为边界。

安装后如需清理 1.5.0–1.8.5 遗留文件，请严格按
[`CLEANUP_1.5.0_TO_1.8.5.md`](CLEANUP_1.5.0_TO_1.8.5.md) 分类核对，不能直接删除整个
`/etc/liquid-formula` 或 `/var/lib/liquid-formula`。

## English

Liquid Formula 1.8.6 is rebuilt from the user's archived 1.8.3 source tree. It forward-ports the
reviewed 1.8.4/1.8.5 improvements that do not depend on subscription aggregation, while restoring
one subscription URL, one `sb-sub-c` process, and port 9716.

### Single-subscription migration

- Multiple subscription URLs, aggregation generations, fallback sources, the port-9717 gateway,
  and its wait wrapper are not part of 1.8.6.
- The approved **Plan A** migration converts an ordered 1.8.5 `list subscription_url` to one scalar
  option by retaining the first URL and dropping the rest. It preserves the enabled state.
- A gateway-generated YAML is backed up once as
  `/etc/liquid-formula/config.yaml.pre-1.8.6` with mode 0600 before scalar regeneration. An ordinary
  user-maintained YAML is preserved.
- The legacy `singbox-formula` service is stopped and disabled, while its legacy namespaces,
  logs, persistent state, runtime directory, and init script are preserved for backup and manual
  comparison instead of being recursively deleted during installation.
- Disabled-service Check/Update operations retain their temporary-instance ownership and cleanup
  guarantees on success and failure paths.

### Compatibility and usability

- Fresh installations default to `v2rayN/7.24.4`. LuCI exposes maintained v2rayN, v2rayNG,
  sing-box SFI/SFA/SFM, and Karing presets while continuing to accept a custom provider-specific
  value. Known old built-ins are upgraded exactly; custom and explicitly empty values are kept.
- The LuCI, generator, and update paths share strict scalar HTTP/HTTPS URL validation.
- The authoritative Momo template is refreshed, `localdns_template` is added and enabled, and Momo
  remains the fresh-install default. Successful template mutations update the table and default
  selector atomically without discarding other unsaved form fields.
- FakeHTTP/FakeSIP resolve official OpenWrt WAN/L3 state automatically for DHCP, static, PPPoE,
  IPv4/IPv6, reconnects, and hotplug. Explicit interface selections always win.
- Tuning choices are fixed to the documented congestion-control, qdisc, and SYN-backlog profiles.
  `cake_mq` is visible and accepted only on OpenWrt 25.12 or newer.

### CI and source integrity

- Every tracked file is committed as `100644`; the reviewed runtime allowlist restores `0755`.
  Incorrect Git modes are reported by path.
- Frozen converter sources are checked with a history-independent path/mode/SHA-256 manifest, so
  shallow checkout and browser upload do not depend on an opaque historical tree digest.
- CI keeps amd64, arm, and arm64 converter compile coverage. The archived converter Go source and
  third-party source/hash boundaries remain unchanged from 1.8.3.

Use the manual [`1.5.0–1.8.5 cleanup guide`](CLEANUP_1.5.0_TO_1.8.5.md) only after 1.8.6 has been
installed and verified.
