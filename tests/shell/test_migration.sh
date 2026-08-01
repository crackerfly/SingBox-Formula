#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

MIGRATION_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula"
DPI_MIGRATION_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/uci-defaults/99-liquid-formula-dpi"
PACKAGE_MAKEFILE="$REPO_ROOT/openwrt-feed/liquid-formula/Makefile"
LUCI_MAKEFILE="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/Makefile"
TEMPLATE_PATH='/www/liquid-formula/templates/momo-template.json'

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-migration-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_BIN="$TEST_TMP/bin"
MOCK_ROOT="$TEST_TMP/root"
UCI_STATE="$TEST_TMP/uci.state"
UCI_CALLS="$TEST_TMP/uci.calls"
UCI_TYPES="$TEST_TMP/uci.types"
MIGRATION_UNDER_TEST="$TEST_TMP/99-liquid-formula"
mkdir -p "$MOCK_BIN" \
	"$MOCK_ROOT/etc/config" \
	"$MOCK_ROOT/etc/init.d" \
	"$MOCK_ROOT/etc/singbox-formula" \
	"$MOCK_ROOT/var/log/singbox-formula" \
	"$MOCK_ROOT/var/lib/singbox-formula" \
	"$MOCK_ROOT/var/run/singbox-formula" \
	"$MOCK_ROOT/usr/share/liquid-formula"

printf 'legacy uci sentinel\n' > "$MOCK_ROOT/etc/config/singbox_formula"
printf 'legacy config sentinel\n' > "$MOCK_ROOT/etc/singbox-formula/config.yaml"
printf 'legacy log sentinel\n' > "$MOCK_ROOT/var/log/singbox-formula/server.log"
printf 'legacy state sentinel\n' > "$MOCK_ROOT/var/lib/singbox-formula/state.json"
printf 'legacy runtime sentinel\n' > "$MOCK_ROOT/var/run/singbox-formula/owner"

cat > "$MOCK_ROOT/etc/init.d/singbox-formula" <<EOF
#!/bin/sh
printf 'legacy-init|%s\n' "\$*" >> "$TEST_TMP/legacy-init.calls"
case "\${1:-}" in
	stop) exit "\${MOCK_LEGACY_STOP_RC:-0}" ;;
	disable) exit "\${MOCK_LEGACY_DISABLE_RC:-0}" ;;
esac
exit 2
EOF
chmod 0755 "$MOCK_ROOT/etc/init.d/singbox-formula"
: > "$TEST_TMP/legacy-init.calls"

cat > "$MOCK_BIN/uci" <<'EOF'
#!/bin/sh
set -u

while [ "${1:-}" = "-q" ]; do shift; done
command=${1:-}
[ "$#" -gt 0 ] && shift

case "$command" in
	get)
		key=${1:-}
		type=$(awk -F '\t' -v wanted="$key" '$1 == wanted { value=$2 } END { print value }' "$MOCK_UCI_TYPES")
		if [ "$type" = list ]; then
			awk -F '\t' -v wanted="$key" '
				$1 == wanted { if (seen++) printf " "; printf "%s", substr($0, index($0, "\t") + 1) }
				END { if (!seen) exit 1; print "" }
			' "$MOCK_UCI_STATE"
			exit $?
		fi
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
		if [ "$key" = liquid_formula.main.subscription_url ]; then
			awk -F '\t' -v wanted="$key" '$1 != wanted { print }' "$MOCK_UCI_TYPES" > "$MOCK_UCI_TYPES.tmp.$$"
			printf '%s\toption\n' "$key" >> "$MOCK_UCI_TYPES.tmp.$$"
			mv "$MOCK_UCI_TYPES.tmp.$$" "$MOCK_UCI_TYPES"
		fi
		printf 'set|%s|%s\n' "$key" "$value" >> "$MOCK_UCI_CALLS"
		;;
	delete)
		key=${1:-}
		tmp="$MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".") != 1 { print }
		' "$MOCK_UCI_STATE" > "$tmp"
		mv "$tmp" "$MOCK_UCI_STATE"
		awk -F '\t' -v wanted="$key" '$1 != wanted { print }' "$MOCK_UCI_TYPES" > "$MOCK_UCI_TYPES.tmp.$$"
		mv "$MOCK_UCI_TYPES.tmp.$$" "$MOCK_UCI_TYPES"
		printf 'delete|%s\n' "$key" >> "$MOCK_UCI_CALLS"
		;;
	export)
		printf "package 'liquid_formula'\n\nconfig global 'main'\n"
		key=liquid_formula.main.subscription_url
		type=$(awk -F '\t' -v wanted="$key" '$1 == wanted { value=$2 } END { print value }' "$MOCK_UCI_TYPES")
		[ -n "$type" ] || type=option
		awk -F '\t' -v wanted="$key" -v type="$type" '
			$1 == wanted { printf "\t%s subscription_url \047%s\047\n", type, substr($0, index($0, "\t") + 1) }
		' "$MOCK_UCI_STATE"
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

