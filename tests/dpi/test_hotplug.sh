#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
PKG="$ROOT/openwrt-feed/liquid-formula"
HOTPLUG="$ROOT/openwrt-feed/liquid-formula/files/etc/hotplug.d/iface/99-liquid-formula-dpi"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

INIT_DIR="$TMP/init.d"
INIT_LOG="$TMP/init.log"
LOGGER_LOG="$TMP/logger.log"
NETWORK_HELPERS="$TMP/network.sh"
NETWORK_LOG="$TMP/network.log"
LFDPI_SYS_CLASS_NET="$TMP/sys/class/net"
LFDPI_BOOT_STATE_DIR="$TMP/boot-state"
LFDPI_WAN_RESOLVER="$PKG/files/usr/share/liquid-formula-dpi/wan-resolver.sh"
mkdir -p "$INIT_DIR" "$LFDPI_SYS_CLASS_NET/wan0" "$LFDPI_SYS_CLASS_NET/other0"
: >"$INIT_LOG"
: >"$LOGGER_LOG"
: >"$NETWORK_LOG"

for service in fakehttp fakesip; do
	printf '%s\n' \
		'#!/bin/sh' \
		'printf "%s %s %s%s\n" "${0##*/}" "${1:-}" "${2:-}" "${3:+ $3}" >>"$LFDPI_HOTPLUG_INIT_LOG"' \
		'[ "${LFDPI_HOTPLUG_INIT_FAIL:-0}" != 1 ]' \
		>"$INIT_DIR/$service"
	chmod 0755 "$INIT_DIR/$service"
done

printf '%s\n' \
	'network_flush_cache() { printf "flush\n" >>"$LFDPI_NETWORK_LOG"; }' \
	'network_find_wan() {' \
	'  printf "find4\n" >>"$LFDPI_NETWORK_LOG"' \
	'  [ -n "${TF_NETWORK_V4:-}" ] || return 1' \
	'  eval "$1=\$TF_NETWORK_V4"' \
	'}' \
	'network_find_wan6() {' \
	'  printf "find6\n" >>"$LFDPI_NETWORK_LOG"' \
	'  [ -n "${TF_NETWORK_V6:-}" ] || return 1' \
	'  eval "$1=\$TF_NETWORK_V6"' \
	'}' \
	'network_get_device() {' \
	'  destination="${1:-}"; network="${2:-}"' \
	'  printf "device %s\n" "$network" >>"$LFDPI_NETWORK_LOG"' \
	'  case "$network" in' \
	'    "${TF_NETWORK_V4:-__missing4}") device="${TF_DEVICE_V4:-}" ;;' \
	'    "${TF_NETWORK_V6:-__missing6}") device="${TF_DEVICE_V6:-}" ;;' \
	'    "${TF_NETWORK_INTERFACE:-__missing}") device="${TF_NETWORK_DEVICE:-}" ;;' \
	'    *) return 1 ;;' \
	'  esac' \
	'  [ -n "$device" ] || return 1' \
	'  eval "$destination=\$device"' \
	'}' >"$NETWORK_HELPERS"

LFDPI_COMMON="$ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/service-common.sh"
LFDPI_NETWORK_HELPERS="$NETWORK_HELPERS"
LFDPI_INIT_DIR="$INIT_DIR"
LFDPI_HOTPLUG_INIT_LOG="$INIT_LOG"
LFDPI_NETWORK_LOG="$NETWORK_LOG"
TF_NETWORK_INTERFACE=wan
TF_NETWORK_DEVICE=wan0
TF_NETWORK_V4=''
TF_DEVICE_V4=''
TF_NETWORK_V6=''
TF_DEVICE_V6=''
export LFDPI_COMMON LFDPI_NETWORK_HELPERS LFDPI_INIT_DIR LFDPI_HOTPLUG_INIT_LOG
export LFDPI_NETWORK_LOG LFDPI_WAN_RESOLVER
export LFDPI_SYS_CLASS_NET LFDPI_BOOT_STATE_DIR TF_NETWORK_INTERFACE TF_NETWORK_DEVICE
export TF_NETWORK_V4 TF_DEVICE_V4 TF_NETWORK_V6 TF_DEVICE_V6
LFDPI_HOTPLUG_INIT_FAIL=0
export LFDPI_HOTPLUG_INIT_FAIL

HTTP_ENABLED=1
SIP_ENABLED=1
HTTP_MODE=selected
SIP_MODE=selected
HTTP_FAMILY=dual
SIP_FAMILY=dual
HTTP_INTERFACES=wan0
SIP_INTERFACES=wan0

uci() {
	[ "${1:-}" = -q ] && shift
	[ "${1:-}" = get ] && shift
	case "${1:-}" in
		fakehttp.main.enabled) printf '%s\n' "$HTTP_ENABLED" ;;
		fakesip.main.enabled) printf '%s\n' "$SIP_ENABLED" ;;
		fakehttp.main.interface_mode) printf '%s\n' "$HTTP_MODE" ;;
		fakesip.main.interface_mode) printf '%s\n' "$SIP_MODE" ;;
		fakehttp.main.family) printf '%s\n' "$HTTP_FAMILY" ;;
		fakesip.main.family) printf '%s\n' "$SIP_FAMILY" ;;
		fakehttp.main.interface) printf '%s\n' "$HTTP_INTERFACES" ;;
		fakesip.main.interface) printf '%s\n' "$SIP_INTERFACES" ;;
		*) return 1 ;;
	esac
}

