#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
COMMON="$ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/service-common.sh"
RESOLVER="$ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/wan-resolver.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

LFDPI_SYS_CLASS_NET="$TMP/sys/class/net"
LFDPI_BOOT_STATE_DIR="$TMP/state"
LFDPI_NETWORK_HELPERS="$TMP/network.sh"
CALL_LOG="$TMP/network.calls"
mkdir -p "$LFDPI_SYS_CLASS_NET"
: >"$CALL_LOG"
export LFDPI_SYS_CLASS_NET LFDPI_BOOT_STATE_DIR LFDPI_NETWORK_HELPERS

cat >"$LFDPI_NETWORK_HELPERS" <<'EOF'
network_flush_cache() {
	printf 'flush\n' >>"$CALL_LOG"
}

network_find_wan() {
	printf 'find4\n' >>"$CALL_LOG"
	[ -n "${WAN4_NETWORK:-}" ] || return 1
	eval "$1=\$WAN4_NETWORK"
}

network_find_wan6() {
	printf 'find6\n' >>"$CALL_LOG"
	[ -n "${WAN6_NETWORK:-}" ] || return 1
	eval "$1=\$WAN6_NETWORK"
}

network_get_device() {
	local destination="$1" network="$2" device=''

	printf 'device %s\n' "$network" >>"$CALL_LOG"
	case "$network" in
		"${WAN4_NETWORK:-__missing4}") device="${WAN4_DEVICE:-}" ;;
		"${WAN6_NETWORK:-__missing6}") device="${WAN6_DEVICE:-}" ;;
	esac
	[ -n "$device" ] || return 1
	eval "$destination=\$device"
}
EOF

if [ ! -r "$RESOLVER" ]; then
	echo "WAN resolver helper is missing: $RESOLVER" >&2
	exit 1
fi

# shellcheck source=/dev/null
. "$COMMON"
# shellcheck source=/dev/null
. "$RESOLVER"

fail() {
	echo "WAN resolver test failed: $*" >&2
	exit 1
}

assert_equal() {
	[ "$1" = "$2" ] || fail "$3 (expected '$2', got '$1')"
}

assert_success() {
	"$@" || fail "expected success: $*"
}

assert_failure() {
	if "$@"; then
		fail "expected failure: $*"
	fi
}

make_device() {
	mkdir -p "$LFDPI_SYS_CLASS_NET/$1"
}

reset_case() {
	WAN4_NETWORK=''
	WAN4_DEVICE=''
	WAN6_NETWORK=''
	WAN6_DEVICE=''
	DEVICE=''
	INTERFACE=''
	CFG_main_interface=''
	TF_WAN_IPV4_NETWORK=stale4
	TF_WAN_IPV4_DEVICE=stale4
	TF_WAN_IPV6_NETWORK=stale6
	TF_WAN_IPV6_DEVICE=stale6
	TF_WAN_DEVICES=stale-device
	rm -rf "$LFDPI_SYS_CLASS_NET"
	mkdir -p "$LFDPI_SYS_CLASS_NET"
	: >"$CALL_LOG"
}

# This function is the public contract consumed by both init scripts. It must
# clear prior outputs, resolve only the requested families through network.sh,
# and expose the actual L3 devices in IPv4-then-IPv6 order.
run_resolve() {
	tf_wan_resolve "$1"
}

reset_case
make_device pppoe-wan
WAN4_NETWORK=wan
WAN4_DEVICE=pppoe-wan
assert_success run_resolve ipv4
assert_equal "${TF_WAN_IPV4_NETWORK:-}" wan "PPPoE keeps the logical IPv4 network"
assert_equal "${TF_WAN_IPV4_DEVICE:-}" pppoe-wan "PPPoE resolves its actual L3 device"
assert_equal "${TF_WAN_IPV6_NETWORK:-}" '' "IPv4 mode does not retain stale IPv6 state"
assert_equal "${TF_WAN_IPV6_DEVICE:-}" '' "IPv4 mode does not resolve IPv6"
assert_equal "${TF_WAN_DEVICES:-}" pppoe-wan "IPv4 exposes one resolved device"
assert_equal "$(cat "$CALL_LOG")" "flush
find4
device wan" "IPv4 uses only the official IPv4 resolver chain"

reset_case
make_device eth1
WAN4_NETWORK=wan
WAN4_DEVICE=eth1
assert_success run_resolve ipv4
assert_equal "${TF_WAN_DEVICES:-}" eth1 "DHCP/static WAN resolves its L3 device"

reset_case
make_device eth2
WAN6_NETWORK=wan6
WAN6_DEVICE=eth2
assert_success run_resolve ipv6
assert_equal "${TF_WAN_IPV4_NETWORK:-}" '' "IPv6 mode does not resolve IPv4"
assert_equal "${TF_WAN_IPV4_DEVICE:-}" '' "IPv6 mode clears stale IPv4 state"
assert_equal "${TF_WAN_IPV6_NETWORK:-}" wan6 "IPv6 keeps the logical network"
assert_equal "${TF_WAN_IPV6_DEVICE:-}" eth2 "IPv6 resolves its actual L3 device"
assert_equal "${TF_WAN_DEVICES:-}" eth2 "IPv6 exposes one resolved device"
assert_equal "$(cat "$CALL_LOG")" "flush
find6
device wan6" "IPv6 uses only the official IPv6 resolver chain"

