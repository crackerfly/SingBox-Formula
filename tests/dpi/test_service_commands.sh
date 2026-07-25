#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
PKG="$ROOT/openwrt-feed/liquid-formula"
COMMON="$ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/service-common.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

LFDPI_BOOT_STATE_DIR="$TMP/boot-state"
LFDPI_UPTIME_FILE="$TMP/uptime"
export LFDPI_BOOT_STATE_DIR LFDPI_UPTIME_FILE
printf '100.00 100.00\n' >"$LFDPI_UPTIME_FILE"

sed -e 's#^PROG=.*#PROG="/bin/true"#' \
	-e "s#^COMMON=.*#COMMON=\"$COMMON\"#" \
	"$ROOT/openwrt-feed/liquid-formula/files/etc/init.d/fakehttp" >"$TMP/fakehttp"
sed -e 's#^PROG=.*#PROG="/bin/true"#' \
	-e "s#^COMMON=.*#COMMON=\"$COMMON\"#" \
	"$ROOT/openwrt-feed/liquid-formula/files/etc/init.d/fakesip" >"$TMP/fakesip"

config_load() { :; }

config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}" marker resolved
	eval "marker=\${CFG_${section}_${option}+set}"
	if [ "$marker" = set ]; then
		eval "resolved=\${CFG_${section}_${option}}"
	else
		resolved="$default"
	fi
	eval "$destination=\$resolved"
}

config_get_bool() {
	local destination="$1" value
	config_get value "$2" "$3" "${4-0}"
	case "$value" in 1|on|true|yes|enabled) value=1 ;; *) value=0 ;; esac
	eval "$destination=\$value"
}

config_list_foreach() {
	local section="$1" option="$2" callback="$3" values value
	eval "values=\${CFG_${section}_${option}-}"
	for value in $values; do "$callback" "$value"; done
}

config_foreach() {
	local callback="$1" type="$2" section
	[ "$type" = payload ] || return 0
	for section in $PAYLOAD_SECTIONS; do "$callback" "$section"; done
}

logger() { :; }

COMMAND_ARGS=''
NETDEVS=''
EVENTS=''
ORDER_LOG="$TMP/order.log"
: >"$ORDER_LOG"
append_line() {
	local variable="$1" value="$2" current
	eval "current=\${$variable}"
	if [ -n "$current" ]; then current="$current
$value"; else current="$value"; fi
	eval "$variable=\$current"
}
procd_open_instance() { :; }
procd_close_instance() { :; }
procd_set_param() {
	local parameter="$1" value
	shift
	[ "$parameter" = command ] || return 0
	for value in "$@"; do append_line COMMAND_ARGS "$value"; done
}
procd_append_param() {
	local parameter="$1" value
	shift
	case "$parameter" in
		command) for value in "$@"; do append_line COMMAND_ARGS "$value"; done ;;
		netdev) for value in "$@"; do append_line NETDEVS "$value"; done ;;
	esac
}
procd_kill() {
	printf 'kill %s\n' "$1" >>"$ORDER_LOG"
	append_line EVENTS "kill $1"
}
procd_lock() { append_line EVENTS "procd-lock"; }
procd_add_reload_trigger() { append_line EVENTS "trigger $1"; }
start() {
	local result=0
	append_line EVENTS 'definition-open'
	start_service || result=$?
	service_triggers
	append_line EVENTS 'definition-close-set'
	return "$result"
}

