#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
COMMON="$ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/service-common.sh"
RUNNER="$ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula-dpi/boot-delay-runner.sh"
SCHEDULER="$ROOT/openwrt-feed/liquid-formula/files/etc/init.d/liquid-formula-boot-delay"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

LFDPI_BOOT_STATE_DIR="$TMP/state"
LFDPI_UPTIME_FILE="$TMP/uptime"
export LFDPI_BOOT_STATE_DIR LFDPI_UPTIME_FILE

# shellcheck source=/dev/null
. "$COMMON"

expect_fail() {
	if "$@"; then
		echo "expected failure: $*" >&2
		exit 1
	fi
}

[ "$(tf_boot_delay_value 0 40)" = 0 ]
[ "$(tf_boot_delay_value 600 40)" = 600 ]
[ "$(tf_boot_delay_value 601 40)" = 40 ]
[ "$(tf_boot_delay_value 00 40)" = 40 ]

printf '11.75 99.00\n' >"$LFDPI_UPTIME_FILE"
[ "$(tf_boot_uptime)" = 11 ]
[ "$(tf_boot_remaining 40)" = 29 ]
printf '40.00 99.00\n' >"$LFDPI_UPTIME_FILE"
[ "$(tf_boot_remaining 40)" = 0 ]

tf_boot_state_set fakesip wait alpha-1 40
[ "$(tf_boot_state_get fakesip)" = 'wait alpha-1 40' ]
[ "$(stat -c '%a' "$LFDPI_BOOT_STATE_DIR")" = 700 ]
[ "$(stat -c '%a' "$LFDPI_BOOT_STATE_DIR/fakesip.state")" = 600 ]
expect_fail tf_boot_state_set '../fakesip' wait alpha 40
expect_fail tf_boot_state_set fakesip invalid alpha 40
expect_fail tf_boot_state_set fakesip wait 'bad token' 40
expect_fail tf_boot_state_set fakesip wait alpha 601
printf 'wait alpha 40\ndone alpha 40\n' >"$LFDPI_BOOT_STATE_DIR/fakesip.state"
expect_fail tf_boot_state_get fakesip

printf 'do not follow\n' >"$TMP/outside"
rm -f "$LFDPI_BOOT_STATE_DIR/fakesip.state"
ln -s "$TMP/outside" "$LFDPI_BOOT_STATE_DIR/fakesip.state"
expect_fail tf_boot_state_get fakesip
tf_boot_state_set fakesip wait safe 40
grep -qx 'do not follow' "$TMP/outside"
[ ! -L "$LFDPI_BOOT_STATE_DIR/fakesip.state" ]
tf_boot_state_clear fakesip
expect_fail tf_boot_should_defer fakesip 40
tf_boot_state_set fakesip wait safe 40
printf '11.00 99.00\n' >"$LFDPI_UPTIME_FILE"
tf_boot_should_defer fakesip 40
printf '40.00 99.00\n' >"$LFDPI_UPTIME_FILE"
expect_fail tf_boot_should_defer fakesip 40
tf_boot_state_set fakesip cancel disabled 0
tf_boot_should_defer fakesip 40
tf_boot_state_set fakesip manual manual 0
expect_fail tf_boot_should_defer fakesip 40

expect_fail tf_link_state_set fakesip up
tf_lifecycle_lock_acquire fakesip
tf_lifecycle_lock_acquire fakesip
expect_fail tf_lifecycle_lock_acquire fakehttp
tf_link_state_set fakesip up
[ "$(tf_link_state_get fakesip)" = 'up 1' ]
tf_lifecycle_lock_release fakesip
# One nested release must leave the outer lock held.
tf_link_state_set fakesip down
[ "$(tf_link_state_get fakesip)" = 'down 2' ]
printf 'down 2\nup 3\n' >"$LFDPI_BOOT_STATE_DIR/fakesip.link"
expect_fail tf_link_state_get fakesip
tf_lifecycle_lock_release fakesip
expect_fail tf_link_state_set fakesip up

rm -f "$LFDPI_BOOT_STATE_DIR/fakesip.lock"
ln -s "$TMP/outside" "$LFDPI_BOOT_STATE_DIR/fakesip.lock"
expect_fail tf_lifecycle_lock_acquire fakesip
rm -f "$LFDPI_BOOT_STATE_DIR/fakesip.lock"

(
	flock() { return 127; }
	expect_fail tf_lifecycle_lock_acquire fakesip
)

NONROOT_STATE="$TMP/nonroot-state"
mkdir "$NONROOT_STATE"
(
	LFDPI_BOOT_STATE_DIR="$NONROOT_STATE"
	stat() { printf '65534\n'; }
	expect_fail tf_boot_prepare_state_dir
)

[ -x "$RUNNER" ] || { echo "missing executable boot-delay runner" >&2; exit 1; }
[ -x "$SCHEDULER" ] || { echo "missing executable boot-delay scheduler" >&2; exit 1; }
if grep -q 'tf_boot_state_set' "$RUNNER"; then
	echo "runner must leave the wait-to-done transition to the target init" >&2
	exit 1
fi
grep -q 'boot_expired' "$RUNNER"

INIT_DIR="$TMP/init.d"
INIT_LOG="$TMP/init.log"
mkdir -p "$INIT_DIR"
: >"$INIT_LOG"

