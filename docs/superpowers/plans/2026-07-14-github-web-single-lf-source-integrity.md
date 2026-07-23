# GitHub Web Single-LF Source Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 GitHub 网页编辑器创建的两个指定上游文件在只多一个末尾 LF 时通过源码完整性校验，同时继续拒绝其他任何字节变化。

**Architecture:** 保留原始 upstream manifest，不保存替代哈希。一个无副作用的 POSIX shell helper 先验证原始 SHA-256；只有显式 allowlist 路径才可移除最后一个 `0x0a` 后再次与原始哈希比较。独立回归测试覆盖边界条件，并用临时源码树重现当前远端状态。

**Tech Stack:** POSIX shell、GNU coreutils（`sha256sum`、`head`、`tail`、`od`）、现有 TAP shell harness、Git、ZIP。

## Global Constraints

- 仅支持用户指定的 GitHub 网页上传工作流，不要求 Git CLI、GitHub CLI、API push 或其他上传方式。
- 只允许 `.env` 与 `.github/workflows/go-release-docker.yml` 多一个末尾 LF。
- 两个 LF、CRLF、末尾空格、正文变化及其他路径的单 LF 均必须失败。
- 原始 `singbox-subscribe-convert-8222509.manifest` 不得修改。
- 两个文件继续是必需路径，mode 继续严格要求 `100644`。
- converter 实际监听 `:<port>`，默认密码保持 `890716`，日志继续保留密码和完整订阅 URL/令牌。
- OpenWrt 保持 `25.12.5`、`mediatek/mt7622`、`aarch64_cortex-a53`；不修改 converter、LuCI、procd 或 UCI 运行逻辑。
- 新增测试脚本保持 mode `100644` 并由 workflow 通过 `sh` 调用，不扩展 executable-mode 恢复清单。
- 修复包只能包含非隐藏文件，用户必须先解压再在仓库根目录使用 **Add file → Upload files**。

---

## File Structure

- Create: `tests/shell/source_integrity.sh` — 单文件 SHA-256/单 LF 严格判定 helper。
- Create: `tests/shell/test_source_integrity.sh` — helper 边界测试和真实临时源码树集成测试。
- Create: `tests/shell/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths` — 两路径 allowlist。
- Modify: `tests/shell/test_source_package.sh` — 接入 helper，并允许测试覆盖 `SOURCE_DIR`。
- Modify: `README.md` — 记录隐藏文件网页创建和严格单 LF 兼容规则。
- Create: `docs/superpowers/plans/2026-07-14-github-web-single-lf-source-integrity.md` — 本计划。
- Existing: `docs/superpowers/specs/2026-07-14-github-web-single-lf-source-integrity-design.md` — 已确认设计。

## Task 1: 用失败测试定义严格的单 LF 兼容边界

**Files:**

- Create: `tests/shell/test_source_integrity.sh`
- Create: `tests/shell/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths`

**Interfaces:**

- Consumes: 后续由 `tests/shell/source_integrity.sh` 提供的 `source_file_matches_manifest_hash FILE EXPECTED_HASH RELATIVE_PATH ALLOWLIST`。
- Produces: 一个可由 workflow 的 `tests/shell/test_*.sh` 循环直接执行的回归脚本。

- [ ] **Step 1: 创建精确两路径 allowlist**

```text
.env
.github/workflows/go-release-docker.yml
```

- [ ] **Step 2: 写入先失败的回归测试**

创建 `tests/shell/test_source_integrity.sh`：

