#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
DPI_DIR="$REPO_ROOT/openwrt-feed/liquid-formula"
MAKEFILE="$DPI_DIR/Makefile"
ROOT_MOMO_TEMPLATE="$REPO_ROOT/momo-template.json"
PACKAGED_MOMO_TEMPLATE="$DPI_DIR/files/www/liquid-formula/templates/momo-template.json"
PACKAGED_LOCALDNS_TEMPLATE="$DPI_DIR/files/www/liquid-formula/templates/localdns-template.json"
ROOT_MOMO_TEMPLATE_SHA256=748745145195b8def355b138e477bc5451902b3902c6f90094c15d4801209f17
PACKAGED_MOMO_TEMPLATE_SHA256=5341a298ae1c8c804d143a045c450355fef69cf58b06c026489bf92dd0e55e63
PACKAGED_LOCALDNS_TEMPLATE_SHA256=ecda3da752640095ec8e1b13cddea72da92b4d7a64b008e56ae31dfcf7bd54ff
FROZEN_CONVERTER_MANIFEST="$SCRIPT_DIR/fixtures/converter-source-1.8.8.manifest"
FROZEN_DPI_MANIFEST="$SCRIPT_DIR/fixtures/dpi-source-1.8.8.manifest"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-dpi-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

. "$SCRIPT_DIR/harness.sh"
. "$SCRIPT_DIR/source_manifest.sh"

assert_file_exists "$MAKEFILE" "DPI package Makefile exists"
WAN_RESOLVER="$DPI_DIR/files/usr/share/liquid-formula-dpi/wan-resolver.sh"

# --- authoritative template and archived-source boundaries ------------------

assert_file_sha256 \
	"$ROOT_MOMO_TEMPLATE_SHA256" \
	"$ROOT_MOMO_TEMPLATE" \
	"keeps the archived root momo template byte-exact"
assert_file_sha256 \
	"$PACKAGED_MOMO_TEMPLATE_SHA256" \
	"$PACKAGED_MOMO_TEMPLATE" \
	"ships the authoritative momo template at its packaged path"
assert_file_sha256 \
	"$PACKAGED_LOCALDNS_TEMPLATE_SHA256" \
	"$PACKAGED_LOCALDNS_TEMPLATE" \
	"ships the authoritative local DNS template at its packaged path"

# 1.8.6 moved source-integrity checking to offline manifests so shallow clones
# and browser uploads still verify. These two assertions were the last holdouts
# on `git rev-parse HEAD:<path>`: outside a Git checkout the command failed, the
# `|| true` swallowed it, and the comparison silently degraded to "" vs a pinned
# digest — a guaranteed failure that said nothing about the source tree.
CONVERTER_MANIFEST_ACTUAL="$TEST_TMP/converter-source.manifest"
assert_command_success \
	"the archived converter tree can be manifested without Git history" \
	write_source_manifest "$DPI_DIR/src" "$CONVERTER_MANIFEST_ACTUAL"
assert_files_equal \
	"$FROZEN_CONVERTER_MANIFEST" \
	"$CONVERTER_MANIFEST_ACTUAL" \
	"keeps the archived converter source tree unchanged"

DPI_MANIFEST_ACTUAL="$TEST_TMP/dpi-source.manifest"
assert_command_success \
	"the archived DPI trees can be manifested without Git history" \
	write_source_manifest "$DPI_DIR/src-dpi" "$DPI_MANIFEST_ACTUAL"
assert_files_equal \
	"$FROZEN_DPI_MANIFEST" \
	"$DPI_MANIFEST_ACTUAL" \
	"keeps the archived third-party DPI source trees unchanged"

# --- vendored sources are present and buildable from source ------------------

assert_file_exists "$DPI_DIR/src-dpi/fakehttp/Makefile" "FakeHTTP source is vendored"
assert_file_exists "$DPI_DIR/src-dpi/fakehttp/src/mainfun.c" "FakeHTTP has its C sources"
assert_file_exists "$DPI_DIR/src-dpi/fakesip/Makefile" "FakeSIP source is vendored"
assert_file_exists "$DPI_DIR/src-dpi/fakesip/src/mainfun.c" "FakeSIP has its C sources"

