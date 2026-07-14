# SingBox Formula 1.5.0 完整修复设计

## 目标

将附件中的 `singbox-formula 1.4.0` 升级为面向 OpenWrt 25.12.5、Linksys E8450（`mediatek/mt7622`、AArch64 Cortex-A53）的 `1.5.0` 源码构建版本。修复已确认的服务生命周期、缓存一致性、并发、procd、RPC、LuCI、打包和升级问题，并为核心行为建立自动化回归测试。

## 明确保留的行为

以下行为由用户确认是刻意设计，1.5.0 必须保留，并用回归测试防止被误改：

1. HTTP 服务继续监听 `:<port>`，即所有接口；不能改为 `127.0.0.1:<port>`。
2. 默认密码继续是 `890716`；首次安装不生成随机密码。
3. 请求密码、鉴权 expected/got、完整订阅 URL、实际带 cache-buster 的拉取 URL 继续写入现有日志；不做脱敏。

这些行为具有安全风险，但不属于本次修复范围。其他 ACL、路径、事务和并发问题仍需修复。

## 源码与构建

- 上游基线固定为 `haierkeys/singbox-subscribe-convert` 提交 `8222509aff98229886d304ef72e1d0affb087a62`（tag `0.7.2`）。
- 将该提交的完整源码和许可证放入 `openwrt-feed/singbox-formula/src/`，并记录来源提交；删除对 `files/usr/bin/sb-sub-c` 预编译 ELF 的依赖。
- OpenWrt 包使用 25.12 packages feed 的 `golang-package.mk` 从内置源码构建，产物安装为 `/usr/bin/sb-sub-c`。
- 包版本为 `1.5.0-r1`，目标限制为 `TARGET_mediatek_mt7622`。
- 许可证元数据准确反映 Apache-2.0 上游与带 GPL-3.0-or-later 头的文件，并随源码保留相应文本；不得继续仅声明 Apache-2.0。
- GitHub Actions 固定 OpenWrt `25.12.5` 和 `mediatek/mt7622`，所有第三方 action 使用完整提交 SHA；构建前运行 shell、JS、JSON、Go 单元测试与 `go test -race ./...`，构建后验证 APK 架构及内部 ELF 为 AArch64。

## 转换器核心生命周期

### 同步监听和真实启动结果

- `NewServer` 返回成功前必须同步执行 `net.Listen("tcp", fmt.Sprintf(":%d", port))`。
- 只有 listener 成功后才启动 HTTP serve、cache watcher 和 auto-update；只有这些步骤完成后才记录“Server is running”。
- bind 失败必须返回错误、取消该实例 context、等待已启动 goroutine 退出，并使进程返回非零；不能留下“进程存在但无监听”的实例。
- 地址构造必须保持 `":9716"` 形式。

### 配置热加载

- 配置 watcher 不再永久持有首次 `*Server`；当前实例由单一 supervisor 串行拥有。
- reload 必须：解析并验证候选配置；停止并等待当前实例完全关闭；启动候选实例；成功后替换 current 指针。
- 删除固定 `sleep 2s`。关闭流程必须调用 context cancel、HTTP shutdown、watcher/ticker stop，并等待完成。
- reload 失败不能记录成功；若候选配置无效，旧实例继续运行。若旧实例已因端口变更而停止而候选 bind 失败，错误必须向根进程传播，由 procd 的有限 respawn 恢复，不能伪装成成功。
- 连续至少三次同端口 reload 不得出现 `EADDRINUSE`，且任意时刻只能有一个 cache watcher 和一个 auto-update ticker。

### 刷新协调和缓存

