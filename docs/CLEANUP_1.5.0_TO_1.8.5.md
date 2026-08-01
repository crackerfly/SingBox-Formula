# Liquid Formula 1.5.0–1.8.5 手动残留清理清单

本清单用于设备曾安装并实际使用过 1.5.0–1.8.5，准备升级或已经升级到
`liquid-formula` 1.8.6 与 `luci-app-liquid-formula` 1.8.6，并希望手工清理旧版残留。
它分为“安装前保全”和“1.8.6 验证后清理”两个阶段，顺序不能颠倒。

它不是“复制后一次性执行”的删除脚本。路径必须逐项核对；尤其不要把表中的通配模式
直接交给未检查的递归删除命令。

本次审计有完整的 1.8.3、1.8.4、1.8.5 源码边界；1.5.0–1.8.2 的完整发布树无法在
当前存档中逐版复证，早期路径主要由 1.8.3 的迁移代码和历史命名记录佐证。因此本文
列出的是“已证实路径 + 明确标注的条件候选”，不能诚实声称覆盖未知私有构建产生的
每一个文件。

## 零、安装 1.8.6 前先保全旧数据

在覆盖安装 1.8.6 前，先备份并查看以下实际存在的旧路径。1.8.6 的单订阅迁移会
停止/停用旧 `singbox-formula` 服务，并保留它的旧命名空间供人工比较；更早安装过的
某些版本可能已经删除过其中一部分，缺失数据只能从升级前备份恢复。

- `/etc/config/singbox_formula`
- `/etc/config/singbox_formula.migrated`
- `/etc/singbox-formula/`
- `/var/log/singbox-formula/`
- `/var/lib/singbox-formula/`
- `/etc/init.d/singbox-formula`
- `/etc/taoistfuchen/fakehttp-payloads/`
- `/etc/init.d/taoistfuchen-boot-delay`

TaoistFuchen 的 DPI 路径采用不同语义：若新目录尚不存在，安装迁移会把
`/etc/taoistfuchen/fakehttp-payloads/` 移动到
`/etc/liquid-formula/fakehttp-payloads/`；旧 `/etc/init.d/taoistfuchen-boot-delay`
会被停止、停用并退休删除。因此这两个路径必须在覆盖安装前备份，不能依赖安装后仍
保留原位。

`/var/run/singbox-formula/` 是易失运行状态，只需记录和确认旧进程，不能把它当成唯一
备份来源。若 1.8.6 已经安装，仍应先备份上述尚存路径，再继续后面的比较和清理。

## 一、删除前必须满足的条件

1. 确认两个已安装包都显示为 `1.8.6-r1`。OpenWrt 24.10 可查看
   `opkg list-installed`，OpenWrt 25.12 可查看 `apk list --installed`。
2. 确认方案 A 迁移后的 `/etc/config/liquid_formula` 只剩一个标量
   `option subscription_url`，并备份已被舍弃的其他供应商地址（如仍需要）。
3. 以 mode 0600 备份以下实际存在的用户数据：
   - `/etc/config/liquid_formula`
   - `/etc/config/fakehttp`
   - `/etc/config/fakesip`
   - `/etc/config/customlogo`
   - `/etc/config/tuning`
   - `/etc/config/irqbalance`
   - `/etc/liquid-formula/config.yaml`
   - `/etc/liquid-formula/assets/`
   - `/etc/liquid-formula/fakehttp-payloads/`
   - `/www/liquid-formula/templates/`
   - 当前 `output_config` 指向的 JSON 及其 `.bak.*` 备份
   - `/etc/sysctl.d/99-liquid-formula.conf`、`/etc/sysctl.conf` 和
     `/etc/sysctl.conf.liquid-formula.bak`（若存在）
4. 在 LuCI 中让 1.8.6 至少成功执行一次 **Generate config.yaml**、
   **Check generated config** 和 **Update output file**，再确认 9716 健康端点正常。
5. 开始清理前先停止 `/etc/init.d/liquid-formula-boot-delay`，再停止 Liquid Formula、
   FakeHTTP 和 FakeSIP。确认进程列表中没有 `boot-delay-runner.sh`、`sb-sub-c`、
   `liquid-formula-subscription-gateway`、更新 worker、模板操作、上传操作或 Tuning
   操作；否则 scheduler 可能在停服后再次启动 FakeHTTP/FakeSIP。
6. 先用 `opkg files` 或 `apk info -L` 检查两个 1.8.6 包的文件清单。凡仍被当前包
   拥有的路径都不能手工删除。
7. 对每一个目标执行 `stat` 并确认它位于预期根目录、由 root 拥有，而且不是符号链接
   （symbolic link）。目标类型、所有者或真实路径不符合预期时立即停止。
8. 对仍存在的 `/var/run/liquid-formula/subscription.lock`，先用 `flock -n` 非阻塞取得
   并持续持有独占锁；在该 `subscription.lock` 持锁窗口内再验证和处理
   `subscription.lock.barrier` 标记。barrier 不是第二把 `flock` 锁，不能对它另做
   锁文件所有权推断。无法取得真正的 `subscription.lock` 时说明仍有使用者，不得
   unlink，否则旧进程与新进程可能分别持有两个同名锁实体。

