#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
HELPER="$SCRIPT_DIR/source_integrity.sh"
ALLOWLIST="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.web-single-lf-paths"
UPSTREAM_MANIFEST="$SCRIPT_DIR/fixtures/singbox-subscribe-convert-8222509.manifest"
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

cp "$TEST_TMP/changed" "$TEST_TMP/prepared-changed"
assert_command_failure \
	'fixture preparation rejects changed content' \
	source_file_ensure_web_single_lf_variant \
	"$TEST_TMP/prepared-changed" "$CANONICAL_HASH" .env "$ALLOWLIST"
assert_files_equal \
	"$TEST_TMP/changed" \
	"$TEST_TMP/prepared-changed" \
	'failed fixture preparation preserves changed content'

cp "$TEST_TMP/canonical" "$TEST_TMP/prepared-non-allowlisted"
assert_command_failure \
	'fixture preparation rejects a non-allowlisted path' \
	source_file_ensure_web_single_lf_variant \
	"$TEST_TMP/prepared-non-allowlisted" "$CANONICAL_HASH" README.md "$ALLOWLIST"
assert_files_equal \
	"$TEST_TMP/canonical" \
	"$TEST_TMP/prepared-non-allowlisted" \
	'failed fixture preparation preserves a non-allowlisted file'

WEB_SOURCE="$TEST_TMP/web-source"
cp -a "$SOURCE_DIR" "$WEB_SOURCE"
WEB_PREPARATION_FAILURES=
while IFS="$(printf '\t')" read -r relative_path _ expected_hash; do
	grep -Fqx "$relative_path" "$ALLOWLIST" || continue
	if ! source_file_ensure_web_single_lf_variant \
		"$WEB_SOURCE/$relative_path" \
		"$expected_hash" \
		"$relative_path" \
		"$ALLOWLIST"; then
		WEB_PREPARATION_FAILURES="$WEB_PREPARATION_FAILURES $relative_path"
	fi
done < "$UPSTREAM_MANIFEST"
if SOURCE_DIR="$WEB_SOURCE" sh "$SCRIPT_DIR/test_source_package.sh" > "$TEST_TMP/package.stdout" 2> "$TEST_TMP/package.stderr"; then
	WEB_PACKAGE_VALID=1
else
	WEB_PACKAGE_VALID=0
fi
if [ -z "$WEB_PREPARATION_FAILURES" ] && [ "$WEB_PACKAGE_VALID" -eq 1 ]; then
	record_ok 'complete source validation accepts the two real single-LF web variants'
else
	record_failure 'complete source validation accepts the two real single-LF web variants'
	if [ -n "$WEB_PREPARATION_FAILURES" ]; then
		printf 'fixture preparation rejected:%s\n' "$WEB_PREPARATION_FAILURES" >&2
	fi
	cat "$TEST_TMP/package.stdout" >&2
	cat "$TEST_TMP/package.stderr" >&2
fi

finish_tests
