#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"
. "$SCRIPT_DIR/source_manifest.sh"

PACKAGE_DIR="$REPO_ROOT/openwrt-feed/liquid-formula"
SOURCE_DIR=${SOURCE_DIR:-"$PACKAGE_DIR/src"}
PACKAGE_MAKEFILE="$PACKAGE_DIR/Makefile"
WORKFLOW="$REPO_ROOT/.github/workflows/build.yml"
UPSTREAM_MANIFEST="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.manifest"
PATCHED_PATHS="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.patched-paths"
LOCAL_PATHS="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-local-paths"
FROZEN_SOURCE_MANIFEST="$SCRIPT_DIR/fixtures/converter-source-1.8.3.manifest"
UPSTREAM_COMMIT=8222509aff98229886d304ef72e1d0affb087a62
GPL3_SHA256=3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986
LUMBERJACK_MIT_SHA256=4eb222b860ec541a0f981a01de5454ba50d09d38b2d09fa6894ed0bf6331293e

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-source-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

# 仓库索引始终保持普通文件模式；web-upload 工作树所需的执行位由恢复脚本提供。
tracked_modes=$(git -C "$REPO_ROOT" ls-files -s |
	awk '$1 != "100644" { sub(/^[^\t]*\t/, ""); print }')
assert_equal "" "$tracked_modes" "every tracked file is committed as 100644"

find_elf_files() {
	find "$1" -type f -exec sh -c '
		for file do
			magic=$(LC_ALL=C od -An -tx1 -N4 "$file" | tr -d "[:space:]")
			if [ "$magic" = 7f454c46 ]; then
				printf "%s\n" "$file"
			fi
		done
	' sh {} +
}

ACTUAL_SOURCE_MANIFEST="$TEST_TMP/converter-source.manifest"
assert_command_success \
	"writes the working converter source manifest" \
	write_source_manifest "$SOURCE_DIR" "$ACTUAL_SOURCE_MANIFEST"
assert_files_equal \
	"$FROZEN_SOURCE_MANIFEST" \
	"$ACTUAL_SOURCE_MANIFEST" \
	"keeps the working converter source identical to the frozen 1.8.3 manifest"

WORKFLOW_ARCHES=$(python3 -S - "$WORKFLOW" <<'PY'
import re
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    lines = stream.read().splitlines()

try:
    jobs_start = lines.index("jobs:")
except ValueError:
    raise SystemExit(1)

test_jobs = [
    index
    for index in range(jobs_start + 1, len(lines))
    if lines[index] == "  test:"
]
if len(test_jobs) != 1:
    raise SystemExit(1)
test_start = test_jobs[0]
job_header = re.compile(r"^  [A-Za-z0-9_-]+:\s*(?:#.*)?$")
test_end = next(
    (index for index in range(test_start + 1, len(lines)) if job_header.match(lines[index])),
    len(lines),
)

step_name = "      - name: Compile converter for representative architectures"
matches = [
    index
    for index in range(test_start + 1, test_end)
    if lines[index] == step_name
]
if len(matches) != 1:
    raise SystemExit(1)
step_start = matches[0]
step_end = next(
    (
        index
        for index in range(step_start + 1, test_end)
        if lines[index].startswith("      - ")
    ),
    test_end,
)
run_headers = [
    index
    for index in range(step_start + 1, step_end)
    if lines[index] == "        run: |"
]
if len(run_headers) != 1:
    raise SystemExit(1)
run_lines = []
for line in lines[run_headers[0] + 1 : step_end]:
    if line.startswith("          "):
        run_lines.append(line[10:])
    elif not line:
        run_lines.append("")
    else:
        break
run = "\n".join(run_lines)
loop = re.search(r"for goarch in ([A-Za-z0-9_ ]+); do", run)
if loop is None:
    raise SystemExit(1)
arches = loop.group(1).split()
if arches != ["amd64", "arm", "arm64"]:
    raise SystemExit(1)
required = [
    'CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build',
    'test -s "$output"',
]
if any(fragment not in run for fragment in required):
    raise SystemExit(1)
print(" ".join(arches))
PY
)
assert_equal \
	"amd64 arm arm64" \
	"$WORKFLOW_ARCHES" \
	"workflow smoke-compiles the converter for amd64, arm, and arm64"

assert_file_content \
	"$UPSTREAM_COMMIT" \
	"$SOURCE_DIR/UPSTREAM_COMMIT" \
	"records the full pinned upstream commit"
assert_file_line_count \
	35 \
	"$UPSTREAM_MANIFEST" \
	"pins all 35 retained upstream paths"
