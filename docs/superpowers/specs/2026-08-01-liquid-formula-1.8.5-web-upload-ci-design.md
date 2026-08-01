# Liquid Formula 1.8.5 GitHub 网页上传 CI 修复设计

## 背景与已确认根因

GitHub Actions 运行 `30670121561` 在 `Tests` job 的 `Run source, shell, and converter tests` 步骤失败，OpenWrt SDK 构建尚未开始。失败提交为 `30732645f0ddb0028364b4091029186825594284`。

失败由两个只存在于线上 checkout 形态的假设触发：

1. workflow 使用 `actions/checkout@v7` 的默认 `fetch-depth: 1`，测试却通过 `git rev-parse` 和 `git diff` 读取 1.8.3 提交 `a60f59308847acc79d166794488149df6d08ad46`；浅克隆没有该对象。
2. 本次网页上传提交把 329 个 tracked files 全部记录为 `100755`。1.8.3 的 converter 源码文件均为 `100644`，因此相同内容的 Git tree ID 从 `d4f299087af3fdb87f7728846425d48edfeb7ae4` 变为 `54d4e99dcbb92c38b3131dd2b8612e4555fcdf50`。

线上提交与 1.8.3 基线间的 61 个 converter 源码文件均为 `0/0` 字节差异，只有 `100644 => 100755` mode 变化。故障属于 CI/交付兼容性问题，不是 Go 源码变化或编译错误。

## 目标

1. checkout 中所有普通文件均为 `0644` 或均为 `0755` 时，workflow 都恢复同一套审核过的目标权限。
2. 固定的 36 个运行脚本恢复为 `0755`，其他普通文件规范化为 `0644`。
3. converter 源码完整性通过仓库内离线 manifest 验证路径、目标 mode 与 SHA-256，不依赖历史 Git 对象或当前提交的 mode。
4. `test_source_package.sh` 与 `test_subscription_normalize.sh` 在单提交浅历史仓库中正常运行。
5. 保持 1.8.3→1.8.4 已冻结的 converter Go 源码逐字节不变。
6. 版本升级为 `1.8.5`，并生成完整源码包、1.8.4→1.8.5 增量包和精确文件清单。

## 非目标与不变约束

- 不修改 converter、subscription gateway、LuCI、DPI、procd、UCI 或模板运行逻辑。
- 不修改 `openwrt-feed/liquid-formula/src/**` 中任何文件的内容。
- 不通过 `fetch-depth: 0` 扩大 checkout；测试应自带完整离线证据。
- 不直接推送 GitHub、创建 PR 或改写远端提交。
- 继续支持用户从 GitHub 网页上传解压后的文件。
- 先前明确忽略的主题模板修改、只读 LuCI 控件、单主 WAN、明文凭据和 momo 卸载行为保持不变。

## 设计

### 1. 双向权限规范化

`.github/scripts/restore-executable-modes.sh` 保留现有 36 路径 executable allowlist，并继续先验证所有必需路径存在。验证成功后：

1. 使用 `find` 遍历仓库根目录下的普通文件，并按名称剪枝排除任何 `.git` 入口；
2. 将这些普通文件统一设为 `0644`；
3. 将 36 个 allowlist 路径设为 `0755`。

`find -type f` 不跟随符号链接；GitHub Actions 使用干净 checkout，因此不会扩大到仓库外或处理用户数据。缺失 allowlist 路径时仍在任何 mode 修改前失败。

### 2. 离线 converter 源码 manifest

新增 `tests/shell/fixtures/converter-source-1.8.3.manifest`，对 `openwrt-feed/liquid-formula/src` 的全部 61 个文件逐项记录：

```text
relative/path<TAB>100644<TAB>sha256
```

新增无副作用 helper `tests/shell/source_manifest.sh`，从指定目录生成同格式、按 `LC_ALL=C` 排序的 manifest。`test_source_package.sh` 比较实际 manifest 与冻结 fixture；`test_subscription_normalize.sh` 在 Go 测试前后分别比较，既验证初始内容，也验证构建未产生修改。

现有 `template-1.8.4.sha256` 中记录的 1.8.3 baseline commit 和原 tree ID 继续作为来源说明，但测试不再尝试从浅克隆解析该历史提交。

### 3. 回归场景

扩展 `test_web_upload_permissions.sh`，覆盖：

- 全 `0644` checkout：allowlist 变为 `0755`，普通文件保持 `0644`；
- 全 `0755` checkout：allowlist 保持 `0755`，普通文件变为 `0644`；
- 缺失必需路径：在修改任何 mode 前失败；
- 包含空格的仓库路径与默认当前目录调用；
- 一个只有当前提交、且提交中全部文件为 `100755` 的完整临时仓库：执行权限恢复后，真实 `test_source_package.sh` 必须通过。

最后一个场景同时证明完整性校验不再依赖 1.8.3 历史对象或 `HEAD` 的错误 mode。

## 版本、验证与交付

- 两份 OpenWrt package Makefile 升级到 `1.8.5`，`PKG_RELEASE` 保持 `1`。
- README 更新当前版本和网页上传权限说明；新增 1.8.5 release notes。
- 定向测试先完成 RED→GREEN，再串行运行全部 Shell suites、LuCI render、DPI、workflow contract、pipe lint、第三方校验及可用的 Go/C 检查。
- 以 1.8.4 提交 `4df16f07463ebc1f84154700abdde69c98aad35f` 为基线生成增量包；增量包的路径集必须等于 `git diff --name-status` 的新增/修改文件集，并明确验证无删除项。
- 完整包必须包含全部 tracked files、隐藏的 `.github`/`.gitattributes`/`.gitignore` 以及第三方源码归档，且不包含 `.git` 或临时产物。

## 验收标准

- 原线上失败的三条 source-package assertions 在全 `100755` 单提交 fixture 中通过。
- 两种网页上传 mode 极端均恢复为同一目标权限集合。
- source integrity 与 normalizer 测试中不再出现对 1.8.3 Git 对象的运行时依赖。
- 冻结 converter tree 的全部 61 个文件路径、mode 和 SHA-256 与离线 fixture 一致。
- `openwrt-feed/liquid-formula/src` 相对 1.8.4 的 Git tree ID 仍为 `d4f299087af3fdb87f7728846425d48edfeb7ae4`。
- 所有可执行本地回归为 0 失败；本地缺失工具链的项目必须明确标注，由下一次 GitHub Actions 完成验证。
