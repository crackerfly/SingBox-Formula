#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
PKG="$ROOT/openwrt-feed/liquid-formula"
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


# --- self-healing after a theme package upgrade -------------------------------
# Theme packages do not declare their .ut templates as conffiles (verified
# against luci-theme-fluent 1.0.8: control.tar.gz ships no conffiles file), so
# an upgrade restores the pristine header and silently drops the injected block.
argon_is_243() { return 0; }
apply_logos

# Healthy tree: check must not rewrite anything, or the watcher would feed
# itself an endless event loop.
for name in bootstrap fluent fluent-login; do
	cp "$TMP/templates/$name" "$TMP/templates/$name.healthy"
done
check
for name in bootstrap fluent fluent-login; do
	cmp "$TMP/templates/$name.healthy" "$TMP/templates/$name"
done

# Simulate `apk upgrade luci-theme-fluent` replacing both templates.
cp "$TMP/templates/fluent.original" "$TMP/templates/fluent"
cp "$TMP/templates/fluent-login.original" "$TMP/templates/fluent-login"
! grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/fluent"
markers_missing
check
grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/fluent"
grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/fluent-login"
[ "$(grep -c 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/bootstrap")" -eq 1 ]

# Disabled feature must stay disabled even if a template looks unpatched.
config_get() {
	local destination="$1" option="$3" value
	case "$option" in
		enable) value=0 ;;
		logo|favicon) value="$TMP/assets/default-logo.svg" ;;
		*) value="${4-}" ;;
	esac
	eval "$destination=\$value"
}
cp "$TMP/templates/fluent.original" "$TMP/templates/fluent"
check
! grep -q 'LFAPP_CUSTOMLOGO_START' "$TMP/templates/fluent"

echo "theme runtime tests: ok"