exercise_lifecycle() {
	local service="$1" delay="$2" token="${1}-token" kill_line cleanup_line link_state_before

	CFG_main_enabled=1
	CFG_main_boot_delay="$delay"
	action=boot
	unset LFDPI_SERVICE_CONTEXT TAOISTFUCHEN_BOOT_TOKEN TAOISTFUCHEN_BOOT_DEADLINE
	tf_boot_state_clear "$service" 2>/dev/null || true
	printf '11.00 100.00\n' >"$LFDPI_UPTIME_FILE"
	COMMAND_ARGS=''
	EVENTS=''
	tf_interface_exists() { return 1; }
	start_service
	[ -z "$COMMAND_ARGS" ]
	[ -z "$EVENTS" ]

	action=start
	COMMAND_ARGS=''
	EVENTS=''
	: >"$ORDER_LOG"
	tf_interface_exists() { tf_valid_interface "$1" && [ "$1" = wan0 ]; }
	start_service
	[ -n "$COMMAND_ARGS" ]
	[ "$(tf_boot_state_get "$service")" = 'manual manual 0' ]
	kill_line="$(grep -n "^kill $service$" "$ORDER_LOG" | head -n1 | cut -d: -f1)"
	cleanup_line="$(grep -n "^cleanup $service$" "$ORDER_LOG" | head -n1 | cut -d: -f1)"
	[ -n "$kill_line" ] && [ -n "$cleanup_line" ] && [ "$kill_line" -lt "$cleanup_line" ]

	# The timer may start only through the target's locked boot_expired command.
	tf_boot_state_set "$service" wait "$token" "$delay"
	COMMAND_ARGS=''
	EVENTS=''
	boot_expired "$token" "$delay"
	[ -n "$COMMAND_ARGS" ]
	[ "$(tf_boot_state_get "$service")" = "done $token $delay" ]

	# ifdown records link-down before the kill, preserves a boot wait, kills the
	# old definition, and cleans only after the kill before submitting an empty
	# definition with its reload trigger.
	tf_boot_state_set "$service" wait "$token" "$delay"
	COMMAND_ARGS=''
	EVENTS=''
	tf_interface_exists() { return 1; }
	link_down wan0
	[ -z "$COMMAND_ARGS" ]
	[ "$(tf_boot_state_get "$service")" = "wait $token $delay" ]
	case "$(tf_link_state_get "$service")" in down\ *) ;; *) return 1 ;; esac
	kill_line="$(printf '%s\n' "$EVENTS" | grep -n "^kill $service$" | head -n1 | cut -d: -f1)"
	cleanup_line="$(printf '%s\n' "$EVENTS" | grep -n "^cleanup $service$" | head -n1 | cut -d: -f1)"
	[ -n "$kill_line" ] && [ -n "$cleanup_line" ] && [ "$kill_line" -lt "$cleanup_line" ]
	printf '%s\n' "$EVENTS" | grep -Fx 'definition-close-set' >/dev/null
	printf '%s\n' "$EVENTS" | grep -Fx "trigger $service" >/dev/null

	# Re-check current UCI under the lifecycle lock. A stale event for a device
	# removed by Save & Apply must not stop the newly configured instance.
	link_state_before="$(tf_link_state_get "$service")"
	CFG_main_interface=wan1
	EVENTS=''
	link_down wan0
	[ "$EVENTS" = 'procd-lock' ]
	[ "$(tf_link_state_get "$service")" = "$link_state_before" ]
	CFG_main_enabled=0
	EVENTS=''
	link_down wan1
	[ "$EVENTS" = 'procd-lock' ]
	CFG_main_enabled=1
	CFG_main_interface='wan0 wan0'

	# ifup before the deadline records readiness but cannot bypass the wait.
	COMMAND_ARGS=''
	EVENTS=''
	tf_interface_exists() { tf_valid_interface "$1" && [ "$1" = wan0 ]; }
	printf '11.00 100.00\n' >"$LFDPI_UPTIME_FILE"
	link_up wan0
	[ -z "$COMMAND_ARGS" ]
	case "$(tf_link_state_get "$service")" in up\ *) ;; *) return 1 ;; esac
	[ "$(tf_boot_state_get "$service")" = "wait $token $delay" ]

	# If the runner failed, a later ifup at/after the deadline self-heals.
	COMMAND_ARGS=''
	EVENTS=''
	printf '%s.00 100.00\n' "$delay" >"$LFDPI_UPTIME_FILE"
	link_up wan0
	[ -n "$COMMAND_ARGS" ]
	case "$(tf_boot_state_get "$service")" in done\ *) ;; *) return 1 ;; esac

	# Explicit stop invalidates the token before rc.common kills the instance.
	tf_boot_state_set "$service" wait "$token" "$delay"
	action=stop
	COMMAND_ARGS=''
	EVENTS=''
	stop_service
	procd_kill "$service"
	service_stopped
	[ "$(tf_boot_state_get "$service")" = 'cancel explicit-stop 0' ]
	EVENTS=''
	boot_expired "$token" "$delay"
	[ -z "$COMMAND_ARGS" ]
	[ "$EVENTS" = 'procd-lock' ]

	# The opposite lock ordering is also safe: if expiry wins first, stop wins
	# afterwards and a repeated stale expiry cannot resurrect the service.
	tf_boot_state_set "$service" wait "$token" "$delay"
	COMMAND_ARGS=''
	boot_expired "$token" "$delay"
	[ -n "$COMMAND_ARGS" ]
	action=stop
	stop_service
	procd_kill "$service"
	service_stopped
	COMMAND_ARGS=''
	EVENTS=''
	boot_expired "$token" "$delay"
	[ -z "$COMMAND_ARGS" ]
	[ "$EVENTS" = 'procd-lock' ]
	[ "$(tf_boot_state_get "$service")" = 'cancel explicit-stop 0' ]

	# A stale timer after a successful manual start must not submit an empty
	# rc_procd definition that would tear the manual instance down.
	tf_boot_state_set "$service" manual manual 0
	COMMAND_ARGS='manual-instance-present'
	EVENTS=''
	boot_expired "$token" "$delay"
	[ "$COMMAND_ARGS" = 'manual-instance-present' ]
	[ "$EVENTS" = 'procd-lock' ]

	# Disable + Apply cancels the wait and remains immediate. Restart cancels
	# first, then its start half is a normal immediate manual start.
	tf_boot_state_set "$service" wait "$token" "$delay"
	CFG_main_enabled=0
	action=reload
	COMMAND_ARGS=''
	EVENTS=''
	reload_service
	[ -z "$COMMAND_ARGS" ]
	[ "$(tf_boot_state_get "$service")" = 'cancel disabled 0' ]
	kill_line="$(printf '%s\n' "$EVENTS" | grep -n "^kill $service$" | head -n1 | cut -d: -f1)"
	cleanup_line="$(printf '%s\n' "$EVENTS" | grep -n "^cleanup $service$" | head -n1 | cut -d: -f1)"
	[ -n "$kill_line" ] && [ -n "$cleanup_line" ] && [ "$kill_line" -lt "$cleanup_line" ]

	CFG_main_enabled=1
	tf_boot_state_set "$service" wait "$token" "$delay"
	action=restart
	stop_service
	procd_kill "$service"
	service_stopped
	COMMAND_ARGS=''
	start_service
	[ -n "$COMMAND_ARGS" ]
	[ "$(tf_boot_state_get "$service")" = 'manual manual 0' ]

	action=start
	printf '100.00 100.00\n' >"$LFDPI_UPTIME_FILE"
}

