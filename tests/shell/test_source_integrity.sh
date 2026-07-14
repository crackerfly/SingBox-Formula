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
cp "$TEST_TMP/canonical" "$TEST_TMP/space-lf"
printf ' \n' >> "$TEST_TMP/space-lf"
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
	'allowlisted file rejects a trailing space plus LF' \
	source_file_matches_manifest_hash "$TEST_TMP/space-lf" "$CANONICAL_HASH" .env "$ALLOWLIST"
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
