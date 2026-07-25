#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
DPI_DIR="$REPO_ROOT/openwrt-feed/liquid-formula-dpi"
MAKEFILE="$DPI_DIR/Makefile"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-dpi-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

. "$SCRIPT_DIR/harness.sh"

assert_file_exists "$MAKEFILE" "DPI package Makefile exists"

# --- vendored sources are present and buildable from source ------------------

assert_file_exists "$DPI_DIR/src/fakehttp/Makefile" "FakeHTTP source is vendored"
assert_file_exists "$DPI_DIR/src/fakehttp/src/mainfun.c" "FakeHTTP has its C sources"
assert_file_exists "$DPI_DIR/src/fakesip/Makefile" "FakeSIP source is vendored"
assert_file_exists "$DPI_DIR/src/fakesip/src/mainfun.c" "FakeSIP has its C sources"

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

assert_file_exists "$DPI_DIR/src/fakesip/src/portmap.c" "FakeSIP fork keeps its portmap.c"
assert_file_exists "$DPI_DIR/src/fakesip/include/portmap.h" "FakeSIP fork keeps its portmap.h"
assert_contains "$DPI_DIR/src/fakesip/src/mainfun.c" "portmap.h" "FakeSIP mainfun uses the fork's portmap"

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
	tree="$DPI_DIR/src/$2"
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

compare_vendored_tree FakeHTTP-Formula-0.9.20.tar.gz fakehttp FakeHTTP
compare_vendored_tree FakeSIP-Formula-0.9.7.tar.gz fakesip FakeSIP

# --- version pins are explicit and justified ---------------------------------

assert_contains "$MAKEFILE" '^FAKEHTTP_VERSION:=0\.9\.20' "FakeHTTP is pinned to the fork's 0.9.20"
# fork 相对上游 0.9.18 的唯一改动就是这个校验; 它不见了说明 vendored 树被
# 换回了未修复的上游版本。
assert_contains "$DPI_DIR/src/fakehttp/src/ipv4pkt.c" 'invalid TCP data offset' "FakeHTTP fork keeps the IPv4 data offset check"
assert_contains "$DPI_DIR/src/fakehttp/src/ipv6pkt.c" 'invalid TCP data offset' "FakeHTTP fork keeps the IPv6 data offset check"
# 少一个实参的 %04x 会去读不存在的 vararg。两个 fork 都补过, 别被换回去。
assert_contains "$DPI_DIR/src/fakehttp/src/rawsend.c" 'unknown ethertype 0x%04x", \(unsigned int\) ethertype' "FakeHTTP ethertype log passes its argument"
assert_contains "$DPI_DIR/src/fakesip/src/rawsend.c" 'unknown ethertype 0x%04x", \(unsigned int\) ethertype' "FakeSIP ethertype log passes its argument"
assert_contains "$MAKEFILE" '^FAKESIP_VERSION:=0\.9\.7' "FakeSIP is pinned to the fork's 0.9.7"

# The init script compensates for FakeHTTP 0.9.18's reversed payload ring;
# document that coupling so a blind version bump is caught in review.
assert_contains "$MAKEFILE" 'payload ring' "Makefile explains why FakeHTTP is version-locked"

# --- packaging shape ---------------------------------------------------------

assert_make_top_level_contains "$MAKEFILE" \
	'^\s*PKG_NAME:=liquid-formula-dpi\s*$' "package name is liquid-formula-dpi"
assert_make_top_level_not_contains "$MAKEFILE" '@TARGET_' \
	"DPI package is not pinned to a single OpenWrt target"
assert_contains "$MAKEFILE" 'kmod-nft-queue' "DPI package depends on the NFQUEUE kernel module"
assert_contains "$MAKEFILE" 'libnetfilter-queue' "DPI package depends on libnetfilter-queue"

# The SDK toolchain values must reach both upstream Makefiles, which use
# `override CFLAGS+=`, so a plain assignment here would be silently dropped.
assert_contains "$MAKEFILE" 'CC="..TARGET_CC."' "cross compiler is passed to the vendored builds"
assert_contains "$MAKEFILE" 'CFLAGS="..TARGET_CFLAGS.' "target CFLAGS reach the vendored builds"

# --- version matches the rest of the feed ------------------------------------

dpi_version=$(sed -n 's/^PKG_VERSION:=\(.*\)$/\1/p' "$MAKEFILE")
main_version=$(sed -n 's/^PKG_VERSION:=\(.*\)$/\1/p' "$REPO_ROOT/openwrt-feed/liquid-formula/Makefile")
assert_equal "$main_version" "$dpi_version" "DPI package version matches the converter package"

finish_tests
