#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

MIGRATION_SOURCE="$REPO_ROOT/openwrt-feed/singbox-formula/files/etc/uci-defaults/99-singbox-formula"
PACKAGE_MAKEFILE="$REPO_ROOT/openwrt-feed/singbox-formula/Makefile"
LUCI_MAKEFILE="$REPO_ROOT/openwrt-feed/luci-app-singbox-formula/Makefile"
TEMPLATE_PATH='/www/singbox-formula/templates/momo-template.json'

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-migration-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_BIN="$TEST_TMP/bin"
MOCK_ROOT="$TEST_TMP/root"
UCI_STATE="$TEST_TMP/uci.state"
UCI_CALLS="$TEST_TMP/uci.calls"
MIGRATION_UNDER_TEST="$TEST_TMP/99-singbox-formula"
mkdir -p "$MOCK_BIN" "$MOCK_ROOT/etc/init.d" "$MOCK_ROOT/usr/share/singbox-formula"

cat > "$MOCK_BIN/uci" <<'EOF'
#!/bin/sh
set -u

while [ "${1:-}" = "-q" ]; do shift; done
command=${1:-}
[ "$#" -gt 0 ] && shift

case "$command" in
	get)
		key=${1:-}
		awk -F '\t' -v wanted="$key" '
			$1 == wanted {
				found = 1
				value = substr($0, index($0, "\t") + 1)
			}
			END {
				if (!found) exit 1
				print value
			}
		' "$MOCK_UCI_STATE"
		;;
	set)
		assignment=${1:-}
		key=${assignment%%=*}
		value=${assignment#*=}
		tmp="$MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '$1 != wanted { print }' "$MOCK_UCI_STATE" > "$tmp"
		printf '%s\t%s\n' "$key" "$value" >> "$tmp"
		mv "$tmp" "$MOCK_UCI_STATE"
		printf 'set|%s|%s\n' "$key" "$value" >> "$MOCK_UCI_CALLS"
		;;
	delete)
		key=${1:-}
		tmp="$MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".") != 1 { print }
		' "$MOCK_UCI_STATE" > "$tmp"
		mv "$tmp" "$MOCK_UCI_STATE"
		printf 'delete|%s\n' "$key" >> "$MOCK_UCI_CALLS"
		;;
	commit)
		printf 'commit|%s\n' "${1:-}" >> "$MOCK_UCI_CALLS"
		;;
	*)
		exit 2
		;;
esac
EOF
chmod 0755 "$MOCK_BIN/uci"

cat > "$MOCK_ROOT/etc/init.d/singbox-formula" <<EOF
#!/bin/sh
printf 'init|%s\n' "\$*" >> "$TEST_TMP/runtime.calls"
exit 0
EOF
chmod 0755 "$MOCK_ROOT/etc/init.d/singbox-formula"

cat > "$MOCK_ROOT/usr/share/singbox-formula/generate-config.sh" <<EOF
#!/bin/sh
printf 'generate\n' >> "$TEST_TMP/runtime.calls"
exit 0
EOF
chmod 0755 "$MOCK_ROOT/usr/share/singbox-formula/generate-config.sh"

sed \
	-e "s|/etc/singbox-formula|$MOCK_ROOT/etc/singbox-formula|g" \
	-e "s|/var/lib/singbox-formula|$MOCK_ROOT/var/lib/singbox-formula|g" \
	-e "s|/var/log/singbox-formula|$MOCK_ROOT/var/log/singbox-formula|g" \
	-e "s|/www/singbox-formula|$MOCK_ROOT/www/singbox-formula|g" \
	-e "s|/etc/init.d/singbox-formula|$MOCK_ROOT/etc/init.d/singbox-formula|g" \
	-e "s|/usr/share/singbox-formula/generate-config.sh|$MOCK_ROOT/usr/share/singbox-formula/generate-config.sh|g" \
	"$MIGRATION_SOURCE" > "$MIGRATION_UNDER_TEST"
chmod 0755 "$MIGRATION_UNDER_TEST"

PATH="$MOCK_BIN:$PATH"
MOCK_UCI_STATE="$UCI_STATE"
MOCK_UCI_CALLS="$UCI_CALLS"
export PATH MOCK_UCI_STATE MOCK_UCI_CALLS

cat > "$UCI_STATE" <<'EOF'
singbox_formula.main	global
singbox_formula.main.enabled	1
singbox_formula.main.port	9000
singbox_formula.main.password	custom-password
singbox_formula.main.default_template	openwrt
singbox_formula.main.output_config	/etc/sing-box/config.json
singbox_formula.openwrt	template
singbox_formula.openwrt.enabled	0
singbox_formula.openwrt.name	Custom Legacy Template
singbox_formula.openwrt.file	custom-openwrt.json
singbox_formula.openwrt.no_node	Custom Direct
EOF
printf 'singbox_formula.main.subscription_url\t\n' >> "$UCI_STATE"
: > "$UCI_CALLS"
: > "$TEST_TMP/runtime.calls"