cat > "$MOCK_ROOT/etc/init.d/liquid-formula" <<EOF
#!/bin/sh
printf 'init|%s\n' "\$*" >> "$TEST_TMP/runtime.calls"
exit 0
EOF
chmod 0755 "$MOCK_ROOT/etc/init.d/liquid-formula"

cat > "$MOCK_ROOT/usr/share/liquid-formula/generate-config.sh" <<EOF
#!/bin/sh
printf 'generate\n' >> "$TEST_TMP/runtime.calls"
exit 0
EOF
chmod 0755 "$MOCK_ROOT/usr/share/liquid-formula/generate-config.sh"

sed \
	-e "s|/etc/config|$MOCK_ROOT/etc/config|g" \
	-e "s|/etc/singbox-formula|$MOCK_ROOT/etc/singbox-formula|g" \
	-e "s|/var/log/singbox-formula|$MOCK_ROOT/var/log/singbox-formula|g" \
	-e "s|/var/lib/singbox-formula|$MOCK_ROOT/var/lib/singbox-formula|g" \
	-e "s|/var/run/singbox-formula|$MOCK_ROOT/var/run/singbox-formula|g" \
	-e "s|/etc/init.d/singbox-formula|$MOCK_ROOT/etc/init.d/singbox-formula|g" \
	-e "s|/etc/liquid-formula|$MOCK_ROOT/etc/liquid-formula|g" \
	-e "s|/var/lib/liquid-formula|$MOCK_ROOT/var/lib/liquid-formula|g" \
	-e "s|/var/log/liquid-formula|$MOCK_ROOT/var/log/liquid-formula|g" \
	-e "s|/www/liquid-formula|$MOCK_ROOT/www/liquid-formula|g" \
	-e "s|/etc/init.d/liquid-formula|$MOCK_ROOT/etc/init.d/liquid-formula|g" \
	-e "s|/usr/share/liquid-formula/generate-config.sh|$MOCK_ROOT/usr/share/liquid-formula/generate-config.sh|g" \
	"$MIGRATION_SOURCE" > "$MIGRATION_UNDER_TEST"
chmod 0755 "$MIGRATION_UNDER_TEST"

PATH="$MOCK_BIN:$PATH"
MOCK_UCI_STATE="$UCI_STATE"
MOCK_UCI_CALLS="$UCI_CALLS"
MOCK_UCI_TYPES="$UCI_TYPES"
export PATH MOCK_UCI_STATE MOCK_UCI_CALLS MOCK_UCI_TYPES \
	MOCK_LEGACY_STOP_RC MOCK_LEGACY_DISABLE_RC

cat > "$UCI_STATE" <<'EOF'
liquid_formula.main	global
liquid_formula.main.enabled	1
liquid_formula.main.port	9000
liquid_formula.main.password	custom-password
liquid_formula.main.default_template	openwrt
liquid_formula.main.output_config	/etc/sing-box/config.json
liquid_formula.openwrt	template
liquid_formula.openwrt.enabled	0
liquid_formula.openwrt.name	Custom Legacy Template
liquid_formula.openwrt.file	custom-openwrt.json
liquid_formula.openwrt.no_node	Custom Direct
EOF
printf 'liquid_formula.main.subscription_url\t\n' >> "$UCI_STATE"
printf 'liquid_formula.main.subscription_url\toption\n' > "$UCI_TYPES"
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

assert_file_content 'legacy uci sentinel' "$MOCK_ROOT/etc/config/liquid_formula" \
	'migration copies the legacy UCI file before applying defaults'