UPSTREAM_MISMATCHES=
while IFS="$(printf '\t')" read -r path expected_mode expected_hash; do
	file="$SOURCE_DIR/$path"
	if [ ! -f "$file" ]; then
		UPSTREAM_MISMATCHES="$UPSTREAM_MISMATCHES missing:$path"
		continue
	fi
	mode=$(stat -c %a "$file")
	case "$mode" in 644) actual_mode=100644 ;; 755) actual_mode=100755 ;; *) actual_mode=unsupported-$mode ;; esac
	if [ "$actual_mode" != "$expected_mode" ]; then
		UPSTREAM_MISMATCHES="$UPSTREAM_MISMATCHES mode:$path"
		continue
	fi
	if ! grep -Fqx "$path" "$PATCHED_PATHS"; then
		# 严格逐字节比对。原先给 .env 和上游 docker workflow 开的
		# "仅允许差一个结尾 LF" 例外通道已随那些文件一并删除。
		if [ "$(sha256sum "$file" | cut -d' ' -f1)" != "$expected_hash" ]; then
			UPSTREAM_MISMATCHES="$UPSTREAM_MISMATCHES hash:$path"
		fi
	fi
done < "$UPSTREAM_MANIFEST"
assert_empty "$UPSTREAM_MISMATCHES" "preserves every untouched upstream byte and mode"

cut -f1 "$UPSTREAM_MANIFEST" > "$TEST_TMP/allowed.paths"
cat "$LOCAL_PATHS" >> "$TEST_TMP/allowed.paths"
LC_ALL=C sort -u "$TEST_TMP/allowed.paths" -o "$TEST_TMP/allowed.paths"
find "$SOURCE_DIR" -type f | sed "s#^$SOURCE_DIR/##" | LC_ALL=C sort > "$TEST_TMP/actual.paths"
assert_files_equal "$TEST_TMP/allowed.paths" "$TEST_TMP/actual.paths" "contains only pinned upstream and explicitly reviewed local source paths"
assert_file_exists "$SOURCE_DIR/go.mod" "vendors upstream go.mod"
assert_file_exists "$SOURCE_DIR/go.sum" "vendors upstream go.sum"
assert_file_exists "$SOURCE_DIR/LICENSE" "vendors the upstream Apache license"
assert_file_exists \
	"$SOURCE_DIR/LICENSES/GPL-3.0-or-later.txt" \
	"ships the GPL-3.0-or-later license text"
assert_file_sha256 \
	"$GPL3_SHA256" \
	"$SOURCE_DIR/LICENSES/GPL-3.0-or-later.txt" \
	"ships the canonical GPL-3.0 license text"
assert_file_exists \
	"$SOURCE_DIR/LICENSES/MIT-lumberjack.txt" \
	"ships the lumberjack MIT license text"
assert_file_sha256 \
	"$LUMBERJACK_MIT_SHA256" \
	"$SOURCE_DIR/LICENSES/MIT-lumberjack.txt" \
	"ships the exact lumberjack MIT license notice"
assert_file_not_exists \
	"$PACKAGE_DIR/files/usr/bin/sb-sub-c" \
	"does not ship the old prebuilt converter"

ELF_FILES=$(find_elf_files "$PACKAGE_DIR")
assert_empty "$ELF_FILES" "contains no ELF binary anywhere in the package tree"

NESTED_GIT=$(find "$PACKAGE_DIR" -mindepth 1 -name .git -print)
assert_empty "$NESTED_GIT" "contains no nested Git metadata"

FORBIDDEN_ARTIFACTS=$(find "$PACKAGE_DIR" -mindepth 1 \
	\( -iname '*.apk' \
	-o -iname '*.ipk' \
	-o -iname '*openwrt-sdk*' \
	-o -iname 'sdk' \
	-o -iname 'sdk-*' \
	-o -iname '*.tar.gz' \
	-o -iname '*.zip' \
	-o -iname 'sb-sub-c' \
	-o -iname 'singbox-subscribe-convert' \
	-o -iname 'build_dir' \
	-o -iname 'staging_dir' \
	-o -iname 'output_pkg' \
	-o -iname 'tmp' \
	-o -iname '.tmp' \
	-o -iname '.cache' \
	-o -iname '*.tmp' \
	-o -iname '*.temp' \
	-o -iname '*.swp' \
	-o -iname '*.swo' \
	-o -iname '*.bak' \
	-o -iname '*.orig' \
	-o -iname '*.rej' \
	-o -name '*~' \
	-o -name '.#*' \
	-o -name '#*#' \
	-o -name '.DS_Store' \) -print)
assert_empty \
	"$FORBIDDEN_ARTIFACTS" \
	"contains no APK, IPK, SDK, archive, local binary, or temporary artifact"

SPECIAL_ENTRIES=$(find "$PACKAGE_DIR" -mindepth 1 ! -type d ! -type f -print)
assert_empty "$SPECIAL_ENTRIES" "contains only regular files and directories"

PRIVATE_KEY_FILES=$(find "$PACKAGE_DIR" -type f \
	-exec grep -IlE -- '-----BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----' {} + \
	2>/dev/null || true)
assert_empty "$PRIVATE_KEY_FILES" "contains no embedded private key"

assert_make_top_level_not_contains \
	"$PACKAGE_MAKEFILE" \
	'@TARGET_' \
	"does not pin the package to a single OpenWrt target"

assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*PKG_VERSION[[:space:]]*:=[[:space:]]*1\.8\.7[[:space:]]*$' \
	"sets package version 1.8.7 in active top-level metadata"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*PKG_RELEASE[[:space:]]*:=[[:space:]]*1[[:space:]]*$' \
	"sets package release 1 in active top-level metadata"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*PKG_LICENSE[[:space:]]*:=[[:space:]]*Apache-2\.0[[:space:]]+GPL-3\.0-or-later[[:space:]]+MIT[[:space:]]*$' \
	"declares all licenses in active top-level metadata"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*PKG_LICENSE_FILES[[:space:]]*:=[[:space:]]*LICENSE[[:space:]]+LICENSES/GPL-3\.0-or-later\.txt[[:space:]]+LICENSES/MIT-lumberjack\.txt' \
	"collects all license texts from the prepared build directory"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*GO_PKG[[:space:]]*:=[[:space:]]*github\.com/haierkeys/singbox-subscribe-convert[[:space:]]*$' \
	"sets the upstream Go module in active helper metadata"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*GO_PKG_BUILD_PKG[[:space:]]*:=[[:space:]]*github\.com/haierkeys/singbox-subscribe-convert[[:space:]]*$' \
	"builds the converter main package in active helper metadata"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*GO_PKG_INSTALL_EXTRA[[:space:]]*:=[[:space:]]*config/config\.yaml[[:space:]]*$' \
	"adds the go:embed YAML to the helper workspace"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*include[[:space:]]+\$\(TOPDIR\)/feeds/packages/lang/golang/golang-package\.mk[[:space:]]*$' \
	"imports the OpenWrt Go package helper at top level"
assert_make_top_level_order \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*GO_PKG_INSTALL_EXTRA[[:space:]]*:=' \
	'^[[:space:]]*include[[:space:]]+\$\(TOPDIR\)/feeds/packages/lang/golang/golang-package\.mk' \
	"declares extra helper inputs before importing golang-package.mk"
assert_make_top_level_contains \
	"$PACKAGE_MAKEFILE" \
	'^[[:space:]]*GO_PKG_BUILD_VARS[[:space:]]*\+=[[:space:]]*GOFLAGS=-buildvcs=false[[:space:]]*$' \
	"disables VCS probing during Go target discovery"

assert_contains \
	"$SOURCE_DIR/main.go" \
	'^//go:embed[[:space:]]+config/config\.yaml[[:space:]]*$' \
	"converter embeds config/config.yaml"

GO_EXTRA_FILES=$(make_top_level "$PACKAGE_MAKEFILE" | \
	sed -n 's/^[[:space:]]*GO_PKG_INSTALL_EXTRA[[:space:]]*:=[[:space:]]*//p' | \
	tail -n 1)
for extra_file in $GO_EXTRA_FILES; do
	if [ -f "$SOURCE_DIR/$extra_file" ]; then
		mkdir -p "$TEST_TMP/helper-workspace/$(dirname "$extra_file")"
		cp "$SOURCE_DIR/$extra_file" "$TEST_TMP/helper-workspace/$extra_file"
	fi
done
EMBED_RESOURCES=$(sed -n \
	's/^[[:space:]]*\/\/go:embed[[:space:]][[:space:]]*//p' \
	"$SOURCE_DIR/main.go")
MISSING_EMBED_RESOURCES=
for embed_resource in $EMBED_RESOURCES; do
	if [ ! -f "$TEST_TMP/helper-workspace/$embed_resource" ]; then
		MISSING_EMBED_RESOURCES="${MISSING_EMBED_RESOURCES}${MISSING_EMBED_RESOURCES:+ }$embed_resource"
	fi
done
assert_empty \
	"$MISSING_EMBED_RESOURCES" \
	"helper-filtered workspace contains every root-package go:embed resource"

assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Build/Prepare' \
	'^[[:space:]]*\$\(CP\)[[:space:]]+\./src/\.[[:space:]]+\$\(PKG_BUILD_DIR\)/[[:space:]]*$' \
	"prepares the build directory from vendored source in Build/Prepare"
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Build/Compile' \
	'^[[:space:]]*\$\(call[[:space:]]+GoPackage/Build/Compile\)[[:space:]]*$' \
	"invokes the OpenWrt Go helper in Build/Compile"
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Build/Compile' \
	'^[[:space:]]*\$\(CP\)[[:space:]]+\$\(GO_PKG_BUILD_BIN_DIR\)/singbox-subscribe-convert[[:space:]]+\$\(PKG_BUILD_DIR\)/sb-sub-c[[:space:]]*$' \
	"materializes the target-built converter in Build/Compile"
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/install' \
	'^[[:space:]]*\$\(INSTALL_BIN\)[[:space:]]+\$\(PKG_BUILD_DIR\)/sb-sub-c[[:space:]]+\$\(1\)/usr/bin/sb-sub-c[[:space:]]*$' \
	"installs the target-built converter from the active install block"
assert_not_contains \
	"$PACKAGE_MAKEFILE" \
	'\./files/usr/bin/sb-sub-c' \
	"does not reference the old prebuilt converter"
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula' \
	'^[[:space:]]*DEPENDS[[:space:]]*:=[[:space:]]*.*\$\(GO_ARCH_DEPENDS\)' \
	"derives architecture support from GO_ARCH_DEPENDS instead of a single target"

finish_tests
