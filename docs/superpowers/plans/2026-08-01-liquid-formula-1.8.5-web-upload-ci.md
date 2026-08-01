# Liquid Formula 1.8.5 Web Upload CI Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 GitHub 网页上传 checkout 的工作树权限可恢复，同时要求 Git 索引中的全部跟踪文件保持 `100644`；错误的全 `100755` 提交应列出违规文件并明确失败。发布时保持冻结 converter Go 源码不变，并合并 subscription gateway 的跨架构 `Nlink` 类型兼容修正。

**Architecture:** 权限脚本先验证 36 路径 allowlist，再把 `.git` 外全部普通文件规范化为 `0644`，最后恢复 allowlist 为 `0755`。converter 源码完整性改用仓库内的全量路径/mode/SHA-256 manifest，`test_source_package.sh` 与 `test_subscription_normalize.sh` 共享一个 POSIX helper，不再读取 1.8.3 Git 对象。

**Tech Stack:** POSIX shell、Git、GitHub Actions YAML、现有 TAP shell harness、SHA-256 manifest、ZIP。

## Global Constraints

- 版本从 `1.8.4` 升级到 `1.8.5`，两包 `PKG_RELEASE` 保持 `1`。
- `openwrt-feed/liquid-formula/src/**` 的文件内容和 1.8.4 tree ID `d4f299087af3fdb87f7728846425d48edfeb7ae4` 必须保持不变。
- 不修改 converter、LuCI、DPI、procd、UCI、模板或 momo 卸载运行逻辑；subscription gateway 仅允许 Task 5 明确列出的两个 `Nlink` 类型转换。
- workflow 保持浅克隆；不能用 `fetch-depth: 0` 掩盖测试的历史依赖。
- 继续支持 GitHub 网页上传；不能要求用户使用 Git CLI、API push 或 PR。
- Task 1–4 的首轮实现遵循 RED→GREEN 并串行验证；Task 5 按用户明确要求不运行测试或编译，只做静态一致性与交付包反向核对。
- 不直接修改远端仓库。

---

## File Structure

- Modify: `.github/scripts/restore-executable-modes.sh` — 双向规范化 checkout mode。
- Modify: `tests/shell/test_web_upload_permissions.sh` — 覆盖工作树权限恢复，并验证错误的全 `100755` Git 索引会被明确拒绝。
- Create: `tests/shell/source_manifest.sh` — 生成确定性的全量源码 manifest。
- Create: `tests/shell/fixtures/converter-source-1.8.3.manifest` — 冻结61个 converter 源码文件的路径、mode 与 SHA-256。
- Modify: `tests/shell/test_source_package.sh` — 以离线 manifest 替换历史 tree lookup，并校验 1.8.5 版本。
- Modify: `tests/shell/test_subscription_normalize.sh` — 构建前后以离线 manifest 验证冻结源码。
- Modify: `openwrt-feed/liquid-formula/Makefile` — 主包版本 1.8.5。
- Modify: `openwrt-feed/luci-app-liquid-formula/Makefile` — LuCI 包版本 1.8.5。
- Modify: `README.md` — 版本、摘要和双向权限恢复说明。
- Create: `docs/RELEASE_NOTES_1.8.5.md` — 中英文修复说明与验证边界。
- Existing: `docs/superpowers/specs/2026-08-01-liquid-formula-1.8.5-web-upload-ci-design.md` — 已批准设计。
- Create: `docs/superpowers/plans/2026-08-01-liquid-formula-1.8.5-web-upload-ci.md` — 本计划。

> **历史说明：** Task 1–4 记录首轮 1.8.5 实施过程；Task 5 定义最终的 Git mode 与验证规则。两者冲突时以 Task 5 为准。

### Task 1: 用真实网页上传形态建立 RED 回归

**Files:**

- Modify: `tests/shell/test_web_upload_permissions.sh`

**Interfaces:**

- Consumes: `sh .github/scripts/restore-executable-modes.sh [repo-root]`。
- Produces: 一个全 `0755` 工作树的权限恢复断言，以及一个只有当前提交、Git 索引全为 `100755` 的完整临时仓库拒绝断言。