assert_file_content 'legacy uci sentinel' "$MOCK_ROOT/etc/config/singbox_formula.migrated" \
	'migration preserves the renamed legacy UCI file as a backup'
assert_file_content 'legacy config sentinel' "$MOCK_ROOT/etc/singbox-formula/config.yaml" \
	'migration preserves the old configuration namespace for manual comparison'
assert_file_content 'legacy log sentinel' "$MOCK_ROOT/var/log/singbox-formula/server.log" \
	'migration preserves old logs for manual review'
assert_file_content 'legacy state sentinel' "$MOCK_ROOT/var/lib/singbox-formula/state.json" \
	'migration preserves old persistent state for manual review'
assert_file_content 'legacy runtime sentinel' "$MOCK_ROOT/var/run/singbox-formula/owner" \
	'migration does not recursively remove the old runtime namespace'
assert_file_exists "$MOCK_ROOT/etc/init.d/singbox-formula" \
	'migration leaves the disabled legacy init script for explicit cleanup'
assert_contains "$TEST_TMP/legacy-init.calls" '^legacy-init\|stop$' \
	'migration stops the legacy service before preserving it'
assert_contains "$TEST_TMP/legacy-init.calls" '^legacy-init\|disable$' \
	'migration disables the legacy service before preserving it'

: > "$TEST_TMP/legacy-init.calls"
MOCK_LEGACY_STOP_RC=73
MOCK_LEGACY_DISABLE_RC=0
export MOCK_LEGACY_STOP_RC MOCK_LEGACY_DISABLE_RC
if "$MIGRATION_UNDER_TEST" >/dev/null 2>&1; then
	record_failure 'migration rejects a failed legacy-service stop'
else
	record_ok 'migration rejects a failed legacy-service stop'
fi
assert_contains "$TEST_TMP/legacy-init.calls" '^legacy-init\|disable$' \
	'migration still attempts to disable the legacy service after a stop failure'

: > "$TEST_TMP/legacy-init.calls"
MOCK_LEGACY_STOP_RC=0
MOCK_LEGACY_DISABLE_RC=74
export MOCK_LEGACY_STOP_RC MOCK_LEGACY_DISABLE_RC
if "$MIGRATION_UNDER_TEST" >/dev/null 2>&1; then
	record_failure 'migration rejects a failed legacy-service disable'
else
	record_ok 'migration rejects a failed legacy-service disable'
fi
assert_contains "$TEST_TMP/legacy-init.calls" '^legacy-init\|stop$' \
	'migration attempts to stop the legacy service before a disable failure'
MOCK_LEGACY_STOP_RC=0
MOCK_LEGACY_DISABLE_RC=0
export MOCK_LEGACY_STOP_RC MOCK_LEGACY_DISABLE_RC

assert_state liquid_formula.main.port 9000 'migration preserves an explicitly selected legacy port'
assert_state liquid_formula.main.output_config /etc/sing-box/config.json 'migration preserves an explicitly selected legacy output path'
assert_state liquid_formula.main.password custom-password 'migration preserves an explicit password'
assert_state liquid_formula.main.subscription_url '' 'migration preserves an explicitly empty subscription URL'
assert_state liquid_formula.main.default_template openwrt 'migration preserves the explicit default template choice'
assert_state liquid_formula.openwrt.file custom-openwrt.json 'migration does not delete or rewrite a custom legacy template'
assert_state liquid_formula.main.boot_delay 90 'migration fills a missing boot delay'
assert_state liquid_formula.main.subscription_timeout 60 'migration fills a missing subscription timeout'
assert_state liquid_formula.main.template_base_url http://127.0.0.1/liquid-formula/templates 'migration fills a missing template base URL'
assert_state liquid_formula.main.user_agent v2rayN/7.24.4 'migration fills a missing User-Agent with the current v2rayN default'
assert_state liquid_formula.momo_template.file momo-template.json 'migration adds the missing packaged template without removing user sections'
assert_state liquid_formula.momo_template.enabled 1 'migration enables the packaged momo template'
assert_state liquid_formula.localdns_template.file localdns-template.json 'migration adds the missing local DNS template'
assert_state liquid_formula.localdns_template.enabled 1 'migration enables the packaged local DNS template'

