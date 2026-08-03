#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"
. "$SCRIPT_DIR/source_manifest.sh"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-manifest-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM
CONVERTER_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/src"
FROZEN_SOURCE_MANIFEST="$SCRIPT_DIR/fixtures/converter-source-1.8.8.manifest"

BACKSLASH_ROOT="$TEST_TMP/backslash source"
BACKSLASH_PATH='nested/back\slash.txt'
BACKSLASH_MANIFEST="$TEST_TMP/backslash.manifest"
mkdir -p "$BACKSLASH_ROOT/nested" || exit 1
printf 'backslash payload\n' > "$BACKSLASH_ROOT/$BACKSLASH_PATH" || exit 1
assert_command_success \
	"a backslash-bearing source path can be manifested" \
	write_source_manifest "$BACKSLASH_ROOT" "$BACKSLASH_MANIFEST"
assert_file_line_count \
	1 \
	"$BACKSLASH_MANIFEST" \
	"a backslash-bearing source produces one manifest record"
IFS="$(printf '\t')" read -r manifest_path manifest_mode manifest_hash < "$BACKSLASH_MANIFEST"
assert_equal \
	"$BACKSLASH_PATH" \
	"$manifest_path" \
	"the manifest preserves a backslash-bearing relative path"
assert_equal \
	100644 \
	"$manifest_mode" \
	"the manifest records the backslash-bearing source mode"
assert_equal \
	01d564795d5c1dd2d63325b9ab2ba2a1b364199da49703a59e7b253598d5ec45 \
	"$manifest_hash" \
	"the manifest hashes backslash-bearing source content correctly"
assert_equal \
	64 \
	"${#manifest_hash}" \
	"the manifest hash has exactly 64 characters"
case $manifest_hash in
	*[!0-9a-f]*)
		record_failure "the manifest hash contains only lowercase hexadecimal characters"
		;;
	*)
		record_ok "the manifest hash contains only lowercase hexadecimal characters"
		;;
esac

IN_ROOT_CREATE="$TEST_TMP/in-root-create"
mkdir -p "$IN_ROOT_CREATE" || exit 1
printf 'source\n' > "$IN_ROOT_CREATE/source.txt" || exit 1
assert_command_failure \
	"an output inside the source root is rejected before creation" \
	write_source_manifest "$IN_ROOT_CREATE" "$IN_ROOT_CREATE/new.manifest"
assert_file_not_exists \
	"$IN_ROOT_CREATE/new.manifest" \
	"rejecting an in-root output does not create it"
IN_ROOT_CREATE_TEMPS=$(find "$IN_ROOT_CREATE" -maxdepth 1 -type f \
	\( -name '.new.manifest.unsorted.*' -o -name '.new.manifest.sorted.*' \) -print)
assert_empty \
	"$IN_ROOT_CREATE_TEMPS" \
	"rejecting an in-root output leaves no temporary files"

IN_ROOT_EXISTING="$TEST_TMP/in-root-existing"
mkdir -p "$IN_ROOT_EXISTING" || exit 1
printf 'source\n' > "$IN_ROOT_EXISTING/source.txt" || exit 1
printf 'preserve existing output\n' > "$IN_ROOT_EXISTING/existing.manifest" || exit 1
assert_command_failure \
	"an existing output inside the source root is rejected" \
	write_source_manifest "$IN_ROOT_EXISTING" "$IN_ROOT_EXISTING/existing.manifest"
assert_file_content \
	"preserve existing output" \
	"$IN_ROOT_EXISTING/existing.manifest" \
	"rejecting an in-root output preserves existing content"
IN_ROOT_EXISTING_TEMPS=$(find "$IN_ROOT_EXISTING" -maxdepth 1 -type f \
	\( -name '.existing.manifest.unsorted.*' -o -name '.existing.manifest.sorted.*' \) -print)
assert_empty \
	"$IN_ROOT_EXISTING_TEMPS" \
	"rejecting an existing in-root output leaves no temporary files"

DIRECTORY_SOURCE="$TEST_TMP/directory-source"
DIRECTORY_OUTPUT="$TEST_TMP/output-directory"
mkdir -p "$DIRECTORY_SOURCE" "$DIRECTORY_OUTPUT" || exit 1
printf 'source\n' > "$DIRECTORY_SOURCE/source.txt" || exit 1
assert_command_failure \
	"an existing directory is rejected as the manifest output" \
	write_source_manifest "$DIRECTORY_SOURCE" "$DIRECTORY_OUTPUT"
DIRECTORY_OUTPUT_CONTENTS=$(find "$DIRECTORY_OUTPUT" -mindepth 1 -print)
assert_empty \
	"$DIRECTORY_OUTPUT_CONTENTS" \
	"rejecting a directory output leaves the directory unchanged"

TAB=$(printf '\t')
AMBIGUOUS_TAB_ROOT="$TEST_TMP/ambiguous-tab"
mkdir -p "$AMBIGUOUS_TAB_ROOT" || exit 1
printf 'ordinary\n' >"$AMBIGUOUS_TAB_ROOT/ordinary.txt" || exit 1
printf 'ambiguous\n' >"$AMBIGUOUS_TAB_ROOT/name${TAB}mode.txt" || exit 1
assert_command_failure \
	"a tab-bearing path that would corrupt TSV fields is rejected" \
	write_source_manifest "$AMBIGUOUS_TAB_ROOT" "$TEST_TMP/ambiguous-tab.manifest"