- [ ] **Step 1: 为全 `0755` fixture 增加失败断言**

扩展 fixture builder，使其接收初始 mode；新增场景把 allowlist 和 `docs/ordinary.txt` 均设为 `0755`。运行真实 restore 脚本后，手工断言普通文件必须为字面值 `644`，allowlist 文件必须为 `755`。

- [ ] **Step 2: 增加单提交完整仓库集成断言**

在 `TEST_TMP` 下复制当前 working tree、移除复制品中的 `.git`、把所有普通文件设为 `0755`，然后执行：

```sh
git -C "$FULL_WEB_TREE" init
git -C "$FULL_WEB_TREE" config user.name 'Liquid Formula CI Test'
git -C "$FULL_WEB_TREE" config user.email 'ci-test@invalid.example'
git -C "$FULL_WEB_TREE" add -A
git -C "$FULL_WEB_TREE" commit -m 'web upload fixture'
sh "$FULL_WEB_TREE/.github/scripts/restore-executable-modes.sh" "$FULL_WEB_TREE"
sh "$FULL_WEB_TREE/tests/shell/test_source_package.sh"
```

首轮实现原计划断言最后一个命令成功。Task 5 已将最终预期改为：权限恢复脚本修复工作树后，源码包校验仍应明确拒绝 `HEAD` 中全部文件为 `100755` 的错误提交，并列出违规路径。

- [ ] **Step 3: 运行 RED 并核对失败原因**

Run:

```sh
sh tests/shell/test_web_upload_permissions.sh
```

首轮 RED 的 Expected 为 exit `1`：全 `0755` fixture 的普通文件仍为 `755`，完整临时仓库中的 `test_source_package.sh` 报 baseline/tree/mode 三类既知失败。Task 5 的最终实现保留工作树权限恢复，但将 Git 索引 mode 违规改为稳定、可读的预期失败。

- [ ] **Step 4: 提交 RED 测试**

```sh
git add tests/shell/test_web_upload_permissions.sh
git commit -m "test: reproduce all-executable web upload checkout"
```

### Task 2: 实现双向权限恢复和离线源码完整性

**Files:**

- Modify: `.github/scripts/restore-executable-modes.sh`
- Create: `tests/shell/source_manifest.sh`
- Create: `tests/shell/fixtures/converter-source-1.8.3.manifest`
- Modify: `tests/shell/test_source_package.sh`
- Modify: `tests/shell/test_subscription_normalize.sh`
- Test: `tests/shell/test_web_upload_permissions.sh`

**Interfaces:**

- Produces: `write_source_manifest SOURCE_ROOT OUTPUT_FILE`，输出 `relative-path<TAB>git-mode<TAB>sha256` 的 C-locale 排序清单。
- Consumes: `converter-source-1.8.3.manifest`，固定61个文件且全部目标 mode 为 `100644`。

- [ ] **Step 1: 最小实现双向 mode 规范化**

在现有 allowlist 全量验证完成后、恢复 `0755` 前加入：

```sh
find "$REPO_ROOT" \
  -name .git -prune -o \
  -type f -exec chmod 0644 {} +
```

保留随后对36个 allowlist 路径的 `chmod 0755`。任何 `find`/`chmod` 失败都由现有 `set -eu` 传播。

- [ ] **Step 2: 新增真实行为 helper**

`tests/shell/source_manifest.sh` 定义 `write_source_manifest`：遍历指定 root 的所有普通文件；将 `644` 映射为 `100644`、`755` 映射为 `100755`，其他 mode 记录为 `unsupported-<mode>`；记录 SHA-256 并按相对路径排序。helper 不写入 source root，也不调用 Git。

- [ ] **Step 3: 从冻结 1.8.4 working tree 生成 fixture**

Run:

```sh
. tests/shell/source_manifest.sh
write_source_manifest \
  openwrt-feed/liquid-formula/src \
  tests/shell/fixtures/converter-source-1.8.3.manifest
```

Expected: 61 行；所有 mode 列均为 `100644`；路径集等于 `find openwrt-feed/liquid-formula/src -type f` 的排序结果。