: > "$UCI_CALLS"
"$MIGRATION_UNDER_TEST"
assert_empty "$(cat "$UCI_CALLS")" 'a second migration run performs no UCI writes or commit'

# 1.8.5 stored subscription_url as an ordered UCI list. Plan A keeps the first
# item, converts it back to a scalar option and leaves the service switch alone.
cat > "$UCI_STATE" <<'EOF'
liquid_formula.main	global
liquid_formula.main.enabled	1
liquid_formula.main.subscription_url	https://first.example/sub?token=one
liquid_formula.main.subscription_url	https://second.example/sub?token=two
liquid_formula.main.user_agent	sing-box 1.11.0
EOF
printf 'liquid_formula.main.subscription_url\tlist\n' > "$UCI_TYPES"
: > "$UCI_CALLS"
: > "$TEST_TMP/runtime.calls"
"$MIGRATION_UNDER_TEST"
assert_state liquid_formula.main.subscription_url 'https://first.example/sub?token=one' '1.8.5 list migration keeps the first subscription URL'
assert_state liquid_formula.main.enabled 1 '1.8.5 list migration preserves the enabled state'
assert_equal option "$(awk -F '\t' '$1 == "liquid_formula.main.subscription_url" { print $2 }' "$UCI_TYPES")" '1.8.5 list migration converts the URL back to a scalar option'
assert_not_contains "$UCI_STATE" 'second\.example' '1.8.5 list migration discards later subscription URLs under Plan A'

# A gateway-generated YAML is backed up once and regenerated; an ordinary
# custom conffile is not touched merely because the package is upgraded.
mkdir -p "$MOCK_ROOT/etc/liquid-formula"
cat > "$MOCK_ROOT/etc/liquid-formula/config.yaml" <<'EOF'
# Generated by /usr/share/liquid-formula/generate-config.sh
subscription:
  url: 'http://127.0.0.1:9717/v1/aggregate'
liquid_formula_gateway:
  listen_port: 9717
EOF
: > "$TEST_TMP/runtime.calls"
"$MIGRATION_UNDER_TEST"
assert_file_exists "$MOCK_ROOT/etc/liquid-formula/config.yaml.pre-1.8.6" 'gateway YAML is backed up before scalar regeneration'
assert_equal 600 "$(stat -c %a "$MOCK_ROOT/etc/liquid-formula/config.yaml.pre-1.8.6" 2>/dev/null)" 'gateway YAML backup is mode 0600'
assert_contains "$TEST_TMP/runtime.calls" '^generate$' 'gateway YAML migration invokes the scalar generator'
printf 'sentinel backup\n' > "$MOCK_ROOT/etc/liquid-formula/config.yaml.pre-1.8.6"
"$MIGRATION_UNDER_TEST"
assert_file_content 'sentinel backup' "$MOCK_ROOT/etc/liquid-formula/config.yaml.pre-1.8.6" 'gateway YAML backup is never overwritten'

cat > "$MOCK_ROOT/etc/liquid-formula/config.yaml" <<'EOF'
# user-maintained converter configuration
custom: true
EOF
: > "$TEST_TMP/runtime.calls"
"$MIGRATION_UNDER_TEST"
assert_not_contains "$TEST_TMP/runtime.calls" '^generate$' 'ordinary custom config.yaml is preserved without regeneration'

check_ua_migration() {
	old=$1 new=$2 description=$3
	cat > "$UCI_STATE" <<EOF
liquid_formula.main	global
liquid_formula.main.enabled	0
liquid_formula.main.user_agent	$old
EOF
	printf 'liquid_formula.main.subscription_url\t\n' >> "$UCI_STATE"
	printf 'liquid_formula.main.subscription_url\toption\n' > "$UCI_TYPES"
	: > "$UCI_CALLS"
	"$MIGRATION_UNDER_TEST"
	assert_state liquid_formula.main.user_agent "$new" "$description"
}