# No prebuilt ELF may be committed — everything is cross-compiled in the SDK.
committed_elf=$(find "$DPI_DIR" -type f \( -name fakehttp -o -name fakesip \) \
	-not -path "*/src/*.c" 2>/dev/null | while IFS= read -r candidate; do
		if head -c 4 "$candidate" 2>/dev/null | grep -q "$(printf '\177ELF')"; then
			printf '%s\n' "$candidate"
		fi
	done)
if [ -z "$committed_elf" ]; then
	record_ok "no prebuilt FakeHTTP/FakeSIP binary is committed"
else
	record_failure "no prebuilt FakeHTTP/FakeSIP binary is committed"
	printf '  committed ELF: %s\n' "$committed_elf" >&2
fi

# --- FakeSIP must stay the project fork, never replaced by upstream -----------
# The fork adds portmap.[ch] for the -p / -P port filters; their absence means
# someone swapped in a stock FakeSIP that lacks the maintained fixes.

assert_file_exists "$DPI_DIR/src-dpi/fakesip/src/portmap.c" "FakeSIP fork keeps its portmap.c"
assert_file_exists "$DPI_DIR/src-dpi/fakesip/include/portmap.h" "FakeSIP fork keeps its portmap.h"
assert_contains "$DPI_DIR/src-dpi/fakesip/src/mainfun.c" "portmap.h" "FakeSIP mainfun uses the fork's portmap"

# 观测对端 TTL 不能被注入方向门控挡住。默认 direction=outbound 时 init 传 -0,
# 那会置 g_ctx.inbound 而留空 g_ctx.outbound —— 记录动作若排在
# "if (!g_ctx.outbound)" 之后就永远不执行, 动态 TTL 又会退回固定初值。
put_line=$(grep -n 'fs_srcinfo_put' "$DPI_DIR/src-dpi/fakesip/src/rawsend.c" | head -n 1 | cut -d: -f1)
gate_line=$(grep -n 'if (!g_ctx.outbound)' "$DPI_DIR/src-dpi/fakesip/src/rawsend.c" | head -n 1 | cut -d: -f1)
if [ -n "$put_line" ] && [ -n "$gate_line" ] && [ "$put_line" -lt "$gate_line" ]; then
	record_ok "FakeSIP records peer TTL before the direction gate"
else
	record_failure "FakeSIP records peer TTL before the direction gate"
fi

# 反向扫描的加法防回绕。
assert_contains "$DPI_DIR/src-dpi/fakehttp/src/srcinfo.c" 'srci_end \+ CAPACITY - i - 1' "FakeHTTP reverse scan cannot underflow"
assert_contains "$DPI_DIR/src-dpi/fakesip/src/srcinfo.c" 'srci_end \+ CAPACITY - i - 1' "FakeSIP reverse scan cannot underflow"

# --- vendored trees match their pinned upstream tarballs ---------------------
# The tarballs carry the authoritative bytes and third_party/SHA256SUMS pins
# their hashes. What actually gets compiled is the extracted tree, so compare
# that against the tarball rather than trusting the archive alone.

SUMS="$REPO_ROOT/third_party/SHA256SUMS"
assert_file_exists "$SUMS" "third-party checksum manifest exists"

if ( cd "$REPO_ROOT" && sha256sum -c third_party/SHA256SUMS >/dev/null 2>&1 ); then
	record_ok "vendored tarballs match their recorded checksums"
else
	record_failure "vendored tarballs match their recorded checksums"
fi

compare_vendored_tree() {
	tarball="$REPO_ROOT/third_party/sources/$1"
	tree="$DPI_DIR/src-dpi/$2"
	label="$3"

	if [ ! -f "$tarball" ]; then
		record_failure "$label tarball is present"
		return
	fi
	record_ok "$label tarball is present"

	extract_dir="$TEST_TMP/$2-upstream"
	mkdir -p "$extract_dir"
	if ! tar -xzf "$tarball" -C "$extract_dir" --strip-components=1 2>/dev/null; then
		record_failure "$label tarball extracts cleanly"
		return
	fi
	record_ok "$label tarball extracts cleanly"

	if diff -rq "$extract_dir" "$tree" >/dev/null 2>&1; then
		record_ok "$label vendored tree is byte-identical to its tarball"
	else
		record_failure "$label vendored tree is byte-identical to its tarball"
		diff -rq "$extract_dir" "$tree" 2>&1 | head -5 >&2
	fi
}

