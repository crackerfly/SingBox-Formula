#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

RESTORE_SCRIPT="$REPO_ROOT/.github/scripts/restore-executable-modes.sh"
WORKFLOW="$REPO_ROOT/.github/workflows/build.yml"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-web-upload-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

EXPECTED_PATHS="$TEST_TMP/expected-paths"
cat > "$EXPECTED_PATHS" <<'EOF'
tests/dpi/test_theme_runtime.sh
tests/dpi/test_firewall_cleanup.sh
tests/dpi/test_upload.sh
tests/dpi/test_hotplug.sh
tests/dpi/test_service_commands.sh
tests/dpi/test_boot_delay.sh
tests/dpi/test_service_common.sh
tests/dpi/test_wan_resolver.sh
tests/dpi/run.sh
openwrt-feed/luci-app-liquid-formula/root/usr/libexec/rpcd/liquid_formula
openwrt-feed/liquid-formula/files/etc/init.d/liquid-formula
openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula
openwrt-feed/liquid-formula/files/usr/share/liquid-formula/generate-config.sh
openwrt-feed/liquid-formula/files/usr/share/liquid-formula/run-delayed.sh
openwrt-feed/liquid-formula/files/usr/share/liquid-formula/wait-subscription-gateway.sh
openwrt-feed/liquid-formula/files/usr/share/liquid-formula/update.sh
openwrt-feed/liquid-formula/files/usr/share/liquid-formula/validate-template.sh
tests/shell/test_generate_config.sh
tests/shell/test_migration.sh
tests/shell/test_procd_service.sh
tests/shell/test_rpc_contract.sh
tests/shell/test_template_transactions.sh
tests/shell/test_update.sh
tests/shell/test_subscription_normalize.sh
tests/shell/test_subscription_aggregate.sh
openwrt-feed/luci-app-liquid-formula/root/etc/init.d/liquid-formula-logo
openwrt-feed/luci-app-liquid-formula/root/etc/uci-defaults/99-luci-app-liquid-formula
openwrt-feed/luci-app-liquid-formula/root/usr/share/liquid-formula/apply-tuning.sh
openwrt-feed/luci-app-liquid-formula/root/www/cgi-bin/liquid-formula-upload
openwrt-feed/liquid-formula/files/etc/init.d/fakehttp
openwrt-feed/liquid-formula/files/etc/init.d/fakesip
openwrt-feed/liquid-formula/files/etc/init.d/liquid-formula-boot-delay
openwrt-feed/liquid-formula/files/etc/hotplug.d/iface/99-liquid-formula-dpi
openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula-dpi
openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/boot-delay-runner.sh
openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/wan-resolver.sh
EOF

create_web_upload_tree() {
	fixture_root=$1
	initial_mode=$2

	while IFS= read -r relative_path; do
		mkdir -p "$fixture_root/$(dirname "$relative_path")" || return 1
		printf '#!/bin/sh\nexit 0\n' > "$fixture_root/$relative_path" || return 1
		chmod "$initial_mode" "$fixture_root/$relative_path" || return 1
	done < "$EXPECTED_PATHS"

	mkdir -p "$fixture_root/docs" || return 1
	printf 'ordinary file\n' > "$fixture_root/docs/ordinary.txt" || return 1
	chmod "$initial_mode" "$fixture_root/docs/ordinary.txt" || return 1
}

if [ ! -f "$RESTORE_SCRIPT" ]; then
	record_failure "web-upload mode repair script exists (missing: $RESTORE_SCRIPT)"
	finish_tests
	exit $?
fi
record_ok 'web-upload mode repair script exists'

WEB_TREE="$TEST_TMP/web tree"
create_web_upload_tree "$WEB_TREE" 0644 || exit 1
assert_command_success \
	'0644-only web upload tree is repaired when the repair script itself is invoked through sh' \
	sh "$RESTORE_SCRIPT" "$WEB_TREE"

while IFS= read -r relative_path; do
	assert_equal \
		755 \
		"$(stat -c %a "$WEB_TREE/$relative_path")" \
		"restores mode 0755: $relative_path"
done < "$EXPECTED_PATHS"
assert_equal \
	644 \
	"$(stat -c %a "$WEB_TREE/docs/ordinary.txt")" \
	'leaves files outside the executable allowlist at mode 0644'