check_ua_migration 'sing-box 1.11.0' 'sing-box 1.13.15' 'migration refreshes the old sing-box preset'
check_ua_migration 'SFI/1.11.0 (sing-box 1.11.0)' 'SFI/1.13.15 (sing-box 1.13.15)' 'migration refreshes the old SFI preset'
check_ua_migration 'SFA/1.11.0 (sing-box 1.11.0)' 'SFA/1.13.15 (sing-box 1.13.15)' 'migration refreshes the old SFA preset'
check_ua_migration 'SFM/1.11.0 (sing-box 1.11.0)' 'SFM/1.13.15 (sing-box 1.13.15)' 'migration refreshes the old SFM preset'
check_ua_migration 'v2rayN/7.0.0' 'v2rayN/7.24.4' 'migration refreshes the old v2rayN preset'
check_ua_migration 'v2rayNG/1.9.16' 'v2rayNG/2.2.6' 'migration refreshes the old v2rayNG preset'
check_ua_migration 'Karing/1.0.0' 'Karing/1.2.23.2605' 'migration refreshes the old Karing preset'
check_ua_migration 'ProviderCustom/99.1' 'ProviderCustom/99.1' 'migration preserves a custom provider User-Agent'
check_ua_migration '' '' 'migration preserves an explicitly empty User-Agent'

# Exercise the DPI uci-defaults migration as a real script. A fake default
# route is present specifically to prove fresh installs no longer copy a
# guessed /proc device into UCI.
DPI_MOCK_BIN="$TEST_TMP/dpi-bin"
DPI_MOCK_ROOT="$TEST_TMP/dpi-root"
DPI_UCI_STATE="$TEST_TMP/dpi-uci.state"
DPI_UCI_CALLS="$TEST_TMP/dpi-uci.calls"
DPI_ROUTE_FILE="$TEST_TMP/proc-net-route"
DPI_MIGRATION_UNDER_TEST="$TEST_TMP/99-liquid-formula-dpi"
DPI_MOCK_INIT_LOG="$TEST_TMP/dpi-init.calls"
mkdir -p "$DPI_MOCK_BIN" "$DPI_MOCK_ROOT/etc/init.d"

install_dpi_legacy_init() {
	cat >"$DPI_MOCK_ROOT/etc/init.d/taoistfuchen-boot-delay" <<'EOF'
#!/bin/sh
printf 'legacy|%s\n' "${1:-}" >>"$DPI_MOCK_INIT_LOG"
case "${1:-}" in
	stop) exit "${DPI_MOCK_LEGACY_STOP_RC:-0}" ;;
	disable) exit "${DPI_MOCK_LEGACY_DISABLE_RC:-0}" ;;
esac
exit 2
EOF
	chmod 0755 "$DPI_MOCK_ROOT/etc/init.d/taoistfuchen-boot-delay"
}

for dpi_service in liquid-formula-boot-delay fakehttp fakesip; do
	cat >"$DPI_MOCK_ROOT/etc/init.d/$dpi_service" <<'EOF'
#!/bin/sh
printf 'current|%s|%s\n' "${0##*/}" "${1:-}" >>"$DPI_MOCK_INIT_LOG"
exit 0
EOF
	chmod 0755 "$DPI_MOCK_ROOT/etc/init.d/$dpi_service"
done

cat >"$DPI_MOCK_BIN/uci" <<'EOF'
#!/bin/sh
set -u

[ "${1:-}" != -q ] || shift
command=${1:-}
[ "$#" -eq 0 ] || shift

state_value() {
	awk -F '\t' -v wanted="$1" '
		$1 == wanted { print substr($0, index($0, "\t") + 1); found = 1 }
		END { exit(found ? 0 : 1) }
	' "$DPI_MOCK_UCI_STATE"
}