- [ ] **Step 4: 替换 source-package 的 Git 历史断言**

在 `test_source_package.sh` source helper 并设置 manifest 路径。删除对 baseline commit 与 `HEAD:.../src` 的 `git rev-parse` 动态解析；生成实际 manifest，然后使用 `assert_files_equal` 与 fixture 比较。保留 template fixture 中 baseline commit/tree ID 的来源记录断言。

- [ ] **Step 5: 替换 normalizer 的 Git 历史断言**

在 `test_subscription_normalize.sh` source helper。删除运行时 `git rev-parse` 和两处 `git diff "$BASELINE_183_COMMIT"`；Go 测试前后分别生成 manifest，并断言：

```text
frozen fixture == before manifest
before manifest == after manifest
frozen fixture == after manifest
```

- [ ] **Step 6: 运行 GREEN 与篡改反证**

Run:

```sh
sh -n .github/scripts/restore-executable-modes.sh
sh -n tests/shell/source_manifest.sh
sh tests/shell/test_source_package.sh
sh tests/shell/test_web_upload_permissions.sh
```

Expected: 全部 exit `0`。随后在临时 source 副本中分别改一个字节、删除一个文件、把一个文件设为 `0755`，每种情况均必须让 manifest 比较失败。

- [ ] **Step 7: 提交实现**

```sh
git add .github/scripts/restore-executable-modes.sh \
  tests/shell/source_manifest.sh \
  tests/shell/fixtures/converter-source-1.8.3.manifest \
  tests/shell/test_source_package.sh \
  tests/shell/test_subscription_normalize.sh
git commit -m "ci: make web upload integrity checks history independent"
```

### Task 3: 升级到 1.8.5 并同步双语文档

**Files:**

- Modify: `openwrt-feed/liquid-formula/Makefile`
- Modify: `openwrt-feed/luci-app-liquid-formula/Makefile`
- Modify: `tests/shell/test_source_package.sh`
- Modify: `README.md`
- Create: `docs/RELEASE_NOTES_1.8.5.md`

- [ ] **Step 1: 先让版本测试期待 1.8.5 并确认 RED**

把 `test_source_package.sh` 的两条 package version 断言从 `1.8.4` 改为 `1.8.5`，运行测试，确认只因两份 Makefile 仍为 `1.8.4` 而失败。

- [ ] **Step 2: 最小升级两份 package Makefile**

把两份 `PKG_VERSION:=1.8.4` 改为 `PKG_VERSION:=1.8.5`，`PKG_RELEASE:=1` 不变；重新运行 `test_source_package.sh` 并确认 GREEN。

- [ ] **Step 3: 更新 README 和 release notes**

README 中英文部分均说明：1.8.5 修复全 `0755` 网页上传与浅克隆历史依赖；权限脚本现在把普通文件规范化为 `0644` 后恢复36个 `0755` allowlist；converter Go 源码保持不变。上传 wrapper 名改为 `Liquid-Formula-1.8.5`。

- [ ] **Step 4: 提交版本与文档**

```sh
git add README.md docs/RELEASE_NOTES_1.8.5.md \
  openwrt-feed/liquid-formula/Makefile \
  openwrt-feed/luci-app-liquid-formula/Makefile \
  tests/shell/test_source_package.sh
git commit -m "release: prepare Liquid Formula 1.8.5"
```

### Task 4: 全量验证、独立复核和交付包

**Files:**

- Verify: all tracked changes since `4df16f07463ebc1f84154700abdde69c98aad35f`
- Create outside Git: `Liquid-Formula-1.8.5.zip`
- Create outside Git: `Liquid-Formula-1.8.4-to-1.8.5-update-files.zip`
- Create outside Git: `Liquid-Formula-1.8.4-to-1.8.5-files.txt`

- [ ] **Step 1: 串行运行全部可用验证**

依次运行12组 `tests/shell/test_*.sh`（normalizer 使用现有 Go 条件）、`node --check` 全部 LuCI views、`node tests/shell/test_view_render.js`、`sudo -E tests/dpi/run.sh`、`.github/tests/test-release-workflow.sh`、workflow pipe lint、第三方 SHA-256、shell syntax、`git diff --check`。本机没有 Go/NFQUEUE headers 时明确记录跳过，不得声称已本地验证。

