# GitHub 网页上传执行权限修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让经 GitHub 网页上传、所有文件 mode 均退化为 `100644` 的源码在 Actions checkout 后自动恢复精确执行权限，并完成现有测试与 OpenWrt SDK 构建。

**Architecture:** workflow 在 checkout 后通过 `sh` 调用一个自身无需 executable bit 的 POSIX shell 修复脚本。脚本先验证固定的 13 个必需路径全部存在，再只对这些路径执行 `chmod 0755`；回归测试用临时的全 `0644` 文件树验证恢复、失败原子性、错误信息、非清单文件保护和 workflow 顺序。

**Tech Stack:** POSIX shell、GitHub Actions YAML、现有 TAP 风格 shell harness、Git、ZIP。

## Global Constraints

- 仅支持用户指定的 GitHub 网页上传工作流，不要求 Git CLI、GitHub CLI、API push 或其他上传方式。
- converter 实际监听 `:<port>`，不是 `127.0.0.1:<port>`。
- 首次安装密码保持 `890716`。
- 日志继续保留密码、完整订阅 URL、订阅令牌和缓存随机参数。
- OpenWrt 版本保持 `25.12.5`，target/subtarget 保持 `mediatek/mt7622`，架构保持 `aarch64_cortex-a53`。
- 不升级插件版本，不修改 converter、LuCI、procd 或 UCI 运行逻辑。
- 修复清单只包含设计规格列出的 13 个原 `100755` 路径；修复脚本自身和原本为 `100644` 的测试继续通过 `sh` 调用。
- GitHub 浏览器上传器每次最多接受 100 个文件；保留全部 110 个 tracked files，并严格使用 88 文件与 22 文件的两轮上传。
- 两个网页上传批次的路径交集必须为空，并集必须与 HEAD 的 tracked path set 完全一致。

---

## File Structure

- Create: `.github/scripts/restore-executable-modes.sh` — checkout 后验证并恢复固定执行权限清单。
- Create: `tests/shell/test_web_upload_permissions.sh` — 以全 `0644` fixture 回归网页上传场景，并验证 workflow 接线。
- Modify: `.github/workflows/build.yml` — checkout 后、任何测试和 SDK source copy 前调用修复脚本。
- Modify: `README.md` — 记录网页上传兼容行为、两轮上传步骤和自举方式。
- Modify: `docs/superpowers/specs/2026-07-14-github-web-upload-permissions-design.md` — 规定三包交付与两批路径边界。
- Modify: `docs/superpowers/plans/2026-07-14-github-web-upload-permissions.md` — 以两批生成、验证和交付步骤取代单包流程。

### Task 1: 用回归测试驱动权限恢复与 workflow 接线

**Files:**

- Create: `tests/shell/test_web_upload_permissions.sh`
- Create: `.github/scripts/restore-executable-modes.sh`
- Modify: `.github/workflows/build.yml`

**Interfaces:**

- Consumes: 可选仓库根目录参数 `$1`；GitHub Actions 环境变量 `$GITHUB_WORKSPACE`；现有 `tests/shell/harness.sh`。
- Produces: `sh .github/scripts/restore-executable-modes.sh [repo-root]`，成功时 13 个固定路径均为 `0755`，缺失路径时在任何 chmod 前返回非零。

- [ ] **Step 1: 写入失败的网页上传回归测试**

创建 `tests/shell/test_web_upload_permissions.sh`，内容如下：