```sh
#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
HELPER="$SCRIPT_DIR/source_integrity.sh"
ALLOWLIST="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths"
SOURCE_DIR="$REPO_ROOT/openwrt-feed/singbox-formula/src"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-integrity-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

. "$SCRIPT_DIR/harness.sh"

if [ ! -f "$HELPER" ]; then
	record_failure "source-integrity helper exists (missing: $HELPER)"
	finish_tests
	exit $?
fi
. "$HELPER"

printf '%s' 'alpha=beta' > "$TEST_TMP/canonical"
CANONICAL_HASH=$(sha256sum "$TEST_TMP/canonical")
CANONICAL_HASH=${CANONICAL_HASH%% *}

cp "$TEST_TMP/canonical" "$TEST_TMP/one-lf"
printf '\n' >> "$TEST_TMP/one-lf"
cp "$TEST_TMP/one-lf" "$TEST_TMP/two-lf"
printf '\n' >> "$TEST_TMP/two-lf"
cp "$TEST_TMP/canonical" "$TEST_TMP/crlf"
printf '\r\n' >> "$TEST_TMP/crlf"
printf '%s\n' 'alpha=changed' > "$TEST_TMP/changed"

assert_command_success \
	'canonical bytes pass without normalization' \
	source_file_matches_manifest_hash "$TEST_TMP/canonical" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_command_success \
	'allowlisted file accepts exactly one trailing LF' \
	source_file_matches_manifest_hash "$TEST_TMP/one-lf" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_command_failure \
	'allowlisted file rejects two trailing LFs' \
	source_file_matches_manifest_hash "$TEST_TMP/two-lf" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_command_failure \
	'allowlisted file rejects CRLF' \
	source_file_matches_manifest_hash "$TEST_TMP/crlf" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_command_failure \
	'allowlisted file rejects changed content plus LF' \
	source_file_matches_manifest_hash "$TEST_TMP/changed" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_command_failure \
	'non-allowlisted file rejects one trailing LF' \
	source_file_matches_manifest_hash "$TEST_TMP/one-lf" "$CANONICAL_HASH" README.md "$ALLOWLIST"

WEB_SOURCE="$TEST_TMP/web-source"
cp -a "$SOURCE_DIR" "$WEB_SOURCE"
printf '\n' >> "$WEB_SOURCE/.env"
printf '\n' >> "$WEB_SOURCE/.github/workflows/go-release-docker.yml"
if SOURCE_DIR="$WEB_SOURCE" sh "$SCRIPT_DIR/test_source_package.sh" > "$TEST_TMP/package.stdout" 2> "$TEST_TMP/package.stderr"; then
	record_ok 'complete source validation accepts the two real single-LF web variants'
else
	record_failure 'complete source validation accepts the two real single-LF web variants'
	cat "$TEST_TMP/package.stdout" >&2
	cat "$TEST_TMP/package.stderr" >&2
fi

finish_tests
```

- [ ] **Step 3: 运行 RED 并确认失败原因唯一**

Run:

```sh
sh tests/shell/test_source_integrity.sh
```

Expected: exit `1`，唯一断言失败为缺少 `tests/shell/source_integrity.sh`；fixture 和临时文件创建没有错误。

- [ ] **Step 4: 提交 RED 测试**

```sh
git add tests/shell/test_source_integrity.sh tests/shell/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths
git commit -m "test: reproduce web editor single-LF source variants"
```

## Task 2: 实现无副作用的单 LF 校验并接入源码包测试

**Files:**

- Create: `tests/shell/source_integrity.sh`
- Modify: `tests/shell/test_source_package.sh`
- Test: `tests/shell/test_source_integrity.sh`

**Interfaces:**

- Consumes: 文件路径、manifest 原始 SHA-256、相对路径和 allowlist 文件。
- Produces: `source_file_matches_manifest_hash`；返回 `0` 表示原始字节或允许的原始字节加一个 LF，返回非零表示不匹配或读取失败。

- [ ] **Step 1: 写入最小 helper**

创建 `tests/shell/source_integrity.sh`：

```sh
#!/bin/sh

source_file_matches_manifest_hash() {
	integrity_file=$1
	integrity_expected_hash=$2
	integrity_relative_path=$3
	integrity_allowlist=$4

	integrity_actual_hash=$(sha256sum "$integrity_file") || return 2
	integrity_actual_hash=${integrity_actual_hash%% *}
	[ "$integrity_actual_hash" = "$integrity_expected_hash" ] && return 0

	grep -Fqx "$integrity_relative_path" "$integrity_allowlist" || return 1
	integrity_last_byte=$(tail -c 1 "$integrity_file" | od -An -tu1 | tr -d '[:space:]') || return 2
	[ "$integrity_last_byte" = 10 ] || return 1

	integrity_without_lf_hash=$(head -c -1 "$integrity_file" | sha256sum) || return 2
	integrity_without_lf_hash=${integrity_without_lf_hash%% *}
	[ "$integrity_without_lf_hash" = "$integrity_expected_hash" ]
}
```

- [ ] **Step 2: 接入 `test_source_package.sh`**

在变量区加入 helper 和 allowlist：

```sh
. "$SCRIPT_DIR/source_integrity.sh"

PACKAGE_DIR="$REPO_ROOT/openwrt-feed/singbox-formula"
SOURCE_DIR=${SOURCE_DIR:-"$PACKAGE_DIR/src"}
WEB_SINGLE_LF_PATHS="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths"
```

