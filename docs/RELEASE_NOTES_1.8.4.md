# Liquid Formula 1.8.4

[简体中文](#简体中文) · [English](#english)

## 简体中文

### 主要变化

- 更新内置 `momo-template.json` 与 `localdns-template.json`。两者在新安装中均启用，
  `momo_template` 仍为默认模板；升级继续保留用户修改过的 conffile。
- 模板上传、保存、重命名、启用状态变化或删除后，模板表格与 **Default template**
  下拉框立即同步刷新，无需重载页面，并保留其他未保存表单值。
- 增加 1–8 个有序订阅 URL 的聚合。支持 sing-box JSON、base64/明文 URI 列表，以及
  根级 inline `proxies` 的 Clash/Mihomo YAML；不读取或递归下载 `proxy-providers`。
- LuCI、生成器、更新器与 Go 网关使用一致的 URL 边界：拒绝空 hostname、原始 ASCII
  空白、已解析 URL 组件中的错误百分号转义等输入，同时保留合法大小写协议、userinfo、
  IPv6 与重复项顺序。
- 服务关闭时，手动 **Check/Update** 会临时使用已保存的订阅 URL，完成后恢复不含私密
  URL 的静态配置；升级不会覆盖用户编辑过的 `/etc/liquid-formula/config.yaml`。
- 采用失败策略 B：来源失败时只回退到与同一 URL 绑定的有效旧缓存；缺少缓存时整次
  更新失败，上一份完整配置继续生效。精确重复节点保留首项，同名不同内容节点稳定编号。
- LuCI 增加不含 URL、令牌或原始错误文本的安全订阅状态，显示 fresh/fallback、来源
  序号、格式、节点计数与固定失败分类。
- FakeHTTP/FakeSIP 自动模式使用 OpenWrt 官方 `network.sh` 解析默认 IPv4/IPv6 WAN
  的实际 L3 设备，覆盖 PPPoE、DHCP 与静态 WAN；手动接口设置始终优先。
- 保留此前完成的 Tuning 事务锁、原子写入、备份/恢复、回滚报告与卸载失败关闭修复。

附件模板 SHA-256：

```text
ac1648ec562b7d8e23407baeea758f3788889054f6c1bca56220534077c715c3  momo-template.json
9dc80b9caf2eba67ca4b542c480a10a3f88ef8082903a3c10f31a141b1b0fbdf  localdns-template.json
```

### 兼容边界

- 自动模式只跟随 OpenWrt 每个地址族的默认 WAN，不实现 mwan3 或多 WAN 策略；需要
  指定其他出口时使用手动模式。
- FakeHTTP/FakeSIP 的 NFQUEUE 仍为 `8970`/`8971`，FakeSIP 仍默认排除 UDP 53。
- 卸载本项目不会卸载 `momo` 或 `luci-app-momo`。
- `openwrt-feed/liquid-formula/src` 中的原转换器 Go 源码与 1.8.3 字节一致；新增订阅
  聚合器位于冻结目录之外。
- 自定义主题模板行为、只读 LuCI 控件及既有单主 WAN 范围不在本次变更中。

## English

### Main changes

- Updates the bundled `momo-template.json` and `localdns-template.json`. Both are enabled on a
  new installation, `momo_template` remains the default, and upgrades continue to preserve
  user-modified conffiles.
- Template uploads, saves, renames, enable-state changes, and deletions immediately refresh the
  table and **Default template** dropdown without reloading the page or discarding unrelated dirty
  form values.
- Adds aggregation for one to eight ordered subscription URLs. Accepted inputs are sing-box JSON,
  base64/plain URI lists, and Clash/Mihomo YAML with root-level inline `proxies`; the gateway never
  reads or recursively fetches `proxy-providers`.
- LuCI, the generator, updater, and Go gateway now share the same URL boundary: empty hostnames,
  raw ASCII whitespace, and malformed percent escapes in parsed URL components are rejected while
  valid case-insensitive schemes, userinfo, IPv6, and duplicate occurrence order remain intact.
- With the service disabled, manual **Check/Update** temporarily uses the saved source URLs and
  restores the private-URL-free at-rest configuration afterwards. Upgrades no longer overwrite a
  user-edited `/etc/liquid-formula/config.yaml`.
- Implements failure policy B: a failed source may use only a valid old cache bound to that exact
  URL. Without one, the complete attempt fails and the previous full configuration remains active.
  Exact duplicates keep their first occurrence; same-name nodes with different content receive
  stable numbering.
- Adds safe LuCI subscription status without URLs, tokens, or raw errors. It reports
  fresh/fallback state, source indices, formats, node counts, and fixed failure categories.
- FakeHTTP/FakeSIP auto mode uses OpenWrt's official `network.sh` helpers to resolve the actual L3
  device for the default IPv4/IPv6 WAN, including PPPoE, DHCP, and static WAN. Manual selection
  always takes precedence.
- Retains the completed Tuning fixes for transaction locking, atomic writes, backup/restore,
  explicit rollback reporting, and fail-closed uninstall restoration.

Supplied template SHA-256 checksums:

```text
ac1648ec562b7d8e23407baeea758f3788889054f6c1bca56220534077c715c3  momo-template.json
9dc80b9caf2eba67ca4b542c480a10a3f88ef8082903a3c10f31a141b1b0fbdf  localdns-template.json
```

### Compatibility boundaries

- Auto mode follows only OpenWrt's default WAN for each address family. It does not implement
  mwan3 or multi-WAN policy; use manual mode for a different egress.
- FakeHTTP/FakeSIP retain NFQUEUE `8970`/`8971`, and FakeSIP still excludes UDP 53 by default.
- Removing this project does not uninstall `momo` or `luci-app-momo`.
- The original converter Go source under `openwrt-feed/liquid-formula/src` is byte-identical to
  1.8.3; the new aggregation gateway lives outside the frozen tree.
- Theme-template customization, read-only LuCI controls, and the existing single-primary-WAN
  scope are unchanged.
