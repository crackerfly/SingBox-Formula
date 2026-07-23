#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

RESTORE_SCRIPT="$REPO_ROOT/.github/scripts/restore-executable-modes.sh"
WORKFLOW="$REPO_ROOT/.github/workflows/build.yml"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-web-upload-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

EXPECTED_PATHS="$TEST_TMP/expected-paths"
cat > "$EXPECTED_PATHS" <<'EOF'
openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula
openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula
openwrt-feed/singbox-formula/files/etc/uci-defaults/99-singbox-formula
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/run-delayed.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/update.sh
openwrt-feed/singbox-formula/files/usr/share/singbox-formula/validate-template.sh
tests/shell/test_generate_config.sh
tests/shell/test_migration.sh
tests/shell/test_procd_service.sh
tests/shell/test_rpc_contract.sh
tests/shell/test_template_transactions.sh
tests/shell/test_update.sh
EOF

create_web_upload_tree() {
	fixture_root=$1

	while IFS= read -r relative_path; do
		mkdir -p "$fixture_root/$(dirname "$relative_path")" || return 1
		printf '#!/bin/sh\nexit 0\n' > "$fixture_root/$relative_path" || return 1
		chmod 0644 "$fixture_root/$relative_path" || return 1
	done < "$EXPECTED_PATHS"

	mkdir -p "$fixture_root/docs" || return 1
	printf 'ordinary file\n' > "$fixture_root/docs/ordinary.txt" || return 1
	chmod 0644 "$fixture_root/docs/ordinary.txt" || return 1
}

if [ ! -f "$RESTORE_SCRIPT" ]; then
	record_failure "web-upload mode repair script exists (missing: $RESTORE_SCRIPT)"
	finish_tests
	exit $?
fi
record_ok 'web-upload mode repair script exists'

WEB_TREE="$TEST_TMP/web tree"
create_web_upload_tree "$WEB_TREE" || exit 1
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

DEFAULT_TREE="$TEST_TMP/default tree"
create_web_upload_tree "$DEFAULT_TREE" || exit 1
assert_command_success \
	'omitting repo-root repairs the current directory, including a path containing spaces' \
	sh -c 'cd "$1" && sh "$2"' sh "$DEFAULT_TREE" "$RESTORE_SCRIPT"
assert_equal \
	755 \
	"$(stat -c %a "$DEFAULT_TREE/openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula")" \
	'default current-directory mode restores an allowlisted executable'

MISSING_TREE="$TEST_TMP/missing-tree"
create_web_upload_tree "$MISSING_TREE" || exit 1
MISSING_PATH='openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config.sh'
rm "$MISSING_TREE/$MISSING_PATH"
if sh "$RESTORE_SCRIPT" "$MISSING_TREE" > "$TEST_TMP/missing.stdout" 2> "$TEST_TMP/missing.stderr"; then
	record_failure 'missing required executable makes mode repair fail'
else
	record_ok 'missing required executable makes mode repair fail'
fi
assert_contains \
	"$TEST_TMP/missing.stderr" \
	'^restore-executable-modes: missing required file: openwrt-feed/singbox-formula/files/usr/share/singbox-formula/generate-config\.sh$' \
	'missing-file error names the exact relative path'
assert_equal \
	644 \
	"$(stat -c %a "$MISSING_TREE/openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula")" \
	'validates the complete allowlist before changing any mode'

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