SIBLING_HTTP_ENABLED=0
SIBLING_HTTP_QUEUE=8970
SIBLING_SIP_ENABLED=0
SIBLING_SIP_QUEUE=8971

uci() {
	[ "${1-}" = -q ] && shift
	[ "${1-}" = get ] && shift
	case "${1-}" in
		fakehttp.main.enabled) printf '%s\n' "$SIBLING_HTTP_ENABLED" ;;
		fakehttp.main.queue_num) printf '%s\n' "$SIBLING_HTTP_QUEUE" ;;
		fakesip.main.enabled) printf '%s\n' "$SIBLING_SIP_ENABLED" ;;
		fakesip.main.queue_num) printf '%s\n' "$SIBLING_SIP_QUEUE" ;;
		*) return 1 ;;
	esac
}

# shellcheck source=/dev/null
. "$TMP/fakehttp"
tf_interface_exists() { tf_valid_interface "$1" && [ "$1" = wan0 ]; }
tf_cleanup_nft_table() {
	printf 'cleanup %s\n' "$1" >>"$ORDER_LOG"
	append_line EVENTS "cleanup $1"
}

CFG_main_enabled=1
CFG_main_interface_mode=selected
CFG_main_interface='wan0 wan0'
CFG_main_direction=outbound
CFG_main_family=dual
CFG_main_repeat=2
CFG_main_ttl=3
CFG_main_hop_estimation=1
CFG_main_dynamic_ttl_pct=0
CFG_main_log_connections=0
CFG_main_queue_num=8970
CFG_main_fwmark=0x8000
CFG_main_fwmask=0x8000
PAYLOAD_SECTIONS='p1 p2 p3'
CFG_p1_enabled=1
CFG_p1_type=http
CFG_p1_host=a.example
CFG_p2_enabled=1
CFG_p2_type=https
CFG_p2_host=b.example
CFG_p3_enabled=1
CFG_p3_type=http
CFG_p3_host=c.example

start_service

EXPECTED_HTTP='/bin/true
-i
wan0
-1
-4
-6
-h
c.example
-e
b.example
-h
a.example
-r
2
-t
3
-n
8970
-m
0x8000
-x
0x8000
-s'
[ "$COMMAND_ARGS" = "$EXPECTED_HTTP" ] || {
	echo "unexpected FakeHTTP command arguments:" >&2
	printf '%s\n' "$COMMAND_ARGS" >&2
	exit 1
}
[ "$NETDEVS" = wan0 ]

