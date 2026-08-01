#!/bin/sh

set -eu

REPO_ROOT=${1:-.}

if [ ! -d "$REPO_ROOT" ]; then
	printf 'restore-executable-modes: repository root is not a directory: %s\n' "$REPO_ROOT" >&2
	exit 1
fi

EXECUTABLE_PATHS='tests/dpi/run.sh
tests/dpi/test_service_common.sh
tests/dpi/test_boot_delay.sh
tests/dpi/test_service_commands.sh
tests/dpi/test_hotplug.sh
tests/dpi/test_wan_resolver.sh
tests/dpi/test_upload.sh
tests/dpi/test_theme_runtime.sh
tests/dpi/test_firewall_cleanup.sh
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
tests/shell/test_subscription_aggregate.sh'

for relative_path in $EXECUTABLE_PATHS; do
	if [ ! -f "$REPO_ROOT/$relative_path" ] || [ -L "$REPO_ROOT/$relative_path" ]; then
		printf 'restore-executable-modes: missing required file: %s\n' "$relative_path" >&2
		exit 1
	fi
done

find "$REPO_ROOT" \
	-name .git -prune -o \
	-type f -exec chmod 0644 {} +

count=0
for relative_path in $EXECUTABLE_PATHS; do
	if [ -f "$REPO_ROOT/$relative_path" ] && [ ! -L "$REPO_ROOT/$relative_path" ]; then
		chmod 0755 "$REPO_ROOT/$relative_path"
		count=$((count + 1))
	else
		echo "restore-executable-modes: missing $relative_path" >&2
		exit 1
	fi
done

printf 'restore-executable-modes: restored %s executable files\n' "$count"