case "$command" in
	get)
		key=${1:-}
		if state_value "$key"; then
			exit 0
		fi
		awk -F '\t' -v prefix="$key.__list." '
			index($1, prefix) == 1 {
				value = substr($0, index($0, "\t") + 1)
				printf "%s%s", count++ ? " " : "", value
			}
			END {
				if (count) print ""
				else exit 1
			}
		' "$DPI_MOCK_UCI_STATE"
		;;
	show)
		package=${1:-}
		awk -F '\t' -v prefix="$package." '
			index($1, prefix) == 1 && $1 ~ /\.__type$/ {
				key = $1
				sub(/\.__type$/, "", key)
				print key "=" substr($0, index($0, "\t") + 1)
			}
		' "$DPI_MOCK_UCI_STATE"
		;;
	set)
		assignment=${1:-}
		key=${assignment%%=*}
		value=${assignment#*=}
		tmp="$DPI_MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".__list.") != 1 { print }
		' "$DPI_MOCK_UCI_STATE" >"$tmp"
		printf '%s\t%s\n' "$key" "$value" >>"$tmp"
		mv "$tmp" "$DPI_MOCK_UCI_STATE"
		printf 'set|%s|%s\n' "$key" "$value" >>"$DPI_MOCK_UCI_CALLS"
		;;
	add_list)
		assignment=${1:-}
		key=${assignment%%=*}
		value=${assignment#*=}
		index=$(awk -F '\t' -v prefix="$key.__list." '
			index($1, prefix) == 1 {
				n = substr($1, length(prefix) + 1) + 0
				if (n > maximum) maximum = n
			}
			END { print maximum + 1 }
		' "$DPI_MOCK_UCI_STATE")
		printf '%s.__list.%s\t%s\n' "$key" "$index" "$value" >>"$DPI_MOCK_UCI_STATE"
		printf 'add_list|%s|%s\n' "$key" "$value" >>"$DPI_MOCK_UCI_CALLS"
		;;
	delete)
		key=${1:-}
		tmp="$DPI_MOCK_UCI_STATE.tmp.$$"
		awk -F '\t' -v wanted="$key" '
			$1 != wanted && index($1, wanted ".__list.") != 1 { print }
		' "$DPI_MOCK_UCI_STATE" >"$tmp"
		mv "$tmp" "$DPI_MOCK_UCI_STATE"
		printf 'delete|%s\n' "$key" >>"$DPI_MOCK_UCI_CALLS"
		;;
	commit)
		printf 'commit|%s\n' "${1:-}" >>"$DPI_MOCK_UCI_CALLS"
		;;
	*)
		exit 2
		;;
esac
EOF
chmod 0755 "$DPI_MOCK_BIN/uci"

printf 'Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n' >"$DPI_ROUTE_FILE"
printf 'stale0\t00000000\t00000000\t0003\t0\t0\t0\t00000000\n' >>"$DPI_ROUTE_FILE"
sed \
	-e "s|/etc/taoistfuchen|$DPI_MOCK_ROOT/etc/taoistfuchen|g" \
	-e "s|/etc/liquid-formula|$DPI_MOCK_ROOT/etc/liquid-formula|g" \
	-e "s|/etc/init.d|$DPI_MOCK_ROOT/etc/init.d|g" \
	-e "s|/proc/net/route|$DPI_ROUTE_FILE|g" \
	"$DPI_MIGRATION_SOURCE" >"$DPI_MIGRATION_UNDER_TEST"
chmod 0755 "$DPI_MIGRATION_UNDER_TEST"

DPI_MOCK_UCI_STATE="$DPI_UCI_STATE"
DPI_MOCK_UCI_CALLS="$DPI_UCI_CALLS"
DPI_MOCK_LEGACY_STOP_RC=0
DPI_MOCK_LEGACY_DISABLE_RC=0
export DPI_MOCK_UCI_STATE DPI_MOCK_UCI_CALLS DPI_MOCK_INIT_LOG \
	DPI_MOCK_LEGACY_STOP_RC DPI_MOCK_LEGACY_DISABLE_RC

dpi_state_value() {
	awk -F '\t' -v wanted="$1" '
		$1 == wanted { print substr($0, index($0, "\t") + 1); found = 1 }
		END { exit(found ? 0 : 1) }
	' "$DPI_UCI_STATE"
}

dpi_list_value() {
	"$DPI_MOCK_BIN/uci" -q get "$1" 2>/dev/null || true
}

reset_dpi_legacy_fixture() {
	rm -rf "$DPI_MOCK_ROOT/etc/taoistfuchen/fakehttp-payloads" \
		"$DPI_MOCK_ROOT/etc/liquid-formula/fakehttp-payloads"
	mkdir -p "$DPI_MOCK_ROOT/etc/taoistfuchen/fakehttp-payloads"
	printf 'legacy payload sentinel\n' > \
		"$DPI_MOCK_ROOT/etc/taoistfuchen/fakehttp-payloads/custom.bin"
	install_dpi_legacy_init
	: >"$DPI_MOCK_INIT_LOG"
	cat >"$DPI_UCI_STATE" <<'EOF'
fakehttp.main.__type	fakehttp
fakehttp.payload1.__type	payload
fakesip.main.__type	fakesip
EOF
	: >"$DPI_UCI_CALLS"
}