```sh
#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

RESTORE_SCRIPT="$REPO_ROOT/.github/scripts/restore-executable-modes.sh"
WORKFLOW="$REPO_ROOT/.github/workflows/build.yml"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-web-upload-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

EXPECTED_PATHS="$TEST_TMP/expected-paths"
cat > "$EXPECTED_PATHS" <<'EOF'
openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula
openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula
openwrt-feed/singbox-formula/files/etc/uci-defaults/99-singbox-formula
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/run-delayed.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/update.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/validate-template.sh
tests/shell/test_generate_config.sh
tests/shell/test_migration.sh
tests/shell/test_procd_service.sh
tests/shell/test_rpc_contract.sh
tests/shell/test_template_transactions.sh
tests/shell/test_update.sh
EOF

create_web_upload_tree() {
	fixture_root=$1

	while IFS= read -r relative_path; do
		mkdir -p "$fixture_root/$(dirname "$relative_path")" || return 1
		printf '#!/bin/sh\nexit 0\n' > "$fixture_root/$relative_path" || return 1
		chmod 0644 "$fixture_root/$relative_path" || return 1
	done < "$EXPECTED_PATHS"

	mkdir -p "$fixture_root/docs" || return 1
	printf 'ordinary file\n' > "$fixture_root/docs/ordinary.txt" || return 1
	chmod 0644 "$fixture_root/docs/ordinary.txt" || return 1
}

if [ ! -f "$RESTORE_SCRIPT" ]; then
	record_failure "web-upload mode repair script exists (missing: $RESTORE_SCRIPT)"
	finish_tests
	exit $?
fi
record_ok 'web-upload mode repair script exists'

WEB_TREE="$TEST_TMP/web tree"
create_web_upload_tree "$WEB_TREE" || exit 1
assert_command_success \
	'0644-only web upload tree is repaired when the repair script itself is invoked through sh' \
	sh "$RESTORE_SCRIPT" "$WEB_TREE"

while IFS= read -r relative_path; do
	assert_equal \
		755 \
		"$(stat -c %a "$WEB_TREE/$relative_path")" \
		"restores mode 0755: $relative_path"
done < "$EXPECTED_PATHS"
assert_equal \
	644 \
	"$(stat -c %a "$WEB_TREE/docs/ordinary.txt")" \
	'leaves files outside the executable allowlist at mode 0644'

DEFAULT_TREE="$TEST_TMP/default tree"
create_web_upload_tree "$DEFAULT_TREE" || exit 1
assert_command_success \
	'omitting repo-root repairs the current directory, including a path containing spaces' \
	sh -c 'cd "$1" && sh "$2"' sh "$DEFAULT_TREE" "$RESTORE_SCRIPT"
assert_equal \
	755 \
	"$(stat -c %a "$DEFAULT_TREE/openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula")" \
	'default current-directory mode restores an allowlisted executable'

MISSING_TREE="$TEST_TMP/missing-tree"
create_web_upload_tree "$MISSING_TREE" || exit 1
MISSING_PATH='openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh'
rm "$MISSING_TREE/$MISSING_PATH"
if sh "$RESTORE_SCRIPT" "$MISSING_TREE" > "$TEST_TMP/missing.stdout" 2> "$TEST_TMP/missing.stderr"; then
	record_failure 'missing required executable makes mode repair fail'
else
	record_ok 'missing required executable makes mode repair fail'
fi
assert_contains \
	"$TEST_TMP/missing.stderr" \
	'^restore-executable-modes: missing required file: openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config\.sh$' \
	'missing-file error names the exact relative path'
assert_equal \
	644 \
	"$(stat -c %a "$MISSING_TREE/openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula")" \
	'validates the complete allowlist before changing any mode'

checkout_line=$(grep -nF 'uses: actions/checkout@v7' "$WORKFLOW" | head -n 1 | cut -d: -f1)
restore_line=$(grep -nF 'sh .github/scripts/restore-executable-modes.sh "$GITHUB_WORKSPACE"' "$WORKFLOW" | head -n 1 | cut -d: -f1)
setup_go_line=$(grep -nF 'uses: actions/setup-go@v6' "$WORKFLOW" | head -n 1 | cut -d: -f1)
if [ -n "$checkout_line" ] && [ -n "$restore_line" ] && [ -n "$setup_go_line" ] && \
	[ "$checkout_line" -lt "$restore_line" ] && [ "$restore_line" -lt "$setup_go_line" ]; then
	record_ok 'workflow restores modes immediately after checkout and before setup/test steps'
else
	record_failure 'workflow restores modes immediately after checkout and before setup/test steps'
fi

finish_tests
```

- [ ] **Step 2: 运行测试并确认 RED 来自缺少修复脚本**

Run:

```sh
sh tests/shell/test_web_upload_permissions.sh
```

Expected: exit `1`，唯一失败说明包含 `.github/scripts/restore-executable-modes.sh` 缺失；不是 shell 语法错误或 fixture 创建错误。

- [ ] **Step 3: 写入最小权限恢复脚本**

创建 `.github/scripts/restore-executable-modes.sh`，文件本身保持 mode `0644`，内容如下：

