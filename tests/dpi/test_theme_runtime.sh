#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
PKG="$ROOT/openwrt-feed/liquid-formula-dpi"
INIT="$ROOT/openwrt-feed/luci-app-liquid-formula/root/etc/init.d/liquid-formula-logo"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

mkdir -p "$TMP/templates" "$TMP/www"
for name in bootstrap argon-lua argon-lua-login argon-ut argon-ut-login fluent fluent-login; do
	printf '<!doctype html>\n<html>\n<head>\n<title>%s</title>\n</head>\n<body></body>\n</html>\n' "$name" \
		>"$TMP/templates/$name"
	cp "$TMP/templates/$name" "$TMP/templates/$name.original"
done


sed \
	-e "s#^ASSET_DIR=.*#ASSET_DIR=\"$TMP/assets\"#" \
	-e "s#^PACKAGE_DEFAULT=.*#PACKAGE_DEFAULT=\"$ROOT/openwrt-feed/luci-app-liquid-formula/root/usr/share/liquid-formula/assets/default-logo.svg\"#" \
	-e "s#^RUNTIME_SOURCE=.*#RUNTIME_SOURCE=\"$ROOT/openwrt-feed/luci-app-liquid-formula/root/www/luci-static/liquid-formula/customlogo-runtime.js\"#" \
	-e "s#^RUNTIME_DIR=.*#RUNTIME_DIR=\"$TMP/www/customlogo\"#" \
	-e "s#^BOOTSTRAP_HEADER=.*#BOOTSTRAP_HEADER=\"$TMP/templates/bootstrap\"#" \
	-e "s#^ARGON_LUA_HEADER=.*#ARGON_LUA_HEADER=\"$TMP/templates/argon-lua\"#" \
	-e "s#^ARGON_LUA_LOGIN=.*#ARGON_LUA_LOGIN=\"$TMP/templates/argon-lua-login\"#" \
	-e "s#^ARGON_UCODE_HEADER=.*#ARGON_UCODE_HEADER=\"$TMP/templates/argon-ut\"#" \
	-e "s#^ARGON_UCODE_LOGIN=.*#ARGON_UCODE_LOGIN=\"$TMP/templates/argon-ut-login\"#" \
	-e "s#^FLUENT_HEADER=.*#FLUENT_HEADER=\"$TMP/templates/fluent\"#" \
	-e "s#^FLUENT_LOGIN=.*#FLUENT_LOGIN=\"$TMP/templates/fluent-login\"#" \
	"$INIT" >"$TMP/taoistfuchen"

logger() { :; }
config_load() { :; }
config_get() {
	local destination="$1" option="$3" value
	case "$option" in
		enable) value=1 ;;
		logo|favicon) value="$TMP/assets/default-logo.svg" ;;
		*) value="${4-}" ;;
	esac
	eval "$destination=\$value"
}

# shellcheck source=/dev/null
. "$TMP/taoistfuchen"
argon_is_243() { return 0; }

apply_logos

for name in bootstrap argon-lua argon-lua-login argon-ut argon-ut-login fluent fluent-login; do
	[ "$(grep -c 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/$name")" -eq 1 ]
	start_line="$(grep -n 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/$name" | cut -d: -f1)"
	head_line="$(grep -n '</head>' "$TMP/templates/$name" | cut -d: -f1)"
	[ "$start_line" -lt "$head_line" ]
done
[ -f "$TMP/www/customlogo/runtime.js" ]
[ -f "$TMP/www/customlogo/runtime.css" ]
[ -f "$TMP/www/customlogo/logo.svg" ]
[ -f "$TMP/www/customlogo/favicon.svg" ]

# Reapplying must replace, not duplicate, the marker block.
apply_logos
for name in bootstrap argon-lua argon-lua-login argon-ut argon-ut-login fluent fluent-login; do
	[ "$(grep -c 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/$name")" -eq 1 ]
done

stop_service
[ ! -e "$TMP/www/customlogo" ]
for name in bootstrap argon-lua argon-lua-login argon-ut argon-ut-login fluent fluent-login; do
	cmp "$TMP/templates/$name.original" "$TMP/templates/$name"
done

# Versions other than exact Argon 2.4.3 are deliberately left untouched.
argon_is_243() { return 1; }
apply_logos
grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/bootstrap"
grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/fluent"
! grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/argon-lua"
! grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/argon-ut"
stop_service

echo "theme runtime tests: ok"