## 二、1.8.4/1.8.5 聚合器专属残留

满足上述全部条件后，下列路径在 1.8.6 中不再使用，可逐项删除。

| 路径 | 类型/敏感性 | 说明 |
| --- | --- | --- |
| `/usr/bin/liquid-formula-subscription-gateway` | 程序文件 | 1.8.4/1.8.5 的 9717 聚合 gateway；1.8.6 不安装它。 |
| `/usr/share/liquid-formula/wait-subscription-gateway.sh` | 程序脚本 | gateway 等待包装器；1.8.6 直接运行 `sb-sub-c`。 |
| `/var/lib/liquid-formula/subscriptions/` | **高敏感目录** | 可能含标准化节点、服务器、密码、密钥、订阅 token 和 generation 状态；先隔离/备份，再安全清理。 |
| `/var/run/liquid-formula/subscription.lock` | 运行锁 | 仅在确认没有旧 gateway/worker，且非阻塞 `flock` 独占成功并保持持锁后删除。 |
| `/var/run/liquid-formula/subscription.lock.barrier` | barrier 标记 | 不是第二把 `flock` 锁；必须在真正的 `subscription.lock` 持锁窗口内验证并处理，不能仅凭一次进程列表快照判断。 |
| `/var/run/liquid-formula/.subscription.barrier.*` | 临时文件模式 | 只匹配该精确前缀；不要清空整个 `/var/run/liquid-formula/`。 |
| `/var/run/liquid-formula/health.*` | 临时文件模式 | 旧 gateway 健康探测临时文件。 |
| `/var/run/liquid-formula/content-type.*` | 临时文件模式 | 旧聚合拉取的响应头临时文件。 |
| `/tmp/liquid-formula-canonical-*` | 临时文件模式 | 旧 normalize/aggregate 临时文件；先确认没有仍在运行的命令。 |

`/var/run` 和 `/tmp` 通常会在重启后清空。最稳妥的做法是先重启并验证 1.8.6，再只
处理重启后仍存在的旧持久文件；不要为了清理这些临时项递归删除当前运行状态目录。

## 三、只在比较确认后删除的备份与旧命名空间

以下项目不是无条件垃圾。逐项查看内容，并在确认所需数据已迁移、当前包不拥有该路径
后再删除。

| 路径 | 删除条件 |
| --- | --- |
| `/etc/liquid-formula/config.yaml.pre-1.8.6` | 这是 gateway YAML 的一次性 mode 0600 备份，可能含完整订阅 URL/token。只有在 1.8.6 已长期验证且确定不再回退时才删除。 |
| `/etc/liquid-formula/.config.yaml.pre-1.8.6.*` | 迁移在同目录创建的隐藏 mode-0600 staging；断电/信号中断可能留下完整 gateway YAML。确认没有 `99-liquid-formula`/安装进程后，逐个核对所有者、mode、类型和内容，再安全处理。 |
| `/etc/config/singbox_formula` | 旧 UCI 命名；先与 `/etc/config/liquid_formula` 逐项比较。 |
| `/etc/config/singbox_formula.migrated` | 1.7.0 改名迁移留下的备份；比较完成后可删。 |
| `/etc/singbox-formula/` | 旧配置命名空间；先确认没有独有模板、密码或输出。 |
| `/var/log/singbox-formula/` | 旧日志；如需排障/审计先归档。 |
| `/var/lib/singbox-formula/` | 旧缓存/状态；检查是否含仍需恢复的数据。 |
| `/var/run/singbox-formula/` | 旧易失状态；确认没有旧进程后清理。 |
| `/etc/init.d/singbox-formula` | 旧 init 脚本；先 stop/disable，并确认当前包不拥有。 |
| `/etc/init.d/taoistfuchen-boot-delay` | 更早 DPI 版本的 init 脚本；先 stop/disable，并确认已由当前服务替代。 |
| `/etc/taoistfuchen/fakehttp-payloads/` | 逐文件与 `/etc/liquid-formula/fakehttp-payloads/` 比较；有独有 `.bin` payload 时先迁移，绝不能直接丢弃。 |
| `/var/log/liquid-formula/update.log.3` | 若是旧版遗留轮转日志，可在不需要历史排障后删除。 |

包管理器处理 conffile 冲突时还可能留下名称带 `apk-new`、`apk-old`、`opkg-new`、
`opkg-old` 或 `-opkg` 后缀的文件。只检查下列**基准路径 + 明确后缀**组合，例如
`/etc/config/liquid_formula.apk-new`；不要搜索或删除没有这些后缀的当前正式文件，也
不要按后缀跨 `/etc` 批量处理。

