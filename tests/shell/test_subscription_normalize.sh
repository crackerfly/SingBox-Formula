#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"
. "$SCRIPT_DIR/source_manifest.sh"

SOURCE_DIR="$REPO_ROOT/openwrt-feed/liquid-formula/src"
HELPER_DIR="$REPO_ROOT/openwrt-feed/liquid-formula/src-subscription-gateway"
FIXTURE_DIR="$REPO_ROOT/tests/subscription/fixtures"
FROZEN_SOURCE_MANIFEST="$SCRIPT_DIR/fixtures/converter-source-1.8.3.manifest"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-normalizer-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

if [ -n "${GO_BIN:-}" ]; then
	:
elif command -v go >/dev/null 2>&1; then
	GO_BIN=$(command -v go)
elif [ -x /tmp/liquid-official-go126/go/bin/go ]; then
	GO_BIN=/tmp/liquid-official-go126/go/bin/go
elif [ -x /tmp/liquid-official-go/go/bin/go ]; then
	GO_BIN=/tmp/liquid-official-go/go/bin/go
else
	record_failure "a Go toolchain is available for the normalizer tests"
	finish_tests
	exit $?
fi

SOURCE_MANIFEST_BEFORE="$TEST_TMP/converter-source-before.manifest"
assert_command_success \
	"writes the converter source manifest before normalizer tests" \
	write_source_manifest "$SOURCE_DIR" "$SOURCE_MANIFEST_BEFORE"
assert_files_equal \
	"$FROZEN_SOURCE_MANIFEST" \
	"$SOURCE_MANIFEST_BEFORE" \
	"the converter source matches the frozen manifest before normalizer tests"

STAGED_MODULE="$TEST_TMP/module"
mkdir -p "$STAGED_MODULE/cmd/liquid-formula-subscription-gateway"
cp -a "$SOURCE_DIR/." "$STAGED_MODULE/"
cp -a "$HELPER_DIR/." "$STAGED_MODULE/cmd/liquid-formula-subscription-gateway/"

assert_file_not_exists \
	"$HELPER_DIR/go.mod" \
	"the external helper does not create a second module"
assert_file_not_exists \
	"$HELPER_DIR/go.work" \
	"the external helper does not create a workspace"
assert_files_equal \
	"$SOURCE_DIR/go.mod" \
	"$STAGED_MODULE/go.mod" \
	"staging retains the existing module manifest byte-for-byte"
assert_files_equal \
	"$SOURCE_DIR/go.sum" \
	"$STAGED_MODULE/go.sum" \
	"staging retains the existing dependency lock byte-for-byte"

GO_VERSION=$("$GO_BIN" version 2>/dev/null | tr '/ ' '__')
GO_CACHE=${GOCACHE:-"$TEST_TMP/go-cache-$GO_VERSION"}
GO_MOD_CACHE=${GOMODCACHE:-/tmp/liquid-go-modcache}
RACE_FLAG=
if [ "${LIQUID_FORMULA_NORMALIZER_RACE:-0}" = 1 ]; then
	RACE_FLAG=-race
fi

run_go()
{
	(
		cd "$STAGED_MODULE" || exit 1
		LIQUID_FORMULA_NORMALIZER_FIXTURES="$FIXTURE_DIR" \
		GOMAXPROCS="${LIQUID_FORMULA_GO_MAX_PROCS:-2}" \
		GOTOOLCHAIN=local \
		GOMODCACHE="$GO_MOD_CACHE" \
		GOCACHE="$GO_CACHE" \
		GOFLAGS=-buildvcs=false \
			"$GO_BIN" "$@"
	)
}

run_go_cross()
{
	(
		cd "$STAGED_MODULE" || exit 1
		GOOS=linux \
		GOARCH="$1" \
		CGO_ENABLED=0 \
		GOMAXPROCS="${LIQUID_FORMULA_GO_MAX_PROCS:-2}" \
		GOTOOLCHAIN=local \
		GOMODCACHE="$GO_MOD_CACHE" \
		GOCACHE="$GO_CACHE" \
		GOFLAGS=-buildvcs=false \
			"$GO_BIN" build -o /dev/null \
			./cmd/liquid-formula-subscription-gateway
	)
}