COMMAND_ARGS=''
SIBLING_SIP_ENABLED=1
SIBLING_SIP_QUEUE=8970
if start_service; then
	echo "FakeHTTP accepted a conflicting FakeSIP queue" >&2
	exit 1
fi
[ -z "$COMMAND_ARGS" ]
SIBLING_SIP_ENABLED=0
SIBLING_SIP_QUEUE=8971

exercise_lifecycle fakehttp 60

# shellcheck source=/dev/null
. "$TMP/fakesip"
tf_interface_exists() { tf_valid_interface "$1" && [ "$1" = wan0 ]; }
tf_cleanup_nft_table() {
	printf 'cleanup %s\n' "$1" >>"$ORDER_LOG"
	append_line EVENTS "cleanup $1"
}

COMMAND_ARGS=''
NETDEVS=''
CFG_main_enabled=1
CFG_main_interface_mode=selected
CFG_main_interface='wan0 wan0'
CFG_main_direction=both
CFG_main_family=ipv4
CFG_main_port_mode=exclude
CFG_main_ports=53
CFG_main_payload_mode=auto
CFG_main_sip_uri=''
CFG_main_repeat=2
CFG_main_ttl=3
CFG_main_hop_estimation=1
CFG_main_dynamic_ttl_pct=0
CFG_main_log_connections=0
CFG_main_queue_num=8971
CFG_main_fwmark=0x10000
CFG_main_fwmask=0x10000

start_service

EXPECTED_SIP='/bin/true
-i
wan0
-0
-1
-4
-P
53
-r
2
-t
3
-n
8971
-m
0x10000
-x
0x10000
-s'
[ "$COMMAND_ARGS" = "$EXPECTED_SIP" ] || {
	echo "unexpected FakeSIP command arguments:" >&2
	printf '%s\n' "$COMMAND_ARGS" >&2
	exit 1
}
[ "$NETDEVS" = wan0 ]

COMMAND_ARGS=''
NETDEVS=''
unset CFG_main_direction CFG_main_family
start_service
printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-0' >/dev/null
if printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-1' >/dev/null; then
	echo "FakeSIP fallback unexpectedly enabled inbound traffic" >&2
	exit 1
fi
FAMILY_ARGS="$(printf '%s\n' "$COMMAND_ARGS" | grep -E '^-4$|^-6$')"
[ "$FAMILY_ARGS" = '-4
-6' ] || {
	echo "FakeSIP fallback did not enable dual stack" >&2
	exit 1
}
CFG_main_direction=both
CFG_main_family=ipv4

exercise_lifecycle fakesip 40

COMMAND_ARGS=''
NETDEVS=''
CFG_main_direction=outbound
start_service
printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-0' >/dev/null
if printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-1' >/dev/null; then
	echo "FakeSIP outbound mode was not compensated for the upstream flag inversion" >&2
	exit 1
fi
CFG_main_direction=both

COMMAND_ARGS=''
NETDEVS=''
CFG_main_direction=inbound
start_service
printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-1' >/dev/null
if printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-0' >/dev/null; then
	echo "FakeSIP inbound mode was not compensated for the upstream flag inversion" >&2
	exit 1
fi
CFG_main_direction=both

COMMAND_ARGS=''
NETDEVS=''
CFG_main_family=ipv6
start_service
printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-6' >/dev/null
if printf '%s\n' "$COMMAND_ARGS" | grep -Fx -- '-4' >/dev/null; then
	echo "FakeSIP IPv6 mode unexpectedly enabled IPv4" >&2
	exit 1
fi

COMMAND_ARGS=''
NETDEVS=''
CFG_main_family=dual
start_service
FAMILY_ARGS="$(printf '%s\n' "$COMMAND_ARGS" | grep -E '^-4$|^-6$')"
[ "$FAMILY_ARGS" = '-4
-6' ] || {
	echo "FakeSIP dual-stack flags are missing or out of order" >&2
	exit 1
}

COMMAND_ARGS=''
NETDEVS=''
CFG_main_family=invalid
if start_service; then
	echo "FakeSIP accepted an invalid address family" >&2
	exit 1
fi
[ -z "$COMMAND_ARGS" ]
CFG_main_family=ipv4

COMMAND_ARGS=''
SIBLING_HTTP_ENABLED=1
SIBLING_HTTP_QUEUE=8971
if start_service; then
	echo "FakeSIP accepted a conflicting FakeHTTP queue" >&2
	exit 1
fi
[ -z "$COMMAND_ARGS" ]

echo "service command tests: ok"