logger() { printf '%s\n' "$*" >>"$LOGGER_LOG"; }

seed_auto_runtime_state() {
	local service
	(
		# Model the state publication performed by start_service only after it
		# has acquired the service lifecycle lock and built a valid definition.
		# shellcheck source=/dev/null
		. "$LFDPI_COMMON"
		# shellcheck source=/dev/null
		. "$LFDPI_NETWORK_HELPERS"
		# shellcheck source=/dev/null
		. "$LFDPI_WAN_RESOLVER"
		tf_wan_resolve dual
		for service in fakehttp fakesip; do
			tf_lifecycle_lock_acquire "$service"
			tf_wan_state_set "$service"
			tf_lifecycle_lock_release "$service"
		done
	)
}

clear_auto_runtime_state() {
	local service
	(
		# shellcheck source=/dev/null
		. "$LFDPI_COMMON"
		# shellcheck source=/dev/null
		. "$LFDPI_WAN_RESOLVER"
		for service in fakehttp fakesip; do
			tf_lifecycle_lock_acquire "$service"
			tf_wan_state_clear "$service"
			tf_lifecycle_lock_release "$service"
		done
	)
}

run_hotplug() {
	# Hotplug files are sourced by netifd; source inside a function so their
	# defensive return paths can be exercised without terminating this test.
	# shellcheck source=/dev/null
	. "$HOTPLUG"
}

reset_logs() {
	: >"$INIT_LOG"
	: >"$LOGGER_LOG"
	: >"$NETWORK_LOG"
}

ACTION=ifdown
DEVICE=wan0
INTERFACE=wan
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_down wan0
fakesip link_down wan0' ]

reset_logs
ACTION=ifup
DEVICE=wan0
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up wan0
fakesip link_up wan0' ]

# Real netifd down events omit DEVICE. The preceding ifup must have cached the
# validated L3 mapping so a later down with no live ubus l3_device still stops
# exactly pppoe-wan/wan0 instead of guessing the physical parent.
reset_logs
ACTION=ifdown
DEVICE=''
TF_NETWORK_DEVICE=''
export TF_NETWORK_DEVICE
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_down wan0
fakesip link_down wan0' ]

reset_logs
ACTION=ifup
DEVICE=other0
run_hotplug
[ ! -s "$INIT_LOG" ]

# DEVICE may be absent when netifd still exposes a unique L3 device through
# INTERFACE. Resolve it rather than guessing or dropping a valid event.
reset_logs
DEVICE=''
TF_NETWORK_DEVICE=wan0
export TF_NETWORK_DEVICE
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up wan0
fakesip link_up wan0' ]

reset_logs
DEVICE=''
TF_NETWORK_DEVICE=''
INTERFACE=uncached
export TF_NETWORK_DEVICE
run_hotplug
[ ! -s "$INIT_LOG" ]
[ -s "$LOGGER_LOG" ]

reset_logs
HTTP_ENABLED=0
SIP_ENABLED=0
DEVICE=wan0
INTERFACE=wan
run_hotplug
[ ! -s "$INIT_LOG" ]

reset_logs
HTTP_ENABLED=1
SIP_ENABLED=1
HTTP_MODE=all
DEVICE=other0
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up other0' ]

reset_logs
HTTP_MODE=selected
DEVICE=ghost0
run_hotplug
[ ! -s "$INIT_LOG" ]
[ -s "$LOGGER_LOG" ]

# Auto mode must classify events by the official logical defaults and bind the
# actual L3 device, never by an arbitrary DEVICE supplied by the event.
reset_logs
HTTP_MODE=auto
SIP_MODE=auto
HTTP_INTERFACES=old-manual0
SIP_INTERFACES=old-manual0
TF_NETWORK_V4=wan
TF_DEVICE_V4=pppoe-wan
TF_NETWORK_V6=wan6
TF_DEVICE_V6=pppoe-wan
TF_NETWORK_INTERFACE=''
TF_NETWORK_DEVICE=''
mkdir -p "$LFDPI_SYS_CLASS_NET/pppoe-wan" "$LFDPI_SYS_CLASS_NET/br-lan"
export TF_NETWORK_V4 TF_DEVICE_V4 TF_NETWORK_V6 TF_DEVICE_V6
export TF_NETWORK_INTERFACE TF_NETWORK_DEVICE
ACTION=ifup
INTERFACE=wan
DEVICE=br-lan

# A hotplug classifier must never publish a "last successful" runtime mapping
# before the init service has accepted and installed it under its lifecycle
# lock. Otherwise a failed or stale ifup can overwrite the running mapping.
clear_auto_runtime_state
LFDPI_HOTPLUG_INIT_FAIL=1
export LFDPI_HOTPLUG_INIT_FAIL
run_hotplug
for service in fakehttp fakesip; do
	if (
		. "$LFDPI_COMMON"
		. "$LFDPI_WAN_RESOLVER"
		tf_wan_state_get "$service"
	); then
		echo "auto hotplug published $service state before init success" >&2
		exit 1
	fi