run_staged_go_tests()
{
	test_package=./cmd/liquid-formula-subscription-gateway
	if [ "$RACE_FLAG" != -race ]; then
		run_go test "$test_package"
		return
	fi

	# The race detector retains shadow state for the life of one test binary.
	# Running all three independent 32 MiB boundary cases in that same process
	# can exceed a CI runner's memory even though each case is bounded. Discover
	# every top-level test, run all ordinary tests together, and isolate only
	# those leaf boundary cases in fresh race-enabled processes.
	test_list=$(run_go test -race -list '^Test' "$test_package") ||
		return 1
	test_names=$(printf '%s\n' "$test_list" |
		sed -n '/^Test[A-Za-z0-9_]*$/p')
	regular_expression='^('
	separator=
	listed_count=0
	regular_count=0
	compact_found=0
	production_found=0
	for test_name in $test_names; do
		case "$test_name" in
		*[!A-Za-z0-9_]*|'')
			return 1
			;;
		esac
		listed_count=$((listed_count + 1))
		case "$test_name" in
		TestCompactCanonicalAggregateByteLimitIsInclusive)
			compact_found=1
			;;
		TestCanonicalAggregateAcceptsProductionReachableExact32MiB)
			production_found=1
			;;
		*)
			regular_expression="${regular_expression}${separator}${test_name}"
			separator='|'
			regular_count=$((regular_count + 1))
			;;
		esac
	done
	regular_expression="${regular_expression})$"
	if [ "$compact_found" -ne 1 ] ||
		[ "$production_found" -ne 1 ] ||
		[ "$listed_count" -ne $((regular_count + 2)) ] ||
		[ "$regular_count" -eq 0 ]; then
		return 1
	fi

	run_go test -race -run "$regular_expression" "$test_package" &&
		run_go test -race -run \
			'^TestCompactCanonicalAggregateByteLimitIsInclusive$/^exactly_32_MiB_accepted_without_trailing_newline$' \
			"$test_package" &&
		run_go test -race -run \
			'^TestCompactCanonicalAggregateByteLimitIsInclusive$/^32_MiB_plus_one_rejected$' \
			"$test_package" &&
		run_go test -race -run \
			'^TestCanonicalAggregateAcceptsProductionReachableExact32MiB$' \
			"$test_package"
}

assert_command_success \
	"the staged normalizer Go tests pass" \
	run_staged_go_tests
assert_command_success \
	"the staged normalizer passes go vet" \
	run_go vet ./cmd/liquid-formula-subscription-gateway
assert_command_success \
	"the staged normalizer builds as an independent command" \
	run_go build -o "$TEST_TMP/liquid-formula-subscription-gateway" \
	./cmd/liquid-formula-subscription-gateway

# The native build above covers only the runner architecture. unix.Stat_t
# field widths vary by target, so compile representative 64-bit uint64-Nlink,
# 64-bit uint32-Nlink, and 32-bit layouts before the SDK matrix begins.
for normalizer_goarch in ${LIQUID_FORMULA_CROSS_GOARCHES:-amd64 arm arm64}; do
	assert_command_success \
		"the staged normalizer cross-compiles for linux/$normalizer_goarch" \
		run_go_cross "$normalizer_goarch"
done

NORMALIZER="$TEST_TMP/liquid-formula-subscription-gateway"
assert_command_success \
	"normalize --input accepts a native sing-box document" \
	sh -c '"$1" normalize --input "$2" >"$3" 2>"$4"' sh \
	"$NORMALIZER" "$FIXTURE_DIR/singbox.json" \
	"$TEST_TMP/native.json" "$TEST_TMP/native.stderr"
assert_contains \
	"$TEST_TMP/native.json" \
	'"outbounds"[[:space:]]*:' \
	"normalize emits a sing-box outbounds object"
assert_contains \
	"$TEST_TMP/native.json" \
	'"type"[[:space:]]*:[[:space:]]*"wireguard"' \
	"native compatible outbound types are retained"

assert_command_success \
	"normalize reads a Base64 URI list from stdin" \
	sh -c '"$1" normalize <"$2" >"$3" 2>"$4"' sh \
	"$NORMALIZER" "$FIXTURE_DIR/base64-uri.txt" \
	"$TEST_TMP/base64.json" "$TEST_TMP/base64.stderr"
assert_contains \
	"$TEST_TMP/base64.json" \
	'"tag"[[:space:]]*:[[:space:]]*"Base64-SS"' \
	"Base64 URI list order and tags reach the CLI output"

if "$NORMALIZER" normalize --input "$FIXTURE_DIR/clash-provider-only.yaml" \
	>"$TEST_TMP/provider.stdout" 2>"$TEST_TMP/provider.stderr"; then
	record_failure "provider-only Clash input fails without fetching providers"
else
	record_ok "provider-only Clash input fails without fetching providers"
fi
assert_not_contains \
	"$TEST_TMP/provider.stderr" \
	'provider-secret-token|https?://|provider\.example' \
	"provider-only diagnostics do not expose URL or token material"

if "$NORMALIZER" normalize --input "$FIXTURE_DIR/unknown.txt" \
	>"$TEST_TMP/unknown.stdout" 2>"$TEST_TMP/unknown.stderr"; then
	record_failure "an unknown document fails"
else
	record_ok "an unknown document fails"
fi
assert_not_contains \
	"$TEST_TMP/unknown.stderr" \
	'must-not-be-logged|secret-token|https?://' \
	"unknown-format diagnostics do not echo the source"

SOURCE_MANIFEST_AFTER="$TEST_TMP/converter-source-after.manifest"
assert_command_success \
	"writes the converter source manifest after normalizer tests" \
	write_source_manifest "$SOURCE_DIR" "$SOURCE_MANIFEST_AFTER"
assert_files_equal \
	"$SOURCE_MANIFEST_BEFORE" \
	"$SOURCE_MANIFEST_AFTER" \
	"staging, testing, and building leave the converter source manifest unchanged"
assert_files_equal \
	"$FROZEN_SOURCE_MANIFEST" \
	"$SOURCE_MANIFEST_AFTER" \
	"the converter source matches the frozen manifest after normalizer tests"

finish_tests