- 手动 `/refresh`、`?refresh=1`、自动更新和初始拉取共用一个进程内互斥协调器；实际写缓存的最大并发数为 1。
- 手动刷新不再先清空内存或删除正式缓存。下载、解析或模板编译失败时，内存和磁盘均继续使用 last-known-good 数据。
- 节点响应上限为 32 MiB；单模板响应上限为 8 MiB。超过上限立即失败，不能继续读入内存或覆盖缓存。
- 下载写入与正式文件同目录的临时文件；完成后 `Sync`、关闭、验证，再原子 rename。失败删除临时文件。
- 节点数据必须能按现有节点格式解析；模板必须能由 Pongo2 编译。只有验证成功才提交。
- cache watcher 同时处理 `Write`、`Create` 和 `Rename`，并对同一目标防抖；context 取消后必须关闭。
- HTTP refresh 的 server write timeout 必须大于配置的 subscription timeout；UCI 允许 subscription timeout `5..600` 秒，生成值为 `subscription_timeout + 60` 秒，客户端 curl max-time 使用同一预算。

### 日志和健康检查

- Zap 的 info/debug 输出到 stdout，error 输出到 stderr，避免 OpenWrt 把所有 INFO 标为 `daemon.err`；文件日志仍接收所有配置等级。
- `logging.max_size`、`max_backups`、`max_age` 必须实际生效，不能只出现在 YAML；不新增 logrotate 服务依赖。
- `/health` 增加稳定身份字段 `service: "singbox-subscribe-convert"`、`version` 和 `status`；OpenWrt wrapper 只有在身份和状态都匹配时才判健康。
- 敏感日志保留要求见“明确保留的行为”。

## OpenWrt shell 与 procd

### 配置生成

- `generate-config.sh` 使用 `umask 077`、同目录 `mktemp` 和 trap 清理。
- port 必须为 `1..65535`；subscription timeout 为 `5..600`；refresh interval 为 `1..10080` 分钟；boot delay 为 `0..600` 秒。
- subscription URL 仅接受 `http://` 或 `https://`；template base URL 仅接受本机 `http://127.0.0.1` 或 `http://localhost` 前缀。
- default template 必须存在且 enabled。
- output path 必须为绝对 `.json` 文件，并位于 `/etc/momo/profiles/`、`/etc/sing-box/` 或 `/var/lib/singbox-formula/output/`。
- 内容与现有 `config.yaml` 相同时使用 `cmp -s` 后删除临时文件，不替换 inode、不改变 mtime；内容变化时才 `chmod 0600` 并原子 rename。
- 空 YAML 字符串必须正确输出为 `''`。

### 更新脚本

- `refresh` 不调用配置生成器；`check` 和 `apply` 可调用，但幂等生成不得触发无变化 reload。
- 所有入口使用原子目录锁，直接 CLI 和 RPC 并发时也只能有一个更新任务；锁记录 owner PID/token，并可回收已死 owner。
- 每次操作使用唯一临时目录并通过 trap 清理；不能共用固定 `refresh.json`、`generated.json` 或 `$output_config.new`。
- `apply` 使用 BusyBox `cp`、`chmod 0600`、同目录 `mv`，不使用默认不存在的 `install`。
- JSON 输出始终用 `jsonfilter` 验证；如存在 sing-box，再额外执行 `sing-box check`。
- update.log 使用 0600 权限，达到 256 KiB 时轮转，保留 `.1` 和 `.2`。
- 仍保留最多 5 个输出配置备份。

### procd

- boot delay 由 procd 监管的 helper 完成；延迟期间 stop/disable 必须取消，睡醒前重新读取 UCI enabled；手动 start 不延迟。
- instance 固定名为 `main`；boot respawn 不重复等待同一轮 boot delay。
- respawn 有限：threshold 30 秒、timeout 5 秒、retry 5；`term_timeout` 为 5 秒。
- `start_service` 接受 `boot`、`manual` 和默认 reconcile 模式。boot/default 以磁盘 UCI enabled 为准；manual 允许按需启动。
- 默认 reload 重新生成配置并通过配置 digest 环境变量更新 procd instance；disabled 时不注册实例，从而停止服务；enabled 且内容变化时由 procd 原子替换实例。

## RPC 与 LuCI

### 权限和方法边界

- 删除 `rpcd-mod-file` 依赖及 ACL 中所有通用 `file.read/write`；模板只能经专用 RPC 访问。
- 将混合 `action` 拆为 `service_action`、`generate`、`refresh`、`check`、`update` 和只读 `status`；ACL 按实际能力授权。
- 不再提供未使用的 enable/disable/log action；服务开关通过 UCI Save & Apply 和 reconcile 处理。