state_value() {
	_key=$1
	awk -F '\t' -v wanted="$_key" '$1 == wanted { print substr($0, index($0, "\t") + 1); found = 1 } END { if (!found) exit 1 }' "$UCI_STATE"
}

assert_state() {
	_key=$1
	_expected=$2
	_description=$3
	_actual=$(state_value "$_key" 2>/dev/null) || _actual='<missing>'
	if [ "$_actual" = "$_expected" ]; then
		record_ok "$_description"
	else
		record_failure "$_description (expected '$_expected', got '$_actual')"
	fi
}

assert_state_exists() {
	_key=$1
	_description=$2
	if state_value "$_key" >/dev/null 2>&1; then
		record_ok "$_description"
	else
		record_failure "$_description (missing: $_key)"
	fi
}

"$MIGRATION_UNDER_TEST"

assert_state singbox_formula.main.port 9000 'migration preserves an explicitly selected legacy port'
assert_state singbox_formula.main.output_config /etc/sing-box/config.json 'migration preserves an explicitly selected legacy output path'
assert_state singbox_formula.main.password custom-password 'migration preserves an explicit password'
assert_state singbox_formula.main.subscription_url '' 'migration preserves an explicitly empty subscription URL'
assert_state singbox_formula.main.default_template openwrt 'migration preserves the explicit default template choice'
assert_state singbox_formula.openwrt.file custom-openwrt.json 'migration does not delete or rewrite a custom legacy template'
assert_state singbox_formula.main.boot_delay 90 'migration fills a missing boot delay'
assert_state singbox_formula.main.subscription_timeout 60 'migration fills a missing subscription timeout'
assert_state singbox_formula.main.template_base_url http://127.0.0.1/singbox-formula/templates 'migration fills a missing template base URL'
assert_state singbox_formula.momo_template.file momo-template.json 'migration adds the missing packaged template without removing user sections'

: > "$UCI_CALLS"
"$MIGRATION_UNDER_TEST"
assert_empty "$(cat "$UCI_CALLS")" 'a second migration run performs no UCI writes or commit'

assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/singbox-formula/conffiles' \
	'^/www/singbox-formula/templates/momo-template\.json$' \
	'ships the editable packaged template as a conffile'
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/singbox-formula/install' \
	'run-delayed\.sh.*run-delayed\.sh' \
	'installs the managed boot delay helper'

LUCI_POSTINST="$TEST_TMP/luci-postinst.sh"
RPCD_INIT="$TEST_TMP/rpcd"
RPCD_PLUGIN="$TEST_TMP/singbox_formula"

make_named_block "$LUCI_MAKEFILE" 'Package/luci-app-singbox-formula/postinst' | \
	sed \
		-e 's/\$\$/\$/g' \
		-e "s|/usr/libexec/rpcd/singbox_formula|$RPCD_PLUGIN|g" \
		-e "s|/etc/init.d/rpcd|$RPCD_INIT|g" \
		-e "s|/tmp/luci-|$TEST_TMP/luci-|g" \
	> "$LUCI_POSTINST"
chmod 0755 "$LUCI_POSTINST"

cat > "$RPCD_INIT" <<'EOF'
#!/bin/sh
exit "${MOCK_RPCD_RC:-0}"
EOF
chmod 0755 "$RPCD_INIT"
printf '#!/bin/sh\nexit 0\n' > "$RPCD_PLUGIN"
chmod 0644 "$RPCD_PLUGIN"

IPKG_INSTROOT=
MOCK_RPCD_RC=1
export IPKG_INSTROOT MOCK_RPCD_RC
if "$LUCI_POSTINST" >/dev/null 2>&1; then
	record_failure 'LuCI postinst propagates rpcd registration failure'
else
	record_ok 'LuCI postinst propagates rpcd registration failure'
fi

MOCK_RPCD_RC=0
export MOCK_RPCD_RC
if "$LUCI_POSTINST" >/dev/null 2>&1; then
	record_ok 'LuCI postinst succeeds after rpcd registration succeeds'
else
	record_failure 'LuCI postinst succeeds after rpcd registration succeeds'
fi
if [ -x "$RPCD_PLUGIN" ]; then
	record_ok 'LuCI postinst makes the real rpcd plugin executable'
else
	record_failure 'LuCI postinst makes the real rpcd plugin executable'
fi

POSTINST_CONTENT=$(make_named_block "$LUCI_MAKEFILE" 'Package/luci-app-singbox-formula/postinst')
if printf '%s\n' "$POSTINST_CONTENT" | grep -Fq '/etc/init.d/uhttpd'; then
	record_failure 'LuCI postinst does not restart uhttpd'
else
	record_ok 'LuCI postinst does not restart uhttpd'
fi
if printf '%s\n' "$POSTINST_CONTENT" | grep -Fq '/usr/libexec/rpcd/singbox-formula'; then
	record_failure 'LuCI postinst does not chmod a nonexistent rpcd alias'
else
	record_ok 'LuCI postinst does not chmod a nonexistent rpcd alias'
fi

finish_tests
