# Web Single-LF Fixture Idempotence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让单 LF 完整性集成测试在源码树已经是合法网页单 LF 版本时保持幂等，同时继续拒绝正文变化、两个 LF、CRLF 和非 allowlist 路径。

**Architecture:** 保留 `source_file_matches_manifest_hash` 的只读严格校验行为，在同一 shell helper 中增加一个仅供测试夹具使用的准备函数。准备函数只对 allowlist 路径执行两种动作：原始哈希完全匹配时追加一个 LF；已经匹配“原始字节 + 一个 LF”时不修改；其余状态失败且不修改。完整源码集成测试通过固定 manifest 取得期望哈希，不再无条件追加 LF。

**Tech Stack:** POSIX shell、GNU coreutils（`sha256sum`、`head`）、现有 shell 测试 harness。

## Global Constraints

- 生产源码校验仍只接受原始字节或 allowlist 文件的原始字节加一个 LF。
- `.env` 正文必须保持上游的 `RUNTIME_ENVIROMENT=DEVELOPMENT`，不得接受 `RUNTIME_ENVIRONMENT=DEVELOPMENT`。
- 两个 LF、CRLF、末尾空格、正文变化和非 allowlist 路径仍必须失败。
- 不修改 OpenWrt 服务监听、默认密码、日志内容或任何运行时行为。
- 所有交付文件必须可通过 GitHub 网页 **Upload files** 上传；隐藏的 `.env` 不放入交付 ZIP。

---

### Task 1: 用失败测试定义幂等的夹具准备行为

**Files:**
- Modify: `tests/shell/test_source_integrity.sh`
- Existing: `tests/shell/source_integrity.sh`

**Interfaces:**
- Consumes: `source_file_matches_manifest_hash FILE EXPECTED_HASH RELATIVE_PATH ALLOWLIST`。
- Produces: 对计划中的 `source_file_ensure_web_single_lf_variant FILE EXPECTED_HASH RELATIVE_PATH ALLOWLIST` 的行为约束。

- [ ] **Step 1: 添加夹具准备函数的回归断言**

在 `tests/shell/test_source_integrity.sh` 中增加真实文件断言：

```sh
cp "$TEST_TMP/canonical" "$TEST_TMP/prepared-canonical"
assert_command_success \
	'fixture preparation converts canonical bytes to exactly one LF' \
	source_file_ensure_web_single_lf_variant \
	"$TEST_TMP/prepared-canonical" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_files_equal \
	"$TEST_TMP/one-lf" \
	"$TEST_TMP/prepared-canonical" \
	'fixture preparation emits the reviewed one-LF bytes'

cp "$TEST_TMP/one-lf" "$TEST_TMP/prepared-one-lf"
assert_command_success \
	'fixture preparation accepts an existing reviewed one-LF file' \
	source_file_ensure_web_single_lf_variant \
	"$TEST_TMP/prepared-one-lf" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_files_equal \
	"$TEST_TMP/one-lf" \
	"$TEST_TMP/prepared-one-lf" \
	'fixture preparation is byte-idempotent for an existing one-LF file'
```

- [ ] **Step 2: 运行测试并确认 RED**

Run: `sh tests/shell/test_source_integrity.sh`

Expected: FAIL；新断言以 `source_file_ensure_web_single_lf_variant: not found` 或等价的退出码 `127` 失败，原有严格校验断言仍通过。

- [ ] **Step 3: 提交 RED 测试**

```bash
git add tests/shell/test_source_integrity.sh
git commit -m "test: reproduce idempotent web LF fixture preparation"
```

### Task 2: 实现严格且幂等的单 LF 夹具准备

**Files:**
- Modify: `tests/shell/source_integrity.sh`
- Modify: `tests/shell/test_source_integrity.sh`