reset_case
make_device pppoe-wan
WAN4_NETWORK=wan
WAN4_DEVICE=pppoe-wan
WAN6_NETWORK=wan6
WAN6_DEVICE=pppoe-wan
assert_success run_resolve dual
assert_equal "${TF_WAN_IPV4_DEVICE:-}" pppoe-wan "dual stack retains the IPv4 mapping"
assert_equal "${TF_WAN_IPV6_DEVICE:-}" pppoe-wan "dual stack retains the IPv6 mapping"
assert_equal "${TF_WAN_DEVICES:-}" pppoe-wan "same-device dual stack is de-duplicated"
assert_equal "$(cat "$CALL_LOG")" "flush
find4
device wan
find6
device wan6" "dual stack flushes once and resolves both official defaults"

reset_case
make_device eth1
make_device eth2
WAN4_NETWORK=wan
WAN4_DEVICE=eth1
WAN6_NETWORK=wan6
WAN6_DEVICE=eth2
assert_success run_resolve dual
assert_equal "${TF_WAN_DEVICES:-}" "eth1 eth2" "distinct dual-stack defaults keep stable family order"

reset_case
make_device eth2
WAN6_NETWORK=wan6
WAN6_DEVICE=eth2
assert_success run_resolve dual
assert_equal "${TF_WAN_IPV4_DEVICE:-}" '' "dual stack tolerates IPv4 loss"
assert_equal "${TF_WAN_IPV6_DEVICE:-}" eth2 "dual stack keeps the available IPv6 family"
assert_equal "${TF_WAN_DEVICES:-}" eth2 "one-family loss still exposes the available device"

reset_case
assert_failure run_resolve dual
assert_equal "${TF_WAN_IPV4_NETWORK:-}" '' "no-WAN clears stale IPv4 network state"
assert_equal "${TF_WAN_IPV4_DEVICE:-}" '' "no-WAN clears stale IPv4 device state"
assert_equal "${TF_WAN_IPV6_NETWORK:-}" '' "no-WAN clears stale IPv6 network state"
assert_equal "${TF_WAN_IPV6_DEVICE:-}" '' "no-WAN clears stale IPv6 device state"
assert_equal "${TF_WAN_DEVICES:-}" '' "no-WAN exposes no device"

# An arbitrary hotplug DEVICE and a stale UCI interface are deliberately valid
# and present. Official resolver failure must still fail instead of using them
# (or guessing from /proc/net/route).
reset_case
make_device hotplug0
make_device old0
DEVICE=hotplug0
INTERFACE=wan
CFG_main_interface=old0
assert_failure run_resolve ipv4
assert_equal "${TF_WAN_DEVICES:-}" '' "auto mode never falls back to DEVICE, UCI, or route guesses"
assert_equal "$(cat "$CALL_LOG")" "flush
find4" "failed official lookup does not query a guessed network"

reset_case
WAN4_NETWORK=wan
WAN4_DEVICE='wan0;reboot'
assert_failure run_resolve ipv4
assert_equal "${TF_WAN_DEVICES:-}" '' "invalid L3 device names fail closed"

reset_case
WAN4_NETWORK=wan
WAN4_DEVICE=ghost0
assert_failure run_resolve ipv4
assert_equal "${TF_WAN_DEVICES:-}" '' "unavailable L3 devices fail closed"

assert_failure run_resolve invalid

# Runtime mappings are part of the service lifecycle transaction. Neither a
# hotplug classifier nor any future unlocked caller may publish or clear them.
reset_case
make_device pppoe-wan
WAN4_NETWORK=wan
WAN4_DEVICE=pppoe-wan
WAN6_NETWORK=wan6
WAN6_DEVICE=pppoe-wan
assert_success run_resolve dual
assert_failure tf_wan_state_set fakehttp
assert_success tf_lifecycle_lock_acquire fakehttp
assert_success tf_wan_state_set fakehttp
assert_success tf_lifecycle_lock_release fakehttp
saved_state="$(cat "$LFDPI_BOOT_STATE_DIR/fakehttp.wan")"
TF_WAN_IPV4_NETWORK=wrong
TF_WAN_IPV4_DEVICE=pppoe-wan
assert_failure tf_wan_state_set fakehttp
assert_equal "$(cat "$LFDPI_BOOT_STATE_DIR/fakehttp.wan")" "$saved_state" \
	"unlocked state set cannot replace the last successful mapping"
assert_failure tf_wan_state_clear fakehttp
[ -f "$LFDPI_BOOT_STATE_DIR/fakehttp.wan" ] ||
	fail "unlocked state clear removed the last successful mapping"
assert_success tf_lifecycle_lock_acquire fakehttp
assert_success tf_wan_state_clear fakehttp
assert_success tf_lifecycle_lock_release fakehttp
[ ! -e "$LFDPI_BOOT_STATE_DIR/fakehttp.wan" ] ||
	fail "locked state clear retained the runtime mapping"

echo "WAN resolver tests: ok"
