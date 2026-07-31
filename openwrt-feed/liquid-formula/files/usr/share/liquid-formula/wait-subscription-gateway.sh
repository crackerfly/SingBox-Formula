#!/bin/sh

set -u

. /lib/functions.sh

STATE_DIR=/var/run/liquid-formula
BOOT_MARKER=$STATE_DIR/boot-delay.done
UPTIME_FILE=/proc/uptime
CANCELLED=0
HEALTH_FILE=
CONTENT_TYPE_FILE=
CHILD_PID=

cleanup() {
	[ -z "$HEALTH_FILE" ] || rm -f "$HEALTH_FILE"
	[ -z "$CONTENT_TYPE_FILE" ] || rm -f "$CONTENT_TYPE_FILE"
}

cancel() {
	CANCELLED=1
	[ -z "$CHILD_PID" ] || kill -TERM "$CHILD_PID" 2>/dev/null || true
	cleanup
	exit 143
}

trap cancel HUP INT TERM
trap cleanup EXIT

valid_uint() {
	value=$1
	minimum=$2
	maximum=$3
	max_digits=${#maximum}

	case "$value" in
		''|*[!0-9]*) return 1 ;;
		0) ;;
		0*) return 1 ;;
	esac

	[ "${#value}" -le "$max_digits" ] || return 1
	[ "$value" -ge "$minimum" ] 2>/dev/null || return 1
	[ "$value" -le "$maximum" ] 2>/dev/null || return 1
}

monotonic_seconds() {
	awk '
		NR == 1 && $1 ~ /^[0-9]+([.][0-9]+)?$/ {
			printf "%d\n", $1
			found = 1
			exit
		}
		END { if (!found) exit 1 }
	' "$UPTIME_FILE"
}

managed_sleep() {
	sleep "$1" &
	CHILD_PID=$!
	wait "$CHILD_PID"
	status=$?
	CHILD_PID=
	[ "$CANCELLED" -eq 0 ] || return 143
	return "$status"
}

if [ "$#" -lt 7 ]; then
	exit 2
fi

mode=$1
delay=$2
gateway_port=$3
config_digest=$4
wait_budget=$5
shift 5

case "$mode" in
	boot|manual|reconcile) ;;
	*) exit 2 ;;
esac

valid_uint "$delay" 0 600 || exit 2
valid_uint "$gateway_port" 1 65535 || exit 2
valid_uint "$wait_budget" 1 2147483647 || exit 2

[ "${#config_digest}" -eq 64 ] || exit 2
case "$config_digest" in
	*[!0-9a-f]*) exit 2 ;;
esac

[ "$1" = -- ] || exit 2
shift
[ "$#" -ge 1 ] && [ -n "$1" ] || exit 2

umask 077

if [ "$mode" = boot ] && [ ! -f "$BOOT_MARKER" ]; then
	mkdir -p "$STATE_DIR" || exit 1
	chmod 0700 "$STATE_DIR" 2>/dev/null || exit 1

	if [ "$delay" -gt 0 ]; then
		managed_sleep "$delay" || exit $?
	fi
	[ "$CANCELLED" -eq 0 ] || exit 143

	: > "$BOOT_MARKER" || exit 1
	chmod 0600 "$BOOT_MARKER" 2>/dev/null || exit 1

	config_load liquid_formula || exit 1
	enabled=0
	config_get_bool enabled main enabled 0
	[ "$enabled" -eq 1 ] 2>/dev/null || exit 0
fi

mkdir -p "$STATE_DIR" || exit 1
chmod 0700 "$STATE_DIR" 2>/dev/null || exit 1
HEALTH_FILE=$(mktemp "$STATE_DIR/health.XXXXXX") || exit 1
chmod 0600 "$HEALTH_FILE" 2>/dev/null || exit 1
CONTENT_TYPE_FILE=$(mktemp "$STATE_DIR/content-type.XXXXXX") || exit 1
chmod 0600 "$CONTENT_TYPE_FILE" 2>/dev/null || exit 1

expected_body="{\"service\":\"liquid-formula-subscription-gateway\",\"status\":\"ok\",\"config_digest\":\"$config_digest\"}"
expected_size=${#expected_body}
start=$(monotonic_seconds) || exit 1

while [ "$CANCELLED" -eq 0 ]; do
	now=$(monotonic_seconds) || exit 1
	elapsed=$((now - start))
	[ "$elapsed" -ge 0 ] || exit 1
	remaining=$((wait_budget - elapsed))
	[ "$remaining" -gt 0 ] || exit 1

	probe_timeout=1
	[ "$remaining" -ge "$probe_timeout" ] || probe_timeout=$remaining
	: > "$HEALTH_FILE" || exit 1

	: > "$CONTENT_TYPE_FILE" || exit 1
	curl --silent --show-error --fail \
		--request GET \
		--connect-timeout "$probe_timeout" \
		--max-time "$probe_timeout" \
		--output "$HEALTH_FILE" \
		--write-out '%{content_type}' \
		"http://127.0.0.1:$gateway_port/health" > "$CONTENT_TYPE_FILE" &
	CHILD_PID=$!
	wait "$CHILD_PID"
	curl_status=$?
	CHILD_PID=
	[ "$CANCELLED" -eq 0 ] || exit 143
	content_type=$(cat "$CONTENT_TYPE_FILE")

	if [ "$curl_status" -eq 0 ] && [ "$content_type" = application/json ]; then
		body_size=$(wc -c < "$HEALTH_FILE" | tr -d '[:space:]')
		if [ "$body_size" = "$expected_size" ]; then
			body=$(cat "$HEALTH_FILE")
			if [ "$body" = "$expected_body" ]; then
				[ "$CANCELLED" -eq 0 ] || exit 143
				cleanup
				HEALTH_FILE=
				CONTENT_TYPE_FILE=
				trap - EXIT
				exec "$@"
			fi
		fi
	fi

	now=$(monotonic_seconds) || exit 1
	elapsed=$((now - start))
	[ "$elapsed" -ge 0 ] || exit 1
	remaining=$((wait_budget - elapsed))
	[ "$remaining" -gt 0 ] || exit 1

	sleep_for=1
	[ "$remaining" -ge "$sleep_for" ] || sleep_for=$remaining
	[ "$sleep_for" -gt 0 ] || exit 1
	managed_sleep "$sleep_for" || exit $?
done

exit 143
