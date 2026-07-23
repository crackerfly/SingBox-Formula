#!/bin/sh

delay=${1:-}
case "$delay" in
	''|*[!0-9]*)
		echo "invalid boot delay: $delay" >&2
		exit 2
		;;
esac
[ "$delay" -le 600 ] || {
	echo "invalid boot delay: $delay" >&2
	exit 2
}
shift
[ "$#" -gt 0 ] || {
	echo 'missing converter command' >&2
	exit 2
}

STATE_DIR='/var/run/singbox-formula'
MARKER="$STATE_DIR/boot-delay.done"
sleep_pid=

cancel_delay() {
	if [ -n "$sleep_pid" ]; then
		kill "$sleep_pid" 2>/dev/null || true
		wait "$sleep_pid" 2>/dev/null || true
	fi
	exit 0
}

trap cancel_delay INT TERM
umask 077
mkdir -p "$STATE_DIR" || exit 1

if [ ! -f "$MARKER" ]; then
	sleep "$delay" &
	sleep_pid=$!
	wait "$sleep_pid"
	wait_status=$?
	sleep_pid=
	[ "$wait_status" -eq 0 ] || exit "$wait_status"
	: > "$MARKER" || exit 1
fi

trap - INT TERM

. /lib/functions.sh
config_load singbox_formula
config_get_bool enabled main enabled 0
[ "$enabled" = "1" ] || exit 0

exec "$@"