ALL_EXECUTABLE_TREE="$TEST_TMP/all executable tree"
create_web_upload_tree "$ALL_EXECUTABLE_TREE" 0755 || exit 1
assert_command_success \
	'0755-only web upload tree is repaired when the repair script itself is invoked through sh' \
	sh "$RESTORE_SCRIPT" "$ALL_EXECUTABLE_TREE"
while IFS= read -r relative_path; do
	assert_equal \
		755 \
		"$(stat -c %a "$ALL_EXECUTABLE_TREE/$relative_path")" \
		"keeps allowlisted executable at mode 0755: $relative_path"
done < "$EXPECTED_PATHS"
assert_equal \
	644 \
	"$(stat -c %a "$ALL_EXECUTABLE_TREE/docs/ordinary.txt")" \
	'changes files outside the executable allowlist from mode 0755 to mode 0644'

DEFAULT_TREE="$TEST_TMP/default tree"
create_web_upload_tree "$DEFAULT_TREE" 0644 || exit 1
assert_command_success \
	'omitting repo-root repairs the current directory, including a path containing spaces' \
	sh -c 'cd "$1" && sh "$2"' sh "$DEFAULT_TREE" "$RESTORE_SCRIPT"
assert_equal \
	755 \
	"$(stat -c %a "$DEFAULT_TREE/openwrt-feed/liquid-formula/files/etc/init.d/liquid-formula")" \
	'default current-directory mode restores an allowlisted executable'

MISSING_TREE="$TEST_TMP/missing-tree"
create_web_upload_tree "$MISSING_TREE" 0644 || exit 1
MISSING_PATH='openwrt-feed/liquid-formula/files/usr/share/liquid-formula/generate-config.sh'
rm "$MISSING_TREE/$MISSING_PATH"
if sh "$RESTORE_SCRIPT" "$MISSING_TREE" > "$TEST_TMP/missing.stdout" 2> "$TEST_TMP/missing.stderr"; then
	record_failure 'missing required executable makes mode repair fail'
else
	record_ok 'missing required executable makes mode repair fail'
fi
assert_contains \
	"$TEST_TMP/missing.stderr" \
	'^restore-executable-modes: missing required file: openwrt-feed/liquid-formula/files/usr/share/liquid-formula/generate-config\.sh$' \
	'missing-file error names the exact relative path'
assert_equal \
	644 \
	"$(stat -c %a "$MISSING_TREE/openwrt-feed/luci-app-liquid-formula/root/usr/libexec/rpcd/liquid_formula")" \
	'validates the complete allowlist before changing any mode'

SYMLINK_TREE="$TEST_TMP/allowlisted symlink tree"
create_web_upload_tree "$SYMLINK_TREE" 0755 || exit 1
SYMLINK_PATH='tests/dpi/run.sh'
SYMLINK_TARGET="$TEST_TMP/external symlink target"
printf 'outside the fixture\n' > "$SYMLINK_TARGET" || exit 1
chmod 0600 "$SYMLINK_TARGET" || exit 1
rm "$SYMLINK_TREE/$SYMLINK_PATH" || exit 1
ln -s "$SYMLINK_TARGET" "$SYMLINK_TREE/$SYMLINK_PATH" || exit 1
assert_command_failure \
	'an allowlisted symlink makes mode repair fail' \
	sh "$RESTORE_SCRIPT" "$SYMLINK_TREE"
assert_equal \
	600 \
	"$(stat -c %a "$SYMLINK_TARGET")" \
	'mode repair does not follow an allowlisted symlink outside the repository'
assert_equal \
	755 \
	"$(stat -c %a "$SYMLINK_TREE/docs/ordinary.txt")" \
	'an allowlisted symlink is rejected before changing any fixture mode'