done
LFDPI_HOTPLUG_INIT_FAIL=0
export LFDPI_HOTPLUG_INIT_FAIL
reset_logs
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up pppoe-wan wan
fakesip link_up pppoe-wan wan' ] || {
	echo "auto ifup did not use the official PPPoE L3 device" >&2
	cat "$INIT_LOG" >&2
	exit 1
}
if grep -Fq 'br-lan' "$INIT_LOG"; then
	echo "auto ifup trusted an arbitrary hotplug DEVICE" >&2
	exit 1
fi
seed_auto_runtime_state

# LAN and VPN events remain irrelevant even when DEVICE happens to equal the
# current WAN L3 device.
for irrelevant in lan vpn0; do
	reset_logs
	ACTION=ifup
	INTERFACE="$irrelevant"
	DEVICE=pppoe-wan
	run_hotplug
	[ ! -s "$INIT_LOG" ] || {
		echo "auto mode reacted to irrelevant $irrelevant event" >&2
		cat "$INIT_LOG" >&2
		exit 1
	}
done

# DEVICE without a logical netifd INTERFACE is not enough to classify an auto
# event. This prevents a LAN/VPN device from becoming an accidental fallback.
reset_logs
ACTION=ifup
INTERFACE=''
DEVICE=pppoe-wan
run_hotplug
[ ! -s "$INIT_LOG" ] || {
	echo "auto mode accepted DEVICE without INTERFACE" >&2
	cat "$INIT_LOG" >&2
	exit 1
}
[ -s "$LOGGER_LOG" ]

# Reconnect must resolve again rather than retain the previous PPPoE device.
reset_logs
TF_DEVICE_V4=pppoe-new
TF_DEVICE_V6=pppoe-new
mkdir -p "$LFDPI_SYS_CLASS_NET/pppoe-new"
export TF_DEVICE_V4 TF_DEVICE_V6
ACTION=ifup
INTERFACE=wan
DEVICE=pppoe-wan
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up pppoe-new wan
fakesip link_up pppoe-new wan' ] || {
	echo "auto reconnect did not re-resolve the current PPPoE L3 device" >&2
	cat "$INIT_LOG" >&2
	exit 1
}
seed_auto_runtime_state

# ifdown happens after netifd may have removed the live default. It must first
# recognize the event through the last successful mapping, then trigger a
# re-resolution. A new IPv6-only default must not hide the old IPv4 event.
reset_logs
TF_NETWORK_V4=''
TF_DEVICE_V4=''
TF_NETWORK_V6=wan6
TF_DEVICE_V6=eth6
mkdir -p "$LFDPI_SYS_CLASS_NET/eth6"
export TF_NETWORK_V4 TF_DEVICE_V4 TF_NETWORK_V6 TF_DEVICE_V6
ACTION=ifdown
INTERFACE=wan
DEVICE=''
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_down pppoe-new wan
fakesip link_down pppoe-new wan' ] || {
	echo "auto ifdown did not classify the event using the last runtime mapping" >&2
	cat "$INIT_LOG" >&2
	exit 1
}
grep -Fx 'find6' "$NETWORK_LOG" >/dev/null || {
	echo "auto ifdown did not re-resolve the surviving family" >&2
	cat "$NETWORK_LOG" >&2
	exit 1
}

# A same-device dual-stack event invokes each service once, while distinct
# family devices select only the logical family that generated the event.
reset_logs
TF_NETWORK_V4=wan
TF_DEVICE_V4=eth4
TF_NETWORK_V6=wan6
TF_DEVICE_V6=eth6
mkdir -p "$LFDPI_SYS_CLASS_NET/eth4"
export TF_NETWORK_V4 TF_DEVICE_V4 TF_NETWORK_V6 TF_DEVICE_V6
ACTION=ifup
INTERFACE=wan6
DEVICE=eth4
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up eth6 wan6
fakesip link_up eth6 wan6' ] || {
	echo "auto event did not select the official device for its logical family" >&2
	cat "$INIT_LOG" >&2
	exit 1
}

# Preserve the explicit old modes: selected still matches its saved device,
# FakeHTTP all accepts any valid event device, and FakeSIP still rejects all.
reset_logs
HTTP_MODE=selected
SIP_MODE=selected
HTTP_INTERFACES=wan0
SIP_INTERFACES=wan0
ACTION=ifup
INTERFACE=manual-net
DEVICE=wan0
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up wan0
fakesip link_up wan0' ]

reset_logs
HTTP_MODE=all
SIP_MODE=all
ACTION=ifup
INTERFACE=manual-net
DEVICE=other0
run_hotplug
[ "$(cat "$INIT_LOG")" = 'fakehttp link_up other0' ]

reset_logs
DEVICE='wan0;reboot'
run_hotplug
[ ! -s "$INIT_LOG" ]
[ -s "$LOGGER_LOG" ]

echo "hotplug tests: ok"