### 后台任务

- 状态目录为 root-owned `/var/run/singbox-formula`，mode 0700；拒绝 symlink、错误 owner 或非目录。
- RPC 使用原子 mkdir 锁和 owner token；状态文件先写临时文件再 rename。
- 同时发起 20 个后台任务时只能有一个进入 updater，其余返回明确 busy。
- 后台 updater 使用 900 秒硬超时；异常退出、PID 复用或旧 worker 结束不得删除新 owner 的锁。
- action state 和 code 必须枚举、严格解析；损坏内容 fail closed。

### 服务状态和动作结果

- stop/restart 等待目标 procd instance，而不是 `pgrep -f` 全局匹配；任何 wait、health、UCI、init 操作失败都返回非零。
- status 的 port 始终输出合法 JSON number；非法、leading-zero、越界值回退并标记配置错误。
- converter version 探测失败显示 `unknown`，不能伪报 `0.7.2`。
- health 只有匹配转换器身份 schema 才为 true。
- 配置协调使用内容 digest，不再使用秒级 mtime；首次创建、同秒修改、等长修改都可区分。
- 前端对缺失 `code`、错误类型、异步非 `queued`、RPC 失败全部 fail closed；旧状态必须显示 stale/unavailable。

### 模板事务

- ID 只接受 `[A-Za-z0-9_]+`；文件名只接受 `[A-Za-z0-9._-]+\.json`，非法值拒绝而非静默改写。
- 单模板正文最大 1 MiB；前端和 RPC 均执行边界检查。
- 已有模板的 ID 和文件名在编辑模式中不可修改；避免隐式 rename 和孤儿 section/file。
- 文件名在全部模板中唯一；不同 ID 不能共享同一文件。
- 当前 default template 不能删除或禁用；default 下拉只显示 enabled 模板。
- 写删使用独立原子锁、非 webroot 临时文件、UCI/文件备份和 rollback。持久化失败只能留下完整 old 或完整 new。
- generate 失败返回 `ok:false,persisted:true,phase:"generate"` 且不 restart；restart 失败返回 `phase:"restart"`，不能误报完全成功。
- list/read 对 UCI 中非 canonical 文件名 fail closed，不能通过 `../../` 探测其他文件元数据。

## 包升级、迁移和 UI

- `/www/singbox-formula/templates/momo-template.json` 加入 conffiles，升级保留用户编辑。
- uci-defaults 只填补缺失选项；不得把显式 port `9000` 或显式旧 output path 当默认值覆盖。
- luci package postinst 不重启 uhttpd，不 chmod 不存在的别名；rpcd 注册失败不得静默声称成功。
- UI 显示的“Local converted URL”文案改为“Converter URL”，因为服务实际监听所有接口；展示值仍可使用 127.0.0.1 供本机集成。
- README 删除 `--allow-untrusted` 建议，补充源码构建、目标限制、升级、回滚和测试说明。

## 验证与验收

1. 所有新增行为先有失败测试，再写实现。
2. 本地执行 shell harness、Node 测试、JSON/JS/shell syntax、Go unit/integration tests 和 `go test -race ./...`。
3. Go 集成测试覆盖连续三次 reload、bind 已占用、刷新并发、超限下载、失败保留 last-known-good、关闭后 watcher/ticker 归零。
4. shell/RPC 测试使用 mock UCI/procd/curl，覆盖幂等生成、refresh 不触发 config write、原子锁、超时、模板事务、配置校验和严格错误返回。
5. GitHub Actions 使用 OpenWrt 25.12.5 SDK 构建两个 APK；检查主包内部 `/usr/bin/sb-sub-c` 是 AArch64，并生成 SHA256SUMS。
6. 交付前比较源码 ZIP 内容，确保无旧预编译 ELF、无测试临时文件、无凭据样本、无 `.git` 和 worktree 元数据。
7. 路由器回归清单包含 install、upgrade、reboot、boot-delay cancel、start/stop/restart、连续 refresh/check/apply、端口占用和模板升级保留。

