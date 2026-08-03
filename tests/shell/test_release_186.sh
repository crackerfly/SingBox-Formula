#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

MAIN_MAKEFILE="$REPO_ROOT/openwrt-feed/liquid-formula/Makefile"
LUCI_MAKEFILE="$REPO_ROOT/openwrt-feed/luci-app-liquid-formula/Makefile"
README="$REPO_ROOT/README.md"
RELEASE_NOTES="$REPO_ROOT/docs/RELEASE_NOTES_1.8.6.md"
CLEANUP_GUIDE="$REPO_ROOT/docs/CLEANUP_1.5.0_TO_1.8.5.md"
UCI_CONFIG="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/config/liquid_formula"

assert_make_top_level_contains \
	"$MAIN_MAKEFILE" \
	'^[[:space:]]*PKG_VERSION[[:space:]]*:=[[:space:]]*1\.8\.8[[:space:]]*$' \
	"sets the main package version to 1.8.8"
assert_make_top_level_contains \
	"$LUCI_MAKEFILE" \
	'^[[:space:]]*PKG_VERSION[[:space:]]*:=[[:space:]]*1\.8\.8[[:space:]]*$' \
	"sets the LuCI package version to 1.8.8"
assert_contains "$README" 'Current source version:[*][*][[:space:]]*`1\.8\.8`' \
	"publishes 1.8.8 as the documented source version"
assert_contains "$README" '`user_agent`[[:space:]]*\|[[:space:]]*`v2rayN/7\.24\.4`' \
	"documents the current default subscription User-Agent"
assert_contains "$UCI_CONFIG" '^[[:space:]]*option[[:space:]]+subscription_url[[:space:]]' \
	"keeps the source subscription as one scalar option"
assert_not_contains "$UCI_CONFIG" '^[[:space:]]*list[[:space:]]+subscription_url[[:space:]]' \
	"does not reintroduce a subscription URL list"
assert_file_not_exists \
	"$REPO_ROOT/openwrt-feed/liquid-formula/src-subscription-gateway" \
	"does not ship the abandoned subscription gateway source"
assert_file_not_exists \
	"$REPO_ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula/wait-subscription-gateway.sh" \
	"does not ship the abandoned gateway wait wrapper"

assert_file_exists "$RELEASE_NOTES" "ships the 1.8.6 release notes"
assert_contains "$RELEASE_NOTES" '方案 A|Plan A' \
	"release notes document the ordered first-URL migration"
assert_contains "$RELEASE_NOTES" 'v2rayN/7\.24\.4' \
	"release notes document the refreshed User-Agent default"
assert_contains "$RELEASE_NOTES" 'cake_mq' \
	"release notes document the gated Tuning profile"
assert_contains "$RELEASE_NOTES" 'WAN' \
	"release notes document official WAN resolution"
assert_contains "$RELEASE_NOTES" 'localdns_template' \
	"release notes document the packaged local-DNS template"
assert_contains "$RELEASE_NOTES" '100644' \
	"release notes document the Git mode integrity rule"
assert_contains "$RELEASE_NOTES" '保留.*旧.*命名空间|preserv.*legacy.*namespace' \
	"release notes document preservation of legacy data for manual comparison"

assert_file_exists "$CLEANUP_GUIDE" "ships the 1.5.0-to-1.8.5 cleanup guide"
assert_contains "$CLEANUP_GUIDE" '/usr/bin/liquid-formula-subscription-gateway' \
	"cleanup guide lists the obsolete gateway binary"
assert_contains "$CLEANUP_GUIDE" '/var/lib/liquid-formula/subscriptions/' \
	"cleanup guide lists the sensitive aggregation cache"
assert_contains "$CLEANUP_GUIDE" '/etc/config/liquid_formula' \
	"cleanup guide protects the active converter UCI file"
assert_contains "$CLEANUP_GUIDE" '/var/lib/liquid-formula/cache/' \
	"cleanup guide protects the active converter cache"
assert_contains "$CLEANUP_GUIDE" '符号链接|symbolic link' \
	"cleanup guide requires rejecting symbolic-link cleanup targets"
assert_contains "$CLEANUP_GUIDE" '安装 1\.8\.6 前|升级前' \
	"cleanup guide puts legacy-data backup before the 1.8.6 post-install migration"
assert_contains "$CLEANUP_GUIDE" '/etc/init\.d/liquid-formula-boot-delay.*stop|先停止.*liquid-formula-boot-delay' \
	"cleanup guide stops the DPI boot scheduler before its child services"
assert_contains "$CLEANUP_GUIDE" 'flock.*非阻塞|非阻塞.*flock' \
	"cleanup guide requires exclusive lock ownership before removing gateway locks"
assert_contains "$CLEANUP_GUIDE" 'subscription\.lock.*持锁.*subscription\.lock\.barrier|subscription\.lock\.barrier.*subscription\.lock.*持锁' \
	"cleanup guide handles the barrier marker inside the real subscription lock window"
assert_contains "$CLEANUP_GUIDE" 'taoistfuchen.*迁移|TaoistFuchen.*迁移' \
	"cleanup guide distinguishes migrated legacy DPI paths from preserved converter data"
assert_contains "$CLEANUP_GUIDE" '/var/run/liquid-formula-boot-delay/' \
	"cleanup guide protects the current DPI boot-delay state directory"
assert_contains "$CLEANUP_GUIDE" '/var/run/liquid-formula-upload/' \
	"cleanup guide protects the current upload transaction directory"
assert_contains "$CLEANUP_GUIDE" '/etc/liquid-formula/\.config\.yaml\.pre-1\.8\.6\.\*' \
	"cleanup guide lists interrupted sensitive gateway-backup staging files"
assert_contains "$CLEANUP_GUIDE" '/etc/config/irqbalance' \
	"cleanup guide includes irqbalance in Tuning backup and preservation"
assert_contains "$CLEANUP_GUIDE" '1\.5\.0.*1\.8\.2.*无法|1\.5\.0.*1\.8\.2.*不能' \
	"cleanup guide states the evidence limit for early historical releases"

finish_tests
