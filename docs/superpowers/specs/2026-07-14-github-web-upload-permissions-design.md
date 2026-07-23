# GitHub 网页上传执行权限修复设计

## 背景与根因

GitHub Actions 运行 `29314879258` 在 “Run source, shell, and converter tests” 步骤中失败，首个有效错误是：

```text
generate-config.sh: Permission denied
```

对应提交中所有文件的 Git mode 都是 `100644`。这是通过 GitHub 网页逐个上传文件时丢失 Unix executable bit 的结果；后续 30 个测试失败是该首个权限错误的连锁反应，不是 30 个独立缺陷。

本修复必须让只使用 GitHub 网页上传的源码也能在 Actions 中完成测试和 OpenWrt SDK 编译。它不改变插件运行逻辑，也不改变以下刻意保留的行为：

- converter 实际监听 `:<port>`，而不是 `127.0.0.1:<port>`；
- 默认密码仍为 `890716`；
- 日志仍可包含密码、完整订阅 URL、订阅令牌和缓存随机参数。

## 方案

新增 `.github/scripts/restore-executable-modes.sh`，并在 checkout 后立即使用下面的形式调用：

```sh
sh .github/scripts/restore-executable-modes.sh "$GITHUB_WORKSPACE"
```

显式通过 `sh` 调用意味着修复脚本自身即使被网页上传成 `0644`，仍然可以运行。脚本只恢复项目中已知应当可执行的文件，不使用宽泛的 `find ... chmod`，避免把 JSON、Lua、JavaScript 或配置文件误设为可执行。

恢复清单固定为当前 Git tree 中原本标记为 `100755` 的 13 个路径：

- `openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula`；
- `openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula`；
- `openwrt-feed/singbox-formula/files/etc/uci-defaults/99-singbox-formula`；
- `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh`；
- `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/run-delayed.sh`；
- `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/update.sh`；
- `openwrt-feed/singbox-formula/files/usr/share/singbox-formula/validate-template.sh`；
- `tests/shell/test_generate_config.sh`；
- `tests/shell/test_migration.sh`；
- `tests/shell/test_procd_service.sh`；
- `tests/shell/test_rpc_contract.sh`；
- `tests/shell/test_template_transactions.sh`；
- `tests/shell/test_update.sh`。

修复脚本和原本为 `100644` 的 `test_source_package.sh` 继续通过 `sh` 调用，不把它们加入恢复清单。

脚本接收仓库根目录作为可选的第一个参数，未提供时使用当前目录。对清单中的每个路径，它先验证普通文件存在，再设置为 `0755`。若任一路径缺失，脚本输出包含该相对路径的明确错误并返回非零，防止源码布局变化后静默产出权限不完整的安装包。

## 构建数据流

构建顺序为：

1. `actions/checkout` 取回网页上传形成的全 `100644` 文件树；
2. workflow 通过 `sh` 运行权限恢复脚本；
3. shell、Node.js、Go 测试在已恢复的文件树中运行；
4. package source 复制进 OpenWrt SDK，目标包因此继承正确的运行时文件权限；
5. SDK 正常生成 `singbox-formula` 与 `luci-app-singbox-formula` APK。

该步骤必须位于 checkout 后、任何测试或 SDK source copy 前。这样既修复测试直接执行 helper 的失败，也修复最终 APK 中 init/rpcd/helper 文件可能不可执行的问题。

## 回归测试

新增 `tests/shell/test_web_upload_permissions.sh`。workflow 已用 `sh tests/shell/test_*.sh` 运行测试，因此该测试文件自身无需预先具备 executable bit。

测试在临时目录中构造仓库布局，把恢复清单内的每个文件都强制设为 `0644`，再通过 `sh` 调用实际修复脚本，并验证：

- 所有清单文件均变为可执行；
- 删除一个必需文件时脚本返回非零；
- 缺失文件错误包含准确的相对路径；
- 清单外的普通文件保持 `0644`。

实现遵循测试先行：先提交并运行回归测试，确认它因修复脚本尚不存在而失败；再加入脚本和 workflow 调用并确认测试转绿。最终还要运行全部 shell 测试、Node.js 语法检查、Go race test 和 `go vet`。

## 文档与交付

README 的构建说明增加一条简短说明：GitHub 网页上传会丢失 executable bit，但 workflow 会在构建开始时按固定清单恢复。

GitHub 浏览器上传器每次最多接受 100 个文件，而仓库保留全部 110 个 tracked files，因此网页上传必须严格分为两轮：

1. Batch 1 为 `singbox-formula-1.5.0-web-upload-batch-1-openwrt-feed.zip`，wrapper prefix 是
   `SingBox-Formula-1.5.0-web-upload-batch-1/`，只包含 `openwrt-feed/` 下的 88 个 tracked files；
2. Batch 2 为 `singbox-formula-1.5.0-web-upload-batch-2-repository.zip`，wrapper prefix 是
   `SingBox-Formula-1.5.0-web-upload-batch-2/`，包含 `openwrt-feed/` 外的 22 个 tracked files；
   它的六个顶层入口是 `.github`、`.gitignore`、`README.md`、`docs`、`momo-template.json` 和
   `tests`。

同时生成 `singbox-formula-1.5.0-complete-source.zip`，prefix 为 `SingBox-Formula-1.5.0/`，包含
全部 110 个 tracked files。完整包只用于源码完整性和参考，不是单轮网页上传包。

用户在本地解压两个 batch ZIP，不能直接上传 ZIP 文件。在仓库根目录使用
**Add file → Upload files**：第一轮打开 Batch 1 wrapper 并拖入其中的 `openwrt-feed` 目录后提交；
第二轮打开 Batch 2 wrapper 并一次选中、拖入六个顶层入口后提交。选择第二批前必须显示隐藏文件，
确保 `.github` 和 `.gitignore` 被选中；macOS Finder 使用 `Command+Shift+.`，Windows Explorer 使用
**View → Show → Hidden items**。不能拖入任何 wrapper 目录本身，否则仓库会多出错误的目录层级。
第二次提交后的仓库状态才是权威结果，两批之间出现的 Actions 中间结果可以忽略。

两批文件并集必须与 HEAD 的 110 个 tracked paths 完全相等，交集必须为空，且每批均不超过 100
个文件。即使网页端再次把 mode 归一为 `100644`，第二批上传的 workflow 也能在完整仓库上自举
恢复。此修改只修复 CI/打包入口和交付说明，不升级插件版本，也不触及 converter 配置、监听地址、
密码或日志策略。

## 验收标准

- 从一个所有文件均为 `100644` 的 GitHub checkout 开始，权限恢复测试通过；
- 原失败步骤不再出现 `generate-config.sh: Permission denied` 或 exit 126；
- workflow 在任何测试与 SDK 构建前完成精确权限恢复；
- OpenWrt 25.12.5、`mediatek/mt7622` 的现有构建参数不变；
- 用户列出的三项刻意行为没有源码变化；
- 交付一个 110 文件完整参考包，以及分别为 88 文件和 22 文件的两个网页上传 batch ZIP；
- 两批均不超过浏览器的 100 文件限制，路径交集为空，排序后的路径并集与 HEAD 完全一致；
- README 明确要求分两轮从 wrapper 内拖入内容、显示隐藏文件，并以第二次提交后的结果为准。