reset_dpi_legacy_fixture
DPI_MOCK_LEGACY_STOP_RC=73
DPI_MOCK_LEGACY_DISABLE_RC=0
export DPI_MOCK_LEGACY_STOP_RC DPI_MOCK_LEGACY_DISABLE_RC
if PATH="$DPI_MOCK_BIN:$PATH" "$DPI_MIGRATION_UNDER_TEST" >/dev/null 2>&1; then
	record_failure 'DPI migration rejects a failed legacy-scheduler stop'
else
	record_ok 'DPI migration rejects a failed legacy-scheduler stop'
fi
assert_contains "$DPI_MOCK_INIT_LOG" '^legacy\|disable$' \
	'DPI migration still attempts legacy disable after a stop failure'
assert_file_exists "$DPI_MOCK_ROOT/etc/init.d/taoistfuchen-boot-delay" \
	'DPI migration preserves the legacy init after a stop failure'
assert_file_content 'legacy payload sentinel' \
	"$DPI_MOCK_ROOT/etc/taoistfuchen/fakehttp-payloads/custom.bin" \
	'DPI migration leaves the legacy payload in place after a stop failure'
assert_not_contains "$DPI_MOCK_INIT_LOG" '^current\|' \
	'DPI migration starts no current service after a stop failure'

reset_dpi_legacy_fixture
DPI_MOCK_LEGACY_STOP_RC=0
DPI_MOCK_LEGACY_DISABLE_RC=74
export DPI_MOCK_LEGACY_STOP_RC DPI_MOCK_LEGACY_DISABLE_RC
if PATH="$DPI_MOCK_BIN:$PATH" "$DPI_MIGRATION_UNDER_TEST" >/dev/null 2>&1; then
	record_failure 'DPI migration rejects a failed legacy-scheduler disable'
else
	record_ok 'DPI migration rejects a failed legacy-scheduler disable'
fi
assert_contains "$DPI_MOCK_INIT_LOG" '^legacy\|stop$' \
	'DPI migration attempts legacy stop before a disable failure'
assert_file_exists "$DPI_MOCK_ROOT/etc/init.d/taoistfuchen-boot-delay" \
	'DPI migration preserves the legacy init after a disable failure'
assert_file_content 'legacy payload sentinel' \
	"$DPI_MOCK_ROOT/etc/taoistfuchen/fakehttp-payloads/custom.bin" \
	'DPI migration leaves the legacy payload in place after a disable failure'
assert_not_contains "$DPI_MOCK_INIT_LOG" '^current\|' \
	'DPI migration starts no current service after a disable failure'

reset_dpi_legacy_fixture
DPI_MOCK_LEGACY_STOP_RC=0
DPI_MOCK_LEGACY_DISABLE_RC=0
export DPI_MOCK_LEGACY_STOP_RC DPI_MOCK_LEGACY_DISABLE_RC
cat >"$DPI_UCI_STATE" <<'EOF'
fakehttp.main.__type	fakehttp
fakehttp.payload1.__type	payload
fakesip.main.__type	fakesip
EOF
: >"$DPI_UCI_CALLS"
PATH="$DPI_MOCK_BIN:$PATH" "$DPI_MIGRATION_UNDER_TEST"
assert_contains "$DPI_MOCK_INIT_LOG" '^legacy\|stop$' \
	'DPI migration stops the legacy scheduler before retiring it'
assert_contains "$DPI_MOCK_INIT_LOG" '^legacy\|disable$' \
	'DPI migration disables the legacy scheduler before retiring it'
assert_file_not_exists "$DPI_MOCK_ROOT/etc/init.d/taoistfuchen-boot-delay" \
	'DPI migration removes the legacy init only after a successful retirement'
assert_file_content 'legacy payload sentinel' \
	"$DPI_MOCK_ROOT/etc/liquid-formula/fakehttp-payloads/custom.bin" \
	'DPI migration moves the legacy payload after retiring the old scheduler'
assert_equal auto "$(dpi_state_value fakehttp.main.interface_mode)" \
	'fresh FakeHTTP installation defaults to official WAN auto mode'
assert_equal auto "$(dpi_state_value fakesip.main.interface_mode)" \
	'fresh FakeSIP installation defaults to official WAN auto mode'