- [ ] **Step 2: 验证冻结 Go tree 与变更范围**

```sh
test "$(git rev-parse HEAD:openwrt-feed/liquid-formula/src)" = \
  d4f299087af3fdb87f7728846425d48edfeb7ae4
git diff --exit-code 4df16f07463ebc1f84154700abdde69c98aad35f -- \
  openwrt-feed/liquid-formula/src
git diff --check 4df16f07463ebc1f84154700abdde69c98aad35f..HEAD
```

- [ ] **Step 3: 请求独立代码复核并修复阻断项**

审查重点：POSIX shell 可移植性、`.git` 剪枝、缺失路径失败原子性、manifest 是否覆盖全部61文件、浅克隆是否真正无依赖、测试是否会递归或污染主工作区、文档与版本一致性。

- [ ] **Step 4: 提交最终修正并确认工作区干净**

所有 review fix 必须重新运行相关定向测试；最终 `git status --short` 为空。

- [ ] **Step 5: 生成并反向验证三个交付文件**

完整包从最终 `HEAD` 的全部 tracked files生成，wrapper 为 `Liquid-Formula-1.8.5/`。增量包只包含相对 1.8.4 baseline 的新增/修改文件，wrapper 为 `Liquid-Formula-1.8.4-to-1.8.5/`；文件清单按字典序列出同一路径集。

验证 ZIP CRC、文件数、隐藏路径、第三方源码、无 `.git`/临时产物、字节与 `HEAD` 一致、增量路径集与 `git diff --name-status` 一致、无删除路径，并记录 SHA-256。

### Task 5: 合并跨架构 Nlink 兼容修正并重新打包 1.8.5

**Files:**

- Modify: `openwrt-feed/liquid-formula/src-subscription-gateway/gateway_disk_gc.go`
- Modify: `openwrt-feed/liquid-formula/src-subscription-gateway/gateway_legacy.go`
- Modify: `tests/shell/test_source_package.sh`
- Modify: `tests/shell/test_subscription_normalize.sh`
- Modify: `tests/shell/test_web_upload_permissions.sh`
- Modify: `README.md`
- Modify: `docs/RELEASE_NOTES_1.8.5.md`
- Recreate outside Git: `Liquid-Formula-1.8.5.zip`
- Recreate outside Git: `Liquid-Formula-1.8.4-to-1.8.5-update-files.zip`
- Recreate outside Git: `Liquid-Formula-1.8.4-to-1.8.5-files.txt`

- [ ] **Step 1: 统一 Nlink 字段宽度**

把 `diskGCFileIdentityFromStat()` 的 `stat.Nlink` 和 legacy marker 的
`after.Nlink` 显式转换为 `uint64`，使 `unix.Stat_t.Nlink` 为 `uint32` 的
OpenWrt 目标也能编译。

- [ ] **Step 2: 保留离线源码清单并加入跨架构编译覆盖**

保留 `test_subscription_normalize.sh` 当前不依赖历史 Git 对象的离线
manifest 前后校验，只合并附件中的 `run_go_cross()` 与默认
`amd64 arm arm64` 交叉编译循环。

- [ ] **Step 3: 增加 Git mode 明确诊断**

在 `test_source_package.sh` 中要求全部索引条目为 `100644`，并让
`test_web_upload_permissions.sh` 将全 `100755` 提交视为应被源码包校验
明确拒绝的错误状态，同时仍验证权限恢复脚本能够修复工作树且不修改 `.git`；同步修正
README 与双语发布说明，不再宣称全 `100755` Git 提交可以通过 Actions。

- [ ] **Step 4: 仅做静态一致性检查并重新打包**

按用户要求不运行任何测试或编译；只检查最终 diff、确认冻结的
`openwrt-feed/liquid-formula/src` 与第三方 `SHA256SUMS` 覆盖对象未变，随后从
最终 Git 树重新生成并反向核对完整包、增量包及文件清单。