FULL_WEB_TREE="$TEST_TMP/full web upload tree"
FULL_WEB_ARCHIVE="$TEST_TMP/full-web-head.tar"
mkdir -p "$FULL_WEB_TREE" || exit 1
git -C "$REPO_ROOT" archive --format=tar -o "$FULL_WEB_ARCHIVE" HEAD || exit 1
tar -xf "$FULL_WEB_ARCHIVE" -C "$FULL_WEB_TREE" || exit 1
find "$FULL_WEB_TREE" -type f -exec chmod 0755 {} + || exit 1
git -C "$FULL_WEB_TREE" init || exit 1
git -C "$FULL_WEB_TREE" config user.name 'Liquid Formula CI Test' || exit 1
git -C "$FULL_WEB_TREE" config user.email 'ci-test@invalid.example' || exit 1
git -C "$FULL_WEB_TREE" config core.fileMode true || exit 1
git -C "$FULL_WEB_TREE" add -f -A || exit 1
git -C "$FULL_WEB_TREE" commit -m 'web upload fixture' || exit 1
REAL_HEAD_PATHS="$TEST_TMP/real-head.paths"
FULL_WEB_COMMIT_PATHS="$TEST_TMP/full-web-commit.paths"
git -C "$REPO_ROOT" ls-tree -r --name-only HEAD > "$REAL_HEAD_PATHS" || exit 1
git -C "$FULL_WEB_TREE" ls-tree -r --name-only HEAD > "$FULL_WEB_COMMIT_PATHS" || exit 1
assert_files_equal \
	"$REAL_HEAD_PATHS" \
	"$FULL_WEB_COMMIT_PATHS" \
	'full web upload fixture commit contains exactly the paths tracked by real HEAD'
assert_equal \
	1 \
	"$(git -C "$FULL_WEB_TREE" rev-list --count HEAD)" \
	'full web upload fixture has exactly one commit'
FULL_WEB_TREE_NON_EXECUTABLES=$(git -C "$FULL_WEB_TREE" ls-tree -r HEAD | awk '$1 != "100755" { print $1 " " $4 }')
assert_empty \
	"$FULL_WEB_TREE_NON_EXECUTABLES" \
	'full web upload fixture records every tracked file at mode 100755'
FULL_WEB_CLONE="$TEST_TMP/full web upload clone"
git clone --quiet --no-local "$FULL_WEB_TREE" "$FULL_WEB_CLONE" || exit 1
FULL_WEB_GIT_SENTINEL="$FULL_WEB_CLONE/.git/restore-mode-sentinel"
printf 'do not change this Git metadata sentinel\n' > "$FULL_WEB_GIT_SENTINEL" || exit 1
chmod 0600 "$FULL_WEB_GIT_SENTINEL" || exit 1
sh "$FULL_WEB_CLONE/.github/scripts/restore-executable-modes.sh" "$FULL_WEB_CLONE" || exit 1
assert_equal \
	600 \
	"$(stat -c %a "$FULL_WEB_GIT_SENTINEL")" \
	'mode repair leaves files inside .git unchanged'
if sh "$FULL_WEB_CLONE/tests/shell/test_source_package.sh" \
	>"$TEST_TMP/full-web-source.stdout" \
	2>"$TEST_TMP/full-web-source.stderr"; then
	record_failure 'source-package rejects a commit whose tracked files all use mode 100755'
else
	record_ok 'source-package rejects a commit whose tracked files all use mode 100755'
fi
assert_contains \
	"$TEST_TMP/full-web-source.stderr" \
	'every tracked file is committed as 100644' \
	'the rejection explains the repository mode convention'
assert_contains \
	"$TEST_TMP/full-web-source.stderr" \
	'README\.md' \
	'the rejection names an offending tracked file instead of only reporting a tree digest'

checkout_line=$(grep -nF 'uses: actions/checkout@v7' "$WORKFLOW" | head -n 1 | cut -d: -f1)
restore_line=$(grep -nF 'sh .github/scripts/restore-executable-modes.sh "$GITHUB_WORKSPACE"' "$WORKFLOW" | head -n 1 | cut -d: -f1)
setup_go_line=$(grep -nF 'uses: actions/setup-go@v6' "$WORKFLOW" | head -n 1 | cut -d: -f1)
if [ -n "$checkout_line" ] && [ -n "$restore_line" ] && [ -n "$setup_go_line" ] && \
	[ "$checkout_line" -lt "$restore_line" ] && [ "$restore_line" -lt "$setup_go_line" ]; then
	record_ok 'workflow restores modes immediately after checkout and before setup/test steps'
else
	record_failure 'workflow restores modes immediately after checkout and before setup/test steps'
fi

finish_tests
