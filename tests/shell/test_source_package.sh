#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"
. "$SCRIPT_DIR/source_integrity.sh"

PACKAGE_DIR="$REPO_ROOT/openwrt-feed/singbox-formula"
SOURCE_DIR=${SOURCE_DIR:-"$PACKAGE_DIR/src"}
PACKAGE_MAKEFILE="$PACKAGE_DIR/Makefile"
UPSTREAM_MANIFEST="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.manifest"
PATCHED_PATHS="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.patched-paths"
LOCAL_PATHS="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-local-paths"
WEB_SINGLE_LF_PATHS="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths"
UPSTREAM_COMMIT=8222509aff98229886d304ef72e1d0affb087a62
GPL3_SHA256=3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986
LUMBERJACK_MIT_SHA256=4eb222b860ec541a0f981a01de5454ba50d09d38b2d09fa6894ed0bf6331293e

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-source-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

write_source_manifest() {
	manifest_root=$1
	manifest_output=$2

	find "$manifest_root" -type f \
		! -path "$manifest_root/UPSTREAM_COMMIT" \
		! -path "$manifest_root/LICENSES/GPL-3.0-or-later.txt" \
		! -path "$manifest_root/LICENSES/MIT-lumberjack.txt" \
		-exec sh -c '
			root=$1
			shift
			for file do
				relative_path=${file#"$root"/}
				mode=$(stat -c %a "$file") || exit 1
				case $mode in
					644) git_mode=100644 ;;
					755) git_mode=100755 ;;
					*) git_mode=unsupported-$mode ;;
				esac
				hash=$(sha256sum "$file") || exit 1
				hash=${hash%% *}
				printf "%s\t%s\t%s\n" "$relative_path" "$git_mode" "$hash"
			done
		' sh "$manifest_root" {} + | LC_ALL=C sort > "$manifest_output"
}

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

printf '%s\n' \
	'.env' \
	'.github/workflows/go-release-docker.yml' \
	> "$TEST_TMP/expected-web-single-lf.paths"
assert_files_equal \
	"$TEST_TMP/expected-web-single-lf.paths" \
	"$WEB_SINGLE_LF_PATHS" \
	'limits web single-LF normalization to the two reviewed upstream paths'

assert_file_content \
	"$UPSTREAM_COMMIT" \
	"$SOURCE_DIR/UPSTREAM_COMMIT" \
	"records the full pinned upstream commit"
assert_file_line_count \
	48 \
	"$UPSTREAM_MANIFEST" \
	"pins all 48 original upstream paths"
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
		if ! source_file_matches_manifest_hash \
			"$file" \
			"$expected_hash" \
			"$path" \
			"$WEB_SINGLE_LF_PATHS"; then
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
	'^[[:space:]]*PKG_VERSION[[:space:]]*:=[[:space:]]*1\.6\.3[[:space:]]*$' \
	"sets package version 1.6.3 in active top-level metadata"
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
	'^[[:space:]]*PKG_LICENSE_FILES[[:space:]]*:=[[:space:]]*LICENSE[[:space:]]+LICENSES/GPL-3\.0-or-later\.txt[[:space:]]+LICENSES/MIT-lumberjack\.txt[[:space:]]*$' \
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
	'Package/singbox-formula/install' \
	'^[[:space:]]*\$\(INSTALL_BIN\)[[:space:]]+\$\(PKG_BUILD_DIR\)/sb-sub-c[[:space:]]+\$\(1\)/usr/bin/sb-sub-c[[:space:]]*$' \
	"installs the target-built converter from the active install block"
assert_not_contains \
	"$PACKAGE_MAKEFILE" \
	'\./files/usr/bin/sb-sub-c' \
	"does not reference the old prebuilt converter"
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/singbox-formula' \
	'^[[:space:]]*DEPENDS[[:space:]]*:=[[:space:]]*.*\$\(GO_ARCH_DEPENDS\)' \
	"derives architecture support from GO_ARCH_DEPENDS instead of a single target"

finish_tests