```sh
#!/bin/sh

set -eu

REPO_ROOT=${1:-.}

if [ ! -d "$REPO_ROOT" ]; then
	printf 'restore-executable-modes: repository root is not a directory: %s\n' "$REPO_ROOT" >&2
	exit 1
fi

EXECUTABLE_PATHS='openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula
openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula
openwrt-feed/singbox-formula/files/etc/uci-defaults/99-singbox-formula
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/run-delayed.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/update.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/validate-template.sh
tests/shell/test_generate_config.sh
tests/shell/test_migration.sh
tests/shell/test_procd_service.sh
tests/shell/test_rpc_contract.sh
tests/shell/test_template_transactions.sh
tests/shell/test_update.sh'

for relative_path in $EXECUTABLE_PATHS; do
	if [ ! -f "$REPO_ROOT/$relative_path" ]; then
		printf 'restore-executable-modes: missing required file: %s\n' "$relative_path" >&2
		exit 1
	fi
done

for relative_path in $EXECUTABLE_PATHS; do
	chmod 0755 "$REPO_ROOT/$relative_path"
done

printf 'restore-executable-modes: restored 13 executable files\n'
```

- [ ] **Step 4: 在 checkout 后接入 workflow**

在 `.github/workflows/build.yml` 的 `Checkout repository` step 后、`Set up pinned Go toolchain for tests` step 前加入：

```yaml
      - name: Restore executable modes after web upload
        run: sh .github/scripts/restore-executable-modes.sh "$GITHUB_WORKSPACE"
```

- [ ] **Step 5: 运行目标测试并确认 GREEN**

Run:

```sh
sh -n .github/scripts/restore-executable-modes.sh
sh -n tests/shell/test_web_upload_permissions.sh
sh tests/shell/test_web_upload_permissions.sh
```

Expected: 两个语法检查 exit `0`；回归测试报告 `22 assertions, 0 failures`，13 个清单路径均为 `0755`，非清单文件仍为 `0644`，省略 root 参数也能从含空格的当前目录自举。

- [ ] **Step 6: 检查 diff 并提交实现**

Run:

```sh
git diff --check
git diff -- .github/scripts/restore-executable-modes.sh .github/workflows/build.yml tests/shell/test_web_upload_permissions.sh
git add .github/scripts/restore-executable-modes.sh .github/workflows/build.yml tests/shell/test_web_upload_permissions.sh
git commit -m "ci: restore executable modes after web upload"
```

Expected: diff 仅包含固定清单修复、checkout 后接线和对应测试；commit 成功，修复脚本与新测试在 Git index 中均为 `100644`。

### Task 2: 文档、既有验证证据与两轮网页上传交付

**Files:**

- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-14-github-web-upload-permissions-design.md`
- Modify: `docs/superpowers/plans/2026-07-14-github-web-upload-permissions.md`
- Create outside Git tree: `dist/singbox-formula-1.5.0-complete-source.zip`
- Create outside Git tree: `dist/singbox-formula-1.5.0-web-upload-batch-1-openwrt-feed.zip`
- Create outside Git tree: `dist/singbox-formula-1.5.0-web-upload-batch-2-repository.zip`
- Remove outside Git tree after replacement verification: `dist/singbox-formula-1.5.0-web-upload.zip`

**Interfaces:**

- Consumes: Task 1 的 `restore-executable-modes.sh`、110 个 tracked paths、已记录的 shell/Node.js/rpcd/Go/全 `0644` 验证证据和提交后的干净 `HEAD`。
- Produces: 一个 110 文件完整参考包和两个无交集的网页上传批次；Batch 1 为 `openwrt-feed/` 的 88 个文件，Batch 2 为其余 22 个文件。

- [ ] **Step 1: 更新三份文档中的两轮网页上传契约**

README 必须说明 GitHub 浏览器每次最多上传 100 个文件，并给出以下操作：

1. 本地解压两个 batch ZIP，不上传 ZIP 文件本身；
2. 在仓库根目录使用 **Add file → Upload files**；
3. 第一轮打开 Batch 1 wrapper，只拖入其中的 `openwrt-feed` 目录并提交；
4. 第二轮打开 Batch 2 wrapper，同时拖入 `.github`、`.gitignore`、`README.md`、`docs`、`momo-template.json` 和 `tests` 并提交；
5. 选择第二批前显示隐藏文件：macOS Finder 使用 `Command+Shift+.`，Windows Explorer 使用 **View → Show → Hidden items**；
6. 不拖入 wrapper 目录本身，避免产生额外目录层级；
7. 以第二次提交后的结果为准，忽略两批之间的 Actions 中间结果。

设计和计划同时记录 110/88/22 文件边界、两个 wrapper prefix、完整包只用于完整性/参考而非单轮上传，以及两批交集为空、并集等于 HEAD 的验收条件。

- [ ] **Step 2: 确认 review fix 只触及文档并复用原验证证据**

Run:

```sh
git diff --check
git diff --name-only
git diff --quiet -- openwrt-feed .github tests
rg -n '100 files|88 tracked files|22 tracked files|Add file → Upload files|Command\+Shift\+\.|Hidden items' README.md
rg -n ':<port>|890716|cache-buster' README.md docs/superpowers/specs/2026-07-14-github-web-upload-permissions-design.md docs/superpowers/plans/2026-07-14-github-web-upload-permissions.md
```

Expected: `git diff --check` 和 runtime/workflow/tests/scripts 的 quiet diff 均 exit `0`；变更列表只有三份指定文档；监听 `:<port>`、默认密码 `890716` 及完整 secret/URL/token/cache-buster 日志策略保持不变。

Task 2 原报告中的 314 个 shell assertions、两个 Node checks、rpcd 语法、`go test -race`、`go vet` 与全 `0644` 模拟证据继续有效，因为 review fix 没有修改任何 executable/source 文件。除非范围检查意外发现这类文件发生变化，否则不要重跑完整矩阵。

- [ ] **Step 3: 以指定标题提交三份文档**

Run:

```sh
git diff --check
git add README.md \
	docs/superpowers/specs/2026-07-14-github-web-upload-permissions-design.md \
	docs/superpowers/plans/2026-07-14-github-web-upload-permissions.md
git diff --cached --name-only
git commit -m "docs: document two-batch web upload"
```

Expected: staged/committed path 只有三份指定文档，提交成功，runtime、workflow、tests 与 scripts 无变化。

- [ ] **Step 4: 从已提交且干净的 HEAD 生成三个 ZIP**

Run:

```sh
test -z "$(git status --porcelain)"
mkdir -p /workspace/scratch/dc5edf9fd457/dist
git archive \
	--format=zip \
	--prefix=SingBox-Formula-1.5.0/ \
	--output=/workspace/scratch/dc5edf9fd457/dist/singbox-formula-1.5.0-complete-source.zip \
	HEAD
git archive \
	--format=zip \
	--prefix=SingBox-Formula-1.5.0-web-upload-batch-1/ \
	--output=/workspace/scratch/dc5edf9fd457/dist/singbox-formula-1.5.0-web-upload-batch-1-openwrt-feed.zip \
	HEAD -- openwrt-feed
git archive \
	--format=zip \
	--prefix=SingBox-Formula-1.5.0-web-upload-batch-2/ \
	--output=/workspace/scratch/dc5edf9fd457/dist/singbox-formula-1.5.0-web-upload-batch-2-repository.zip \
	HEAD -- .github .gitignore README.md docs momo-template.json tests
```

Expected: 三个命令均 exit `0`；完整包来自 HEAD 的全部 110 个文件，Batch 1 和 Batch 2 分别只取规定的 88 与 22 个文件。

- [ ] **Step 5: 验证 ZIP 完整性、计数、边界、交集和并集**

Run:

```sh
set -eu
DIST=/workspace/scratch/dc5edf9fd457/dist
COMPLETE="$DIST/singbox-formula-1.5.0-complete-source.zip"
BATCH_1="$DIST/singbox-formula-1.5.0-web-upload-batch-1-openwrt-feed.zip"
BATCH_2="$DIST/singbox-formula-1.5.0-web-upload-batch-2-repository.zip"
VERIFY_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-zip-verify.XXXXXX")
trap 'rm -rf "$VERIFY_ROOT"' EXIT HUP INT TERM

unzip -tq "$COMPLETE"
unzip -tq "$BATCH_1"
unzip -tq "$BATCH_2"