assert_equal '' "$(dpi_list_value fakehttp.main.interface)" \
	'fresh FakeHTTP installation does not persist a guessed route device'
assert_equal '' "$(dpi_list_value fakesip.main.interface)" \
	'fresh FakeSIP installation does not persist a guessed route device'
assert_equal 8970 "$(dpi_state_value fakehttp.main.queue_num)" \
	'fresh auto migration retains FakeHTTP NFQUEUE 8970'
assert_equal 8971 "$(dpi_state_value fakesip.main.queue_num)" \
	'fresh auto migration retains FakeSIP NFQUEUE 8971'
assert_equal 53 "$(dpi_state_value fakesip.main.ports)" \
	'fresh auto migration retains FakeSIP default excluded port 53'

# A pre-auto installation with saved manual lists is selected even if its old
# package never wrote interface_mode explicitly. Neither list may be rewritten
# or supplemented by the fake default route.
cat >"$DPI_UCI_STATE" <<'EOF'
fakehttp.main.__type	fakehttp
fakehttp.main.interface.__list.1	manual-http0
fakehttp.payload1.__type	payload
fakesip.main.__type	fakesip
fakesip.main.interface.__list.1	manual-sip0
EOF
: >"$DPI_UCI_CALLS"
PATH="$DPI_MOCK_BIN:$PATH" "$DPI_MIGRATION_UNDER_TEST"
assert_equal selected "$(dpi_state_value fakehttp.main.interface_mode)" \
	'migration classifies an existing FakeHTTP manual list as selected'
assert_equal selected "$(dpi_state_value fakesip.main.interface_mode)" \
	'migration classifies an existing FakeSIP manual list as selected'
assert_equal manual-http0 "$(dpi_list_value fakehttp.main.interface)" \
	'migration preserves the existing FakeHTTP manual list exactly'
assert_equal manual-sip0 "$(dpi_list_value fakesip.main.interface)" \
	'migration preserves the existing FakeSIP manual list exactly'

assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/conffiles' \
	'^/www/liquid-formula/templates/momo-template\.json$' \
	'ships the editable packaged template as a conffile'
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/conffiles' \
	'^/www/liquid-formula/templates/localdns-template\.json$' \
	'ships the editable local DNS template as a conffile'
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/install' \
	'run-delayed\.sh.*run-delayed\.sh' \
	'installs the managed boot delay helper'
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/postinst' \
	'/etc/uci-defaults/99-liquid-formula[[:space:]]+>/dev/null[[:space:]]+2>&1[[:space:]]+\|\|[[:space:]]*exit[[:space:]]+1' \
	'package installation propagates a failed configuration migration'
assert_make_block_contains \
	"$PACKAGE_MAKEFILE" \
	'Package/liquid-formula/postinst' \
	'/etc/uci-defaults/99-liquid-formula-dpi[[:space:]]+>/dev/null[[:space:]]+2>&1[[:space:]]+\|\|[[:space:]]*exit[[:space:]]+1' \
	'package installation propagates a failed DPI migration'

LUCI_POSTINST="$TEST_TMP/luci-postinst.sh"
RPCD_INIT="$TEST_TMP/rpcd"
RPCD_PLUGIN="$TEST_TMP/liquid_formula"

make_named_block "$LUCI_MAKEFILE" 'Package/luci-app-liquid-formula/postinst' | \
	sed \
		-e 's/\$\$/\$/g' \
		-e "s|/usr/libexec/rpcd/liquid_formula|$RPCD_PLUGIN|g" \
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

POSTINST_CONTENT=$(make_named_block "$LUCI_MAKEFILE" 'Package/luci-app-liquid-formula/postinst')
if printf '%s\n' "$POSTINST_CONTENT" | grep -Fq '/etc/init.d/uhttpd'; then
	record_failure 'LuCI postinst does not restart uhttpd'
else
	record_ok 'LuCI postinst does not restart uhttpd'
fi
if printf '%s\n' "$POSTINST_CONTENT" | grep -Fq '/usr/libexec/rpcd/liquid-formula'; then
	record_failure 'LuCI postinst does not chmod a nonexistent rpcd alias'
else
	record_ok 'LuCI postinst does not chmod a nonexistent rpcd alias'
fi

finish_tests