compare_vendored_tree FakeHTTP-Formula-0.9.21.tar.gz fakehttp FakeHTTP
compare_vendored_tree FakeSIP-Formula-0.9.8.tar.gz fakesip FakeSIP

# --- version pins are explicit and justified ---------------------------------

assert_contains "$MAKEFILE" '^FAKEHTTP_VERSION:=0\.9\.21' "FakeHTTP is pinned to the fork's 0.9.21"
# fork 相对上游 0.9.18 的唯一改动就是这个校验; 它不见了说明 vendored 树被
# 换回了未修复的上游版本。
assert_contains "$DPI_DIR/src-dpi/fakehttp/src/ipv4pkt.c" 'invalid TCP data offset' "FakeHTTP fork keeps the IPv4 data offset check"
assert_contains "$DPI_DIR/src-dpi/fakehttp/src/ipv6pkt.c" 'invalid TCP data offset' "FakeHTTP fork keeps the IPv6 data offset check"
# 少一个实参的 %04x 会去读不存在的 vararg。两个 fork 都补过, 别被换回去。
assert_contains "$DPI_DIR/src-dpi/fakehttp/src/rawsend.c" 'unknown ethertype 0x%04x", \(unsigned int\) ethertype' "FakeHTTP ethertype log passes its argument"
assert_contains "$DPI_DIR/src-dpi/fakesip/src/rawsend.c" 'unknown ethertype 0x%04x", \(unsigned int\) ethertype' "FakeSIP ethertype log passes its argument"
assert_contains "$MAKEFILE" '^FAKESIP_VERSION:=0\.9\.8' "FakeSIP is pinned to the fork's 0.9.8"

# The init script compensates for FakeHTTP 0.9.18's reversed payload ring;
# document that coupling so a blind version bump is caught in review.
assert_contains "$MAKEFILE" 'payload ring' "Makefile explains why FakeHTTP is version-locked"

# --- packaging shape ---------------------------------------------------------

assert_make_top_level_contains "$MAKEFILE" \
	'^\s*PKG_NAME:=liquid-formula\s*$' "the merged package keeps the liquid-formula name"
assert_make_top_level_not_contains "$MAKEFILE" '@TARGET_' \
	"the merged package is not pinned to a single OpenWrt target"
assert_contains "$MAKEFILE" 'kmod-nft-queue' "the merged package depends on the NFQUEUE kernel module"
assert_contains "$MAKEFILE" 'libnetfilter-queue' "the merged package depends on libnetfilter-queue"
assert_make_block_contains "$MAKEFILE" \
	'Package/liquid-formula/install' \
	'momo-template\.json.*momo-template\.json' \
	"installs the authoritative momo template"
assert_make_block_contains "$MAKEFILE" \
	'Package/liquid-formula/install' \
	'localdns-template\.json.*localdns-template\.json' \
	"installs the authoritative local DNS template"

# WAN auto-detection is a packaged runtime helper, not test-only logic. It is
# installed executable because both init scripts and hotplug source it on a
# router restored from a GitHub web upload.
assert_file_exists "$WAN_RESOLVER" "shared official WAN resolver is packaged"
assert_make_block_contains \
	"$MAKEFILE" \
	'Package/liquid-formula/install' \
	'wan-resolver\.sh.*wan-resolver\.sh' \
	"package install copies the shared WAN resolver"

# The SDK toolchain values must reach both upstream Makefiles, which use
# `override CFLAGS+=`, so a plain assignment here would be silently dropped.
assert_contains "$MAKEFILE" 'CC="..TARGET_CC."' "cross compiler is passed to the vendored builds"
assert_contains "$MAKEFILE" 'CFLAGS="..TARGET_CFLAGS.' "target CFLAGS reach the vendored builds"

# --- version matches the LuCI companion package -------------------------------

main_version=$(sed -n 's/^PKG_VERSION:=\(.*\)$/\1/p' "$MAKEFILE")
luci_version=$(sed -n 's/^PKG_VERSION:=\(.*\)$/\1/p' \
	"$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/Makefile")
assert_equal "$main_version" "$luci_version" \
	"LuCI package version matches the converter and DPI package"

finish_tests