| 基准路径 | 只作为候选检查的后缀 |
| --- | --- |
| `/etc/config/liquid_formula` | `.apk-new`、`.apk-old`、`.opkg-new`、`.opkg-old`、`-opkg` |
| `/etc/config/fakehttp` | 同上 |
| `/etc/config/fakesip` | 同上 |
| `/etc/config/customlogo` | 同上 |
| `/etc/config/tuning` | 同上 |
| `/etc/config/irqbalance` | 同上 |
| `/etc/liquid-formula/config.yaml` | 同上 |
| `/www/liquid-formula/templates/<实际模板>.json` | 同上；逐个实际模板文件比较，不能用 `*.json*` 批量删除 |

## 四、必须保留：不是旧版垃圾

下列路径仍属于 1.8.6 的配置、用户数据、运行时或恢复机制。不要因为名称看起来旧而
删除，也不要整目录删除其父目录。

| 必须保留的路径 | 原因 |
| --- | --- |
| `/etc/config/liquid_formula` | 当前单订阅 UCI 配置。 |
| `/etc/config/fakehttp`、`/etc/config/fakesip` | 当前 DPI 配置。 |
| `/etc/config/customlogo`、`/etc/config/tuning` | 当前 LuCI Logo/Tuning 配置。 |
| `/etc/config/irqbalance` | Tuning 会修改并提交当前 irqbalance 开关，属于备份/恢复核对范围。 |
| `/etc/liquid-formula/config.yaml` | 当前转换器配置。 |
| `/etc/liquid-formula/assets/` | 当前 Logo/Favicon 用户资源。 |
| `/etc/liquid-formula/fakehttp-payloads/` | 当前 FakeHTTP payload。 |
| `/www/liquid-formula/templates/` | 当前内置和用户模板。 |
| `/var/lib/liquid-formula/cache/` | 当前转换器节点/模板缓存；不是聚合器的 `subscriptions` 目录。 |
| `/var/lib/liquid-formula/output/` | 允许的当前输出目录。 |
| `/etc/momo/profiles/` | momo 配置和 Liquid Formula 默认输出；不属于旧文件清理范围。 |
| `/etc/sing-box/` | 允许的用户输出目录；可能被其他 sing-box 组件使用。 |
| 当前输出 JSON 的 `.bak.*` | 1.8.6 主动保留的最近五份输出备份，不是版本升级垃圾。 |
| `/etc/sysctl.d/99-liquid-formula.conf` | 当前 Tuning 持久化配置。 |
| `/etc/sysctl.conf.liquid-formula.bak` | Tuning 恢复原配置所需；只能由 Tuning restore/disable 流程退休。 |
| `/usr/share/liquid-formula-dpi/` | 名称虽带 `dpi`，仍是 1.8.6 FakeHTTP/FakeSIP 的当前共享运行库。 |
| `/etc/init.d/liquid-formula-boot-delay` | 仍是当前 DPI 服务启动协调组件。 |
| `/var/run/liquid-formula-boot-delay/` | 当前 scheduler 的 state、lock、link 和 WAN device cache；不是旧命名空间。 |
| `/var/run/liquid-formula-upload/` | 当前上传事务的 lock、pending、candidate/reply staging；不能整目录当残留删除。 |
| `/usr/bin/sb-sub-c`、`/usr/bin/fakehttp`、`/usr/bin/fakesip` | 1.8.6 当前程序，由包管理器管理。 |
| `/var/run/liquid-formula/boot-delay.done` | 当前单订阅开机延迟标记，会由运行时管理。 |

同样不要手工删除当前操作使用的 `/var/run/liquid-formula/update.lock`、
`rpc-action.lock`、`lifecycle.lock`、`template.lock`、`/var/lock/liquid-formula-generate.lock`
或 `/var/lock/liquid-formula-tuning.lock`。若它们疑似卡死，应先确认所有者进程身份并按
对应服务的恢复流程处理，而不是把它们当成旧版本残留。

## 五、推荐清理顺序

1. 完成备份和 1.8.6 的 Generate/Check/Update 验证。
2. 先停止 `liquid-formula-boot-delay`，再停止三个相关服务，确认 scheduler runner、旧
   gateway 和任何写操作均不存在。
3. 先隔离 `/var/lib/liquid-formula/subscriptions/`；它是最敏感、也最有可能需要回看
   的目录。
4. 删除两个明确的旧 gateway 程序文件。
5. 对真正的 `subscription.lock` 非阻塞独占 `flock` 成功并持续持锁后，在同一窗口内
   处理 barrier 标记和 gateway 临时文件；优先让一次重启自然清理易失状态。
6. 比较并处理旧 `singbox_formula`、`taoistfuchen` 和包管理器冲突备份。
7. 重新启动 Liquid Formula/FakeHTTP/FakeSIP，复查 9716、模板、输出文件、WAN
   解析和 Tuning 状态。
8. 正常运行一段时间并确认无需回退后，再决定是否删除
   `/etc/liquid-formula/config.yaml.pre-1.8.6` 和隔离的敏感订阅状态。

源码仓库中的 `openwrt-feed/liquid-formula/src-subscription-gateway/` 属于 1.8.4/1.8.5
源码树残留，不是路由器安装路径；1.8.6 完整源码包本身不应包含该目录。