zip_paths() {
	prefix=$1
	archive=$2
	unzip -Z1 "$archive" | awk -v prefix="$prefix" '
		substr($0, length($0), 1) != "/" {
			if (index($0, prefix) != 1) exit 2
			print substr($0, length(prefix) + 1)
		}' | LC_ALL=C sort
}

git ls-tree -r --name-only HEAD | LC_ALL=C sort > "$VERIFY_ROOT/head"
zip_paths 'SingBox-Formula-1.5.0/' "$COMPLETE" > "$VERIFY_ROOT/complete"
zip_paths 'SingBox-Formula-1.5.0-web-upload-batch-1/' "$BATCH_1" > "$VERIFY_ROOT/batch-1"
zip_paths 'SingBox-Formula-1.5.0-web-upload-batch-2/' "$BATCH_2" > "$VERIFY_ROOT/batch-2"

test "$(wc -l < "$VERIFY_ROOT/complete")" -eq 110
test "$(wc -l < "$VERIFY_ROOT/batch-1")" -eq 88
test "$(wc -l < "$VERIFY_ROOT/batch-2")" -eq 22
test "$(wc -l < "$VERIFY_ROOT/batch-1")" -le 100
test "$(wc -l < "$VERIFY_ROOT/batch-2")" -le 100

awk '$0 !~ /^openwrt-feed\// { bad = 1 } END { exit bad }' "$VERIFY_ROOT/batch-1"
awk '$0 ~ /^openwrt-feed\// { bad = 1 } END { exit bad }' "$VERIFY_ROOT/batch-2"
grep -Fx '.github/scripts/restore-executable-modes.sh' "$VERIFY_ROOT/batch-2"
grep -Fx '.github/workflows/build.yml' "$VERIFY_ROOT/batch-2"
grep -Fx '.gitignore' "$VERIFY_ROOT/batch-2"
grep -Fx 'README.md' "$VERIFY_ROOT/batch-2"

comm -12 "$VERIFY_ROOT/batch-1" "$VERIFY_ROOT/batch-2" > "$VERIFY_ROOT/overlap"
test ! -s "$VERIFY_ROOT/overlap"
LC_ALL=C sort -u "$VERIFY_ROOT/batch-1" "$VERIFY_ROOT/batch-2" > "$VERIFY_ROOT/union"
cmp "$VERIFY_ROOT/union" "$VERIFY_ROOT/head"
cmp "$VERIFY_ROOT/complete" "$VERIFY_ROOT/head"
git show HEAD:README.md > "$VERIFY_ROOT/README.head"
unzip -p "$COMPLETE" SingBox-Formula-1.5.0/README.md > "$VERIFY_ROOT/README.zip"
cmp "$VERIFY_ROOT/README.zip" "$VERIFY_ROOT/README.head"

stat -c '%n size_bytes=%s' "$COMPLETE" "$BATCH_1" "$BATCH_2"
sha256sum "$COMPLETE" "$BATCH_1" "$BATCH_2"
```

Expected: 三个 `unzip -tq` 均 exit `0`；file counts 为 `110`、`88`、`22`；两批均 `<= 100`，overlap 为 0，sorted union 与 HEAD byte-for-byte 相同；Batch 1 只有 `openwrt-feed/**`，Batch 2 没有该前缀并包含四个指定关键路径；完整包路径集与 HEAD 相同，README bytes 与 `HEAD:README.md` 相同；记录三个大小和 SHA-256。

- [ ] **Step 6: 删除旧误导包并完成最终自检**

仅在 Step 5 全部通过后执行：

```sh
rm /workspace/scratch/dc5edf9fd457/dist/singbox-formula-1.5.0-web-upload.zip
test ! -e /workspace/scratch/dc5edf9fd457/dist/singbox-formula-1.5.0-web-upload.zip
git diff-tree --no-commit-id --name-only -r HEAD
git diff --check HEAD^
git status --short --branch
git log -3 --oneline
```

Expected: stale ZIP 不存在；fix commit 只包含三份指定文档且 `git diff --check` 通过；分支为 `fix/v1.5.0`、working tree 干净；报告记录原 314 项及 Node/rpcd/Go/全 `0644` 证据因纯文档修复继续有效，且未执行 push、GitHub CLI/API 或 PR 操作。