**Interfaces:**
- Consumes: 单文件路径、manifest SHA-256、源码相对路径和两路径 allowlist。
- Produces: `source_file_ensure_web_single_lf_variant FILE EXPECTED_HASH RELATIVE_PATH ALLOWLIST`；返回 `0` 表示文件现在是原始字节加一个 LF，返回 `1` 表示内容或路径不允许，返回 `2` 表示读取或写入错误。

- [ ] **Step 1: 添加最小准备函数**

在 `tests/shell/source_integrity.sh` 中增加：

```sh
source_file_ensure_web_single_lf_variant() {
	integrity_file=$1
	integrity_expected_hash=$2
	integrity_relative_path=$3
	integrity_allowlist=$4

	grep -Fqx "$integrity_relative_path" "$integrity_allowlist" || return 1
	integrity_actual_hash=$(sha256sum "$integrity_file") || return 2
	integrity_actual_hash=${integrity_actual_hash%% *}
	if [ "$integrity_actual_hash" = "$integrity_expected_hash" ]; then
		printf '\n' >> "$integrity_file" || return 2
		return 0
	fi

	source_file_matches_manifest_hash \
		"$integrity_file" \
		"$integrity_expected_hash" \
		"$integrity_relative_path" \
		"$integrity_allowlist"
}
```

- [ ] **Step 2: 让完整源码测试通过 manifest 驱动准备函数**

在 `tests/shell/test_source_integrity.sh` 中读取 `singbox-subscribe-convert-8222509.manifest`，对 `.env` 和 `.github/workflows/go-release-docker.yml` 查找对应哈希并调用 `source_file_ensure_web_single_lf_variant`，删除两个无条件 `printf '\n' >>`。

- [ ] **Step 3: 补充拒绝与无副作用断言**

验证正文变化和非 allowlist 路径调用准备函数时失败，且文件字节保持不变。

- [ ] **Step 4: 运行聚焦测试并确认 GREEN**

Run: `sh tests/shell/test_source_integrity.sh && sh tests/shell/test_source_package.sh`

Expected: 两个脚本均返回 `0`；新增幂等断言通过，源码包 36 项断言为 0 失败。

- [ ] **Step 5: 提交实现**

```bash
git add tests/shell/source_integrity.sh tests/shell/test_source_integrity.sh
git commit -m "test: make web LF fixture preparation idempotent"
```

### Task 3: 验证并制作网页上传包

**Files:**
- Create outside Git: `dist/singbox-formula-1.5.0-web-upload-single-lf-fixture-fix.zip`
- Include: `tests/shell/source_integrity.sh`
- Include: `tests/shell/test_source_integrity.sh`
- Include: `docs/superpowers/plans/2026-07-15-web-single-lf-fixture-idempotence.md`

**Interfaces:**
- Consumes: Task 2 的通过状态和干净工作树。
- Produces: 不含隐藏文件、无目录包装层、可直接解压后网页上传的 ZIP。

- [ ] **Step 1: 运行完整验证**

Run: `for test_script in tests/shell/test_*.sh; do sh "$test_script"; done`

Expected: 所有 shell 脚本返回 `0`，所有断言为 `0 failures`。

Run:

```bash
node --check openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/overview.js
node --check openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/templates.js
sh -n openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula
git diff --check
```

Expected: 全部返回 `0` 且无诊断。

- [ ] **Step 2: 创建并校验 ZIP**

ZIP 只包含本计划列出的三个普通文件。使用 `unzip -Z1` 确认无隐藏 basename、无顶层包装目录；逐文件与当前 Git blob 比较，并记录 SHA-256。

- [ ] **Step 3: 复核网页操作要求**

交付说明必须明确：解压 ZIP 后通过 **Add file → Upload files** 上传三个文件；随后使用 GitHub 网页编辑现有 `openwrt-feed/singbox-formula/src/.env`，正文必须精确为 `RUNTIME_ENVIROMENT=DEVELOPMENT`。网页保存产生的一个末尾 LF允许保留。
