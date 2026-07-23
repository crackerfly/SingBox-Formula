#!/bin/sh

set -eu

REPO_ROOT=${1:-.}

if [ ! -d "$REPO_ROOT" ]; then
	printf 'restore-executable-modes: repository root is not a directory: %s\n' "$REPO_ROOT" >&2
	exit 1
fi

EXECUTABLE_PATHS='openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula
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
tests/shell/test_update.sh'

for relative_path in $EXECUTABLE_PATHS; do
	if [ ! -f "$REPO_ROOT/$relative_path" ]; then
		printf 'restore-executable-modes: missing required file: %s\n' "$relative_path" >&2
		exit 1
	fi
done

for relative_path in $EXECUTABLE_PATHS; do
	chmod 0755 "$REPO_ROOT/$relative_path"
done

printf 'restore-executable-modes: restored 13 executable files\n'