write_fake_init() {
	local service="$1"
	printf '%s\n' \
		'#!/bin/sh' \
		'. "$LFDPI_COMMON"' \
		'service="${0##*/}"' \
		'[ "${1:-}" = boot_expired ] || exit 1' \
		'token="${2:-}"' \
		'deadline="${3:-}"' \
		'tf_lifecycle_lock_acquire "$service"' \
		'if tf_boot_state_is "$service" wait "$token" "$deadline"; then' \
		'  tf_boot_state_set "$service" done "$token" "$deadline"' \
		'  printf "%s boot_expired %s %s\n" "$service" "$token" "$deadline" >>"$LFDPI_INIT_LOG"' \
		'fi' \
		'tf_lifecycle_lock_release "$service"' \
		>"$INIT_DIR/$service"
	chmod 0755 "$INIT_DIR/$service"
}

write_fake_init fakesip
write_fake_init fakehttp
export LFDPI_COMMON="$COMMON" LFDPI_INIT_DIR="$INIT_DIR" LFDPI_INIT_LOG="$INIT_LOG"

run_runner() {
	"$RUNNER" "$@"
}

printf '40.00 99.00\n' >"$LFDPI_UPTIME_FILE"
tf_boot_state_set fakesip wait expected 40
run_runner fakesip wrong 40
[ ! -s "$INIT_LOG" ]

tf_boot_state_set fakesip manual manual 0
run_runner fakesip manual 0
[ ! -s "$INIT_LOG" ]
tf_boot_state_set fakesip cancel disabled 0
run_runner fakesip disabled 0
[ ! -s "$INIT_LOG" ]

tf_boot_state_set fakesip wait expected 40
run_runner fakesip expected 40
run_runner fakesip expected 40
[ "$(wc -l <"$INIT_LOG" | tr -d ' ')" = 1 ]
grep -qx 'fakesip boot_expired expected 40' "$INIT_LOG"
[ "$(tf_boot_state_get fakesip)" = 'done expected 40' ]

# Source the real scheduler with procd/UCI stubs. It must create two parallel
# one-shot instances and never configure respawn.
CONFIG_PACKAGE=''
FAKESIP_ENABLED=1
FAKEHTTP_ENABLED=1
FAKESIP_DELAY=40
FAKEHTTP_DELAY=60
PROCD_LOG="$TMP/procd.log"
: >"$PROCD_LOG"
: >"$INIT_LOG"

config_load() { CONFIG_PACKAGE="$1"; }
config_get_bool() {
	case "$CONFIG_PACKAGE" in
		fakesip) eval "$1=\$FAKESIP_ENABLED" ;;
		fakehttp) eval "$1=\$FAKEHTTP_ENABLED" ;;
		*) return 1 ;;
	esac
}
config_get() {
	case "$CONFIG_PACKAGE" in
		fakesip) eval "$1=\$FAKESIP_DELAY" ;;
		fakehttp) eval "$1=\$FAKEHTTP_DELAY" ;;
		*) eval "$1=\${4:-}" ;;
	esac
}
procd_open_instance() {
	PROCD_INSTANCE="$1"
	printf 'open %s\n' "$1" >>"$PROCD_LOG"
}
procd_set_param() { printf 'set %s %s\n' "$PROCD_INSTANCE" "$*" >>"$PROCD_LOG"; }
procd_close_instance() { printf 'close %s\n' "$PROCD_INSTANCE" >>"$PROCD_LOG"; }

# shellcheck source=/dev/null
export LFDPI_BOOT_RUNNER="$RUNNER"
. "$SCHEDULER"

rm -rf "$LFDPI_BOOT_STATE_DIR"
printf '11.00 99.00\n' >"$LFDPI_UPTIME_FILE"
start_service
grep -q '^open wait-fakesip$' "$PROCD_LOG"
grep -q '^open wait-fakehttp$' "$PROCD_LOG"
grep -Eq 'set wait-fakesip command .*boot-delay-runner\.sh fakesip [A-Za-z0-9_.-]+ 40$' "$PROCD_LOG"
grep -Eq 'set wait-fakehttp command .*boot-delay-runner\.sh fakehttp [A-Za-z0-9_.-]+ 60$' "$PROCD_LOG"
if grep -q 'respawn' "$PROCD_LOG"; then
	echo "boot-delay scheduler configured respawn" >&2
	exit 1
fi
set -- $(tf_boot_state_get fakesip)
[ "$1" = wait ] && [ "$3" = 40 ]
set -- $(tf_boot_state_get fakehttp)
[ "$1" = wait ] && [ "$3" = 60 ]

# Zero means immediate automatic start, with no runner instance.
rm -rf "$LFDPI_BOOT_STATE_DIR"
: >"$PROCD_LOG"
: >"$INIT_LOG"
FAKESIP_DELAY=0
FAKEHTTP_ENABLED=0
start_service
[ ! -s "$PROCD_LOG" ]
grep -Eq '^fakesip boot_expired [A-Za-z0-9_.-]+ 0$' "$INIT_LOG"

# A disabled service cancels only a pending wait; manual/done choices survive a
# scheduler restart in the same boot.
tf_boot_state_set fakehttp wait pending 60
FAKESIP_ENABLED=0
FAKEHTTP_ENABLED=0
start_service
[ "$(tf_boot_state_get fakehttp)" = 'cancel disabled 0' ]
tf_boot_state_set fakesip manual manual 0
start_service
[ "$(tf_boot_state_get fakesip)" = 'manual manual 0' ]

tf_boot_state_set fakehttp wait shutdown-pending 60
tf_boot_state_set fakesip done complete 40
stop_service
[ "$(tf_boot_state_get fakehttp)" = 'cancel scheduler-stop 0' ]
[ "$(tf_boot_state_get fakesip)" = 'done complete 40' ]

echo "boot delay tests: ok"