在 manifest 校验前验证 allowlist 精确内容：

```sh
printf '%s\n' \
	'.env' \
	'.github/workflows/go-release-docker.yml' \
	> "$TEST_TMP/expected-web-single-lf.paths"
assert_files_equal \
	"$TEST_TMP/expected-web-single-lf.paths" \
	"$WEB_SINGLE_LF_PATHS" \
	'limits web single-LF normalization to the two reviewed upstream paths'
```

将未 patch 路径的直接 SHA 比较替换为：

```sh
if ! grep -Fqx "$path" "$PATCHED_PATHS"; then
	if ! source_file_matches_manifest_hash \
		"$file" \
		"$expected_hash" \
		"$path" \
		"$WEB_SINGLE_LF_PATHS"; then
		UPSTREAM_MISMATCHES="$UPSTREAM_MISMATCHES hash:$path"
	fi
fi
```

- [ ] **Step 3: 运行 GREEN 测试**

Run:

```sh
sh tests/shell/test_source_integrity.sh
sh tests/shell/test_source_package.sh
```

Expected: helper 回归脚本 `7 assertions, 0 failures`；源码包脚本 `36 assertions, 0 failures`。

- [ ] **Step 4: 运行反向篡改检查**

在临时源码树分别制造两个 LF、CRLF 和正文变化，确认 `SOURCE_DIR=<temp> sh tests/shell/test_source_package.sh` 都返回非零且输出包含对应 `hash:<path>`。

- [ ] **Step 5: 提交实现**

```sh
git add tests/shell/source_integrity.sh tests/shell/test_source_package.sh
git commit -m "test: accept reviewed web single-LF source variants"
```

## Task 3: 文档、全量验证与网页修复包

**Files:**

- Modify: `README.md`
- Verify: all changed source/test/spec/plan files
- Create outside Git: `dist/singbox-formula-1.5.0-web-upload-single-lf-fix.zip`

**Interfaces:**

- Consumes: Task 1–2 的提交状态和完整测试结果。
- Produces: 一个无隐藏条目、可在仓库根目录通过 **Upload files** 上传的 ZIP。

- [ ] **Step 1: 更新 README**

在网页上传说明中明确：源码内部隐藏文件必须使用 **Create new file** 创建；`.env` 与嵌套 `go-release-docker.yml` 若被网页编辑器追加一个 LF，源码完整性测试只对这两个路径接受该变体；其他字节仍严格拒绝。

- [ ] **Step 2: 提交文档**

```sh
git add README.md docs/superpowers/specs/2026-07-14-github-web-single-lf-source-integrity-design.md docs/superpowers/plans/2026-07-14-github-web-single-lf-source-integrity.md
git commit -m "docs: explain strict web single-LF source compatibility"
```

- [ ] **Step 3: 运行全量本地验证**

Run:

```sh
set -eu
for test_script in tests/shell/test_*.sh; do sh "$test_script"; done
node --check openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/overview.js
node --check openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/templates.js
sh -n openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula
(cd openwrt-feed/singbox-formula/src && go test -race ./... && go vet ./...)
git diff --check
git status --short
```

Expected: 所有 shell/Node/rpcd/Go 检查通过且工作区干净。若本地没有 Go `1.26.4`，必须报告该项未执行，并以新 GitHub Actions 运行作为最终 Go/SDK 证据，不能声称远端编译已通过。

- [ ] **Step 4: 生成只含非隐藏修复文件的网页包**

使用 `git archive` 从最终 HEAD 打包以下精确路径：

```text
README.md
docs/superpowers/specs/2026-07-14-github-web-single-lf-source-integrity-design.md
docs/superpowers/plans/2026-07-14-github-web-single-lf-source-integrity.md
tests/shell/source_integrity.sh
tests/shell/test_source_integrity.sh
tests/shell/test_source_package.sh
tests/shell/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths
```

验证 ZIP 恰有 7 个文件、没有 basename 以 `.` 开头的条目，解压后关键测试与当前 HEAD 逐字节一致，并记录 ZIP SHA-256。

- [ ] **Step 5: 保存修复包到 Library 并交付**

交付可点击的本地文件链接、ZIP SHA-256、网页上传步骤和剩余验证边界。用户不修改当前远端 `.env`/嵌套 workflow；只上传修复包内的 `README.md`、`docs`、`tests` 三个顶层入口。

