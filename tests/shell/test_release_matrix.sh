#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
MATRIX="$REPO_ROOT/.github/arch-matrix.json"
WORKFLOW="$REPO_ROOT/.github/workflows/build.yml"
BUILD_ACTION="$REPO_ROOT/.github/actions/build-package/action.yml"
RESOLVE="$REPO_ROOT/.github/scripts/resolve-matrix.py"
COLLECT="$REPO_ROOT/.github/scripts/collect-release-assets.py"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-matrix-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

. "$SCRIPT_DIR/harness.sh"

assert_file_exists "$MATRIX" "architecture matrix data file exists"
assert_file_exists "$WORKFLOW" "build workflow exists"
assert_file_exists "$BUILD_ACTION" "reusable build action exists"
assert_file_exists "$RESOLVE" "matrix resolver exists"
assert_file_exists "$COLLECT" "release asset collector exists"

if ! command -v python3 >/dev/null 2>&1; then
	record_failure "python3 is available for CI helper validation"
	finish_tests
	exit $?
fi

# --- the data file itself -----------------------------------------------------

python3 - "$MATRIX" > "$TEST_TMP/matrix.report" 2>&1 <<'PY'
import json, sys
spec = json.load(open(sys.argv[1], encoding="utf-8"))
problems = []

versions = [r["version"] for r in spec["openwrt"]]
if sorted(versions) != sorted(set(versions)):
    problems.append("duplicate OpenWrt versions")
if "SNAPSHOT" in json.dumps(spec):
    problems.append("SNAPSHOT must not be part of the release matrix")

for release in spec["openwrt"]:
    version = release["version"]
    arches = [t["arch"] for t in release["targets"]]
    if sorted(arches) != sorted(set(arches)):
        problems.append(f"{version}: duplicate architectures")
    if release["luci_build_arch"] not in arches:
        problems.append(f"{version}: luci_build_arch is not one of its architectures")
    for target in release["targets"]:
        if "/" not in target["target"]:
            problems.append(f"{version}: {target['arch']} target is not target/subtarget")
        if not target["arch"] or " " in target["arch"]:
            problems.append(f"{version}: malformed architecture name")

print("VERSIONS=" + ",".join(versions))
for release in spec["openwrt"]:
    print(f"COUNT_{release['version']}={len(release['targets'])}")
print("PROBLEMS=" + ("|".join(problems) if problems else "none"))
PY

assert_contains "$TEST_TMP/matrix.report" '^PROBLEMS=none$' "architecture matrix is internally consistent"
assert_contains "$TEST_TMP/matrix.report" '^VERSIONS=24\.10,25\.12$' "matrix targets OpenWrt 24.10 and 25.12 only"
assert_contains "$TEST_TMP/matrix.report" '^COUNT_24\.10=21$' "OpenWrt 24.10 covers 21 architectures"
assert_contains "$TEST_TMP/matrix.report" '^COUNT_25\.12=22$' "OpenWrt 25.12 covers 22 architectures"

# riscv64 is named differently across the two releases; a copy-paste error here
# silently drops the architecture from one of them.
assert_contains "$MATRIX" 'riscv64_riscv64' "keeps the 24.10 riscv64 architecture name"
assert_contains "$MATRIX" 'riscv64_generic' "keeps the 25.12 riscv64 architecture name"
assert_contains "$MATRIX" 'loongarch64_generic' "covers loongarch64 on 25.12"

# --- helper scripts compile and behave ---------------------------------------

assert_command_success "matrix resolver compiles" python3 -m py_compile "$RESOLVE"
assert_command_success "release collector compiles" python3 -m py_compile "$COLLECT"

# The collector must reject a release that lost its LuCI package, otherwise a
# partial matrix would publish an unusable release.
mkdir -p "$TEST_TMP/arts/pkg-a" "$TEST_TMP/good/pkg-a"
printf 'main\n' > "$TEST_TMP/arts/pkg-a/singbox-formula_x86_64_openwrt-25.12.apk"
assert_command_failure "collector rejects a release with no LuCI package" \
	python3 "$COLLECT" --input "$TEST_TMP/arts" --output "$TEST_TMP/out" \
	--main singbox-formula --luci luci-app-singbox-formula

printf 'main\n' > "$TEST_TMP/good/pkg-a/singbox-formula_x86_64_openwrt-25.12.apk"
printf 'luci\n' > "$TEST_TMP/good/pkg-a/luci-app-singbox-formula_all_openwrt-25.12.apk"
printf 'info\n' > "$TEST_TMP/good/pkg-a/BUILD_INFO_x86_64.txt"
assert_command_success "collector accepts a complete release set" \
	python3 "$COLLECT" --input "$TEST_TMP/good" --output "$TEST_TMP/goodout" \
	--main singbox-formula --luci luci-app-singbox-formula
assert_file_exists "$TEST_TMP/goodout/singbox-formula_x86_64_openwrt-25.12.apk" "collector copies the service package"
assert_file_exists "$TEST_TMP/goodout/luci-app-singbox-formula_all_openwrt-25.12.apk" "collector copies the LuCI package"
assert_file_not_exists "$TEST_TMP/goodout/BUILD_INFO_x86_64.txt" "collector leaves per-job build info out of the release"

# --- workflow wiring ----------------------------------------------------------

# GitHub runs an explicit `shell: bash` with `-eo pipefail`, so a producer piped
# into an early-exiting consumer (head, grep -q) dies on SIGPIPE and fails the
# step. This bit the SDK extraction once; keep it from coming back.
LINT="$REPO_ROOT/.github/scripts/lint-workflow-pipes.py"
assert_file_exists "$LINT" "pipefail hazard linter exists"
assert_command_success "workflow shell blocks have no pipefail hazards" \
	python3 "$LINT" "$WORKFLOW" "$BUILD_ACTION"


assert_contains "$WORKFLOW" 'resolve-matrix\.py' "workflow resolves the matrix through the helper"
assert_contains "$WORKFLOW" 'collect-release-assets\.py' "workflow assembles release assets through the helper"
assert_contains "$WORKFLOW" 'fail-fast: false' "one failing architecture does not cancel the others"
assert_contains "$WORKFLOW" 'contents: write' "the release job can publish"
assert_contains "$WORKFLOW" 'gh release (create|upload)' "workflow publishes with the GitHub CLI"
assert_not_contains "$WORKFLOW" 'SNAPSHOT' "workflow never builds SNAPSHOT"

# The build action must not assume one CPU family any more.
assert_contains "$BUILD_ACTION" 'loongarch64_\*\)' "build action verifies loongarch binaries"
assert_contains "$BUILD_ACTION" 'riscv64_\*\)' "build action verifies riscv binaries"
assert_contains "$BUILD_ACTION" 'mipsel_\*\)' "build action distinguishes little endian mips"
assert_contains "$BUILD_ACTION" 'for ext in apk ipk' "build action accepts both apk and ipk output"

finish_tests