assert_file_not_exists \
	"$TEST_TMP/ambiguous-tab.manifest" \
	"rejecting a tab-bearing path publishes no ambiguous manifest"

AMBIGUOUS_NEWLINE_ROOT="$TEST_TMP/ambiguous-newline"
AMBIGUOUS_NEWLINE_PATH='line
break.txt'
mkdir -p "$AMBIGUOUS_NEWLINE_ROOT" || exit 1
printf 'ambiguous\n' >"$AMBIGUOUS_NEWLINE_ROOT/$AMBIGUOUS_NEWLINE_PATH" || exit 1
assert_command_failure \
	"a newline-bearing path that would split manifest records is rejected" \
	write_source_manifest "$AMBIGUOUS_NEWLINE_ROOT" "$TEST_TMP/ambiguous-newline.manifest"
assert_file_not_exists \
	"$TEST_TMP/ambiguous-newline.manifest" \
	"rejecting a newline-bearing path publishes no split manifest"

ATOMIC_ROOT="$TEST_TMP/atomic-source"
ATOMIC_OUTPUT="$TEST_TMP/atomic.manifest"
ATOMIC_FAIL_BIN="$TEST_TMP/fail-bin"
mkdir -p "$ATOMIC_ROOT" "$ATOMIC_FAIL_BIN" || exit 1
printf 'source\n' >"$ATOMIC_ROOT/source.txt" || exit 1
printf 'preserve published manifest\n' >"$ATOMIC_OUTPUT" || exit 1
cat >"$ATOMIC_FAIL_BIN/mv" <<'EOF'
#!/bin/sh
exit 73
EOF
chmod 0755 "$ATOMIC_FAIL_BIN/mv" || exit 1
assert_command_failure \
	"a failed atomic publication reports failure" \
	env PATH="$ATOMIC_FAIL_BIN:$PATH" sh -c '. "$1"; write_source_manifest "$2" "$3"' sh \
	"$SCRIPT_DIR/source_manifest.sh" "$ATOMIC_ROOT" "$ATOMIC_OUTPUT"
assert_file_content \
	"preserve published manifest" \
	"$ATOMIC_OUTPUT" \
	"a failed publication preserves the previous manifest"
ATOMIC_TEMPS=$(find "$TEST_TMP" -maxdepth 1 -type f \
	\( -name '.atomic.manifest.unsorted.*' -o -name '.atomic.manifest.sorted.*' \) -print)
assert_empty \
	"$ATOMIC_TEMPS" \
	"a failed publication removes temporary manifest files"

BASELINE_MANIFEST="$TEST_TMP/converter-baseline.manifest"
assert_command_success \
	"the archived converter tree can be manifested without Git history" \
	write_source_manifest "$CONVERTER_SOURCE" "$BASELINE_MANIFEST"
assert_files_equal \
	"$FROZEN_SOURCE_MANIFEST" \
	"$BASELINE_MANIFEST" \
	"the archived converter tree matches the frozen path/mode/SHA manifest"

BYTE_ROOT="$TEST_TMP/converter-byte-change"
cp -a "$CONVERTER_SOURCE" "$BYTE_ROOT" || exit 1
printf '\nbyte tamper\n' >>"$BYTE_ROOT/main.go" || exit 1
write_source_manifest "$BYTE_ROOT" "$TEST_TMP/converter-byte-change.manifest" || exit 1
if cmp -s "$FROZEN_SOURCE_MANIFEST" "$TEST_TMP/converter-byte-change.manifest"; then
	record_failure "a converter byte change alters the generated manifest"
else
	record_ok "a converter byte change alters the generated manifest"
fi

DELETE_ROOT="$TEST_TMP/converter-deletion"
cp -a "$CONVERTER_SOURCE" "$DELETE_ROOT" || exit 1
rm "$DELETE_ROOT/pkg/fileurl/url.go" || exit 1
write_source_manifest "$DELETE_ROOT" "$TEST_TMP/converter-deletion.manifest" || exit 1
if cmp -s "$FROZEN_SOURCE_MANIFEST" "$TEST_TMP/converter-deletion.manifest"; then
	record_failure "a converter source deletion alters the generated manifest"
else
	record_ok "a converter source deletion alters the generated manifest"
fi

MODE_ROOT="$TEST_TMP/converter-mode-change"
cp -a "$CONVERTER_SOURCE" "$MODE_ROOT" || exit 1
chmod 0755 "$MODE_ROOT/main.go" || exit 1
write_source_manifest "$MODE_ROOT" "$TEST_TMP/converter-mode-change.manifest" || exit 1
if cmp -s "$FROZEN_SOURCE_MANIFEST" "$TEST_TMP/converter-mode-change.manifest"; then
	record_failure "a converter mode change alters the generated manifest"
else
	record_ok "a converter mode change alters the generated manifest"
fi

finish_tests
