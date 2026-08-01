#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

INIT_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/etc/init.d/liquid-formula"
HELPER_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula/run-delayed.sh"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-procd-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_BIN="$TEST_TMP/bin"
MOCK_ROOT="$TEST_TMP/root"
PROCD_LOG="$TEST_TMP/procd.log"
START_LOG="$TEST_TMP/start.log"
LIFECYCLE_LOG="$TEST_TMP/lifecycle.log"
CONF="$MOCK_ROOT/etc/liquid-formula/config.yaml"
PROG="$MOCK_ROOT/usr/bin/sb-sub-c"
GEN="$MOCK_ROOT/usr/share/liquid-formula/generate-config.sh"
DELAY_HELPER="$MOCK_ROOT/usr/share/liquid-formula/run-delayed.sh"
INIT_UNDER_TEST="$TEST_TMP/init-under-test.sh"

mkdir -p "$MOCK_BIN" "$(dirname "$CONF")" "$(dirname "$PROG")" "$(dirname "$GEN")"

cat > "$PROG" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$PROG"

cat > "$GEN" <<EOF
#!/bin/sh
printf '%s\n' "\${MOCK_CONFIG_BODY:-config-a}" > "$CONF"
EOF
chmod 0755 "$GEN"

cat > "$DELAY_HELPER" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$DELAY_HELPER"

cat > "$MOCK_BIN/uci" <<'EOF'
#!/bin/sh
while [ "${1:-}" = "-q" ]; do shift; done
case "${1:-}" in
	get)
		case "${2:-}" in
			system.@system\[0\].timezone) printf '%s\n' "${MOCK_TIMEZONE:-JST-9}" ;;
			*) exit 1 ;;
		esac
		;;
	*) exit 1 ;;
esac
EOF
chmod 0755 "$MOCK_BIN/uci"

sed \
	-e '1d' \
	-e '/^[[:space:]]*\.[[:space:]]*\/lib\/functions\.sh[[:space:]]*$/d' \
	-e "s|^PROG=.*|PROG='$PROG'|" \
	-e "s|^GEN=.*|GEN='$GEN'|" \
	-e "s|^CONF=.*|CONF='$CONF'|" \
	-e "s|^WORKDIR=.*|WORKDIR='$(dirname "$CONF")'|" \
	-e "s|^DELAY_HELPER=.*|DELAY_HELPER='$DELAY_HELPER'|" \
	-e "s|/var/lib/liquid-formula|$MOCK_ROOT/var/lib/liquid-formula|g" \
	-e "s|/var/log/liquid-formula|$MOCK_ROOT/var/log/liquid-formula|g" \
	"$INIT_SOURCE" > "$INIT_UNDER_TEST"

PATH="$MOCK_BIN:$PATH"
SBF_STATE_DIR="$MOCK_ROOT/var/run/liquid-formula"
export PATH MOCK_CONFIG_BODY MOCK_TIMEZONE SBF_STATE_DIR

config_load() {
	:
}

config_get_bool() {
	_variable=$1
	_default=$4
	_value=${MOCK_ENABLED:-$_default}
	eval "$_variable=\$_value"
}

config_get() {
	_variable=$1
	_default=$4
	_value=${MOCK_DELAY:-$_default}
	eval "$_variable=\$_value"
}

procd_open_instance() {
	printf 'open' >> "$PROCD_LOG"
	for _arg in "$@"; do printf '|%s' "$_arg" >> "$PROCD_LOG"; done
	printf '\n' >> "$PROCD_LOG"
}

procd_set_param() {
	printf 'param' >> "$PROCD_LOG"
	for _arg in "$@"; do printf '|%s' "$_arg" >> "$PROCD_LOG"; done
	printf '\n' >> "$PROCD_LOG"
}

procd_close_instance() {
	printf 'close\n' >> "$PROCD_LOG"
}

procd_add_reload_trigger() {
	:
}

json_set_namespace() {
	[ "$#" -lt 2 ] || eval "$2=mock"
	return 0
}

install_procd_mutation_mocks() {
	_procd_close_service() {
		if [ -f "$SBF_STATE_DIR/lifecycle.lock/owner" ]; then
			printf 'close:locked\n' >> "$LIFECYCLE_LOG"
		else
			printf 'close:unlocked\n' >> "$LIFECYCLE_LOG"
		fi
		return "${MOCK_PROCD_CLOSE_RC:-0}"
	}
	_procd_kill() {
		if [ -f "$SBF_STATE_DIR/lifecycle.lock/owner" ]; then
			printf 'kill:locked\n' >> "$LIFECYCLE_LOG"
		else
			printf 'kill:unlocked\n' >> "$LIFECYCLE_LOG"
		fi
		return "${MOCK_PROCD_KILL_RC:-0}"
	}
	procd_close_service() { _procd_close_service "$@"; }
	procd_kill() { _procd_kill "$@"; }
}
install_procd_mutation_mocks

start() {
	printf '%s\n' "$*" > "$START_LOG"
	: > "$PROCD_LOG"
	start_service "$@"
	_start_rc=$?
	procd_close_service
	service_started
	_started_rc=$?
	[ "$_started_rc" -ne 0 ] && return "$_started_rc"
	return "$_start_rc"
}

stop() {
	stop_service "$@"
	_stop_rc=$?
	procd_kill liquid-formula "${1-}"
	service_stopped
	_stopped_rc=$?
	[ "$_stopped_rc" -ne 0 ] && return "$_stopped_rc"
	return "$_stop_rc"
}

. "$INIT_UNDER_TEST"

assert_equals() {
	_expected=$1
	_actual=$2
	_description=$3
	if [ "$_actual" = "$_expected" ]; then
		record_ok "$_description"
	else
		record_failure "$_description (expected '$_expected', got '$_actual')"
	fi
}

assert_literal() {
	_file=$1
	_literal=$2
	_description=$3
	if [ -f "$_file" ] && grep -Fq -- "$_literal" "$_file"; then
		record_ok "$_description"
	else
		record_failure "$_description (missing literal: $_literal)"
	fi
}

assert_no_literal() {
	_file=$1
	_literal=$2
	_description=$3
	if [ -f "$_file" ] && ! grep -Fq -- "$_literal" "$_file"; then
		record_ok "$_description"
	else
		record_failure "$_description (unexpected literal: $_literal)"
	fi
}

run_service() {
	_mode=$1
	: > "$PROCD_LOG"
	if [ -n "$_mode" ]; then
		start "$_mode"
	else
		start
	fi
}

MOCK_ENABLED=1
MOCK_DELAY=37
MOCK_CONFIG_BODY=config-default
export MOCK_ENABLED MOCK_DELAY MOCK_CONFIG_BODY
run_service ''
assert_literal "$PROCD_LOG" 'open|main' 'default reconcile registers the named main instance'
assert_literal "$PROCD_LOG" "param|command|$PROG|run|-c|$CONF|-d|$(dirname "$CONF")" 'default reconcile runs the converter directly'
assert_literal "$PROCD_LOG" 'param|respawn|30|5|5' 'main instance has bounded respawn settings'
assert_literal "$PROCD_LOG" 'param|term_timeout|5' 'main instance has a five-second term timeout'

expected_digest=$(sha256sum "$CONF")
expected_digest=${expected_digest%% *}
assert_literal "$PROCD_LOG" "CONFIG_DIGEST=$expected_digest" 'main instance environment contains the generated config digest'

MOCK_CONFIG_BODY=config-changed
export MOCK_CONFIG_BODY
run_service ''
changed_digest=$(sha256sum "$CONF")
changed_digest=${changed_digest%% *}
if [ "$changed_digest" != "$expected_digest" ] && grep -Fq "CONFIG_DIGEST=$changed_digest" "$PROCD_LOG"; then
	record_ok 'config content changes update the procd digest environment'
else
	record_failure 'config content changes update the procd digest environment'
fi

MOCK_ENABLED=0
export MOCK_ENABLED
printf 'open|main\n' > "$PROCD_LOG"
reload_service
assert_empty "$(cat "$PROCD_LOG")" 'disabled default reconcile publishes no main instance'

chmod 0644 "$PROG"
if reload_service; then
	record_ok 'disabled reconcile removes main even when the converter binary is unavailable'
else
	record_failure 'disabled reconcile removes main even when the converter binary is unavailable'
fi
assert_empty "$(cat "$PROCD_LOG")" 'disabled reconcile with no converter still publishes no instance'
chmod 0755 "$PROG"

MOCK_ENABLED=0
MOCK_CONFIG_BODY=config-manual
export MOCK_ENABLED MOCK_CONFIG_BODY
run_service manual
assert_literal "$PROCD_LOG" 'open|main' 'manual mode may start while disk enabled is false'
assert_literal "$PROCD_LOG" "param|command|$PROG|run|-c|$CONF|-d|$(dirname "$CONF")" 'manual mode bypasses the boot delay helper'
assert_no_literal "$PROCD_LOG" "$DELAY_HELPER" 'manual mode never invokes the delay helper'

MOCK_ENABLED=1
MOCK_DELAY=37
export MOCK_ENABLED MOCK_DELAY
run_service boot
assert_literal "$PROCD_LOG" 'open|main' 'enabled boot mode registers main'
assert_literal "$PROCD_LOG" "param|command|$DELAY_HELPER|37|$PROG|run|-c|$CONF|-d|$(dirname "$CONF")" 'boot mode puts the delay helper under procd supervision'

chmod 0644 "$DELAY_HELPER"
: > "$PROCD_LOG"
if start boot >/dev/null 2>&1; then
	record_failure 'boot mode rejects a missing delay helper'
else
	record_ok 'boot mode rejects a missing delay helper'
fi
assert_empty "$(cat "$PROCD_LOG")" 'boot validates the delay helper before opening a procd instance'
chmod 0755 "$DELAY_HELPER"

MOCK_ENABLED=0
export MOCK_ENABLED
run_service boot
assert_empty "$(cat "$PROCD_LOG")" 'disabled boot mode registers no instance'

MOCK_ENABLED=1
export MOCK_ENABLED
: > "$START_LOG"
boot
assert_equals 'boot' "$(cat "$START_LOG")" 'boot delegates to start_service in boot mode through rc.common start'

assert_not_contains "$INIT_SOURCE" '(^|[^[:alnum:]_])pgrep([^[:alnum:]_]|$)' 'init lifecycle does not depend on pgrep'
assert_contains "$INIT_SOURCE" 'service_started' 'init holds the lifecycle lock through procd service publication'
assert_contains "$INIT_SOURCE" 'service_stopped' 'init holds the lifecycle lock through procd service deletion'
assert_file_exists "$HELPER_SOURCE" 'ships a procd-managed boot delay helper'

# rc.common invokes these callbacks after the procd mutation, so their return
# value becomes the init command's result. Releasing the lifecycle lock must not
# turn an underlying ubus/procd failure into success.
rm -rf "$SBF_STATE_DIR"
MOCK_PROCD_CLOSE_RC=74
export MOCK_PROCD_CLOSE_RC
install_procd_mutation_mocks
start manual >/dev/null 2>&1
close_failure_rc=$?
assert_equals 74 "$close_failure_rc" 'start propagates a failed procd service publication'
assert_file_not_exists "$SBF_STATE_DIR/lifecycle.lock" 'failed procd publication still releases the lifecycle lock'
MOCK_PROCD_CLOSE_RC=0
export MOCK_PROCD_CLOSE_RC

MOCK_PROCD_KILL_RC=74
export MOCK_PROCD_KILL_RC
install_procd_mutation_mocks
stop >/dev/null 2>&1
kill_failure_rc=$?
assert_equals 74 "$kill_failure_rc" 'stop propagates a failed procd service deletion'
assert_file_not_exists "$SBF_STATE_DIR/lifecycle.lock" 'failed procd deletion still releases the lifecycle lock'
MOCK_PROCD_KILL_RC=0
export MOCK_PROCD_KILL_RC

# Direct admin/boot actions, updater calls, and RPC calls share one owner-token lock.
# A live external owner must suppress rc.common's close/kill mutations; a caller with
# that exact live token is the only re-entrant path.
rm -rf "$SBF_STATE_DIR"
mkdir -p "$SBF_STATE_DIR/lifecycle.lock"
IFS= read -r test_stat < /proc/$$/stat
test_start=$(printf '%s\n' "$test_stat" | awk '{ sub(/^.*\) /, ""); split($0,f," "); print f[20] }')
printf '%s %s %s\n' "$$" "$test_start" external-owner > "$SBF_STATE_DIR/lifecycle.lock/owner"
chmod 0600 "$SBF_STATE_DIR/lifecycle.lock/owner"
unset LF_LIFECYCLE_TOKEN
: > "$LIFECYCLE_LOG"
install_procd_mutation_mocks
if start manual >/dev/null 2>&1; then
	record_failure 'direct admin start is refused while another lifecycle owner is live'
else
	record_ok 'direct admin start is refused while another lifecycle owner is live'
fi
assert_empty "$(cat "$LIFECYCLE_LOG")" 'busy admin start cannot publish an empty procd service'

: > "$LIFECYCLE_LOG"
install_procd_mutation_mocks
if stop >/dev/null 2>&1; then
	record_failure 'direct admin stop is refused while another lifecycle owner is live'
else
	record_ok 'direct admin stop is refused while another lifecycle owner is live'
fi
assert_empty "$(cat "$LIFECYCLE_LOG")" 'busy admin stop cannot call procd_kill'

LF_LIFECYCLE_TOKEN=external-owner
export LF_LIFECYCLE_TOKEN
: > "$LIFECYCLE_LOG"
install_procd_mutation_mocks
if start manual >/dev/null 2>&1; then
	record_ok 'the exact live owner token permits a re-entrant init start'
else
	record_failure 'the exact live owner token permits a re-entrant init start'
fi
assert_literal "$LIFECYCLE_LOG" 'close:locked' 're-entrant start keeps the external owner lock through procd close'
assert_file_exists "$SBF_STATE_DIR/lifecycle.lock/owner" 're-entrant init call never releases its caller lock'

# A delegated init child must keep the lifecycle lease live if its updater/RPC
# parent dies after delegation. Model the parent's /proc identity changing from
# live to absent; the delegate itself uses this test process's real identity.
mock_delegator_pid=424242
mock_delegator_start=777
mock_delegator_live=1
lf_process_start() {
	local pid="$1" line
	if [ "$pid" = "$mock_delegator_pid" ]; then
		[ "$mock_delegator_live" = 1 ] || return 1
		printf '%s\n' "$mock_delegator_start"
		return 0
	fi
	[ -r "/proc/$pid/stat" ] || return 1
	IFS= read -r line < "/proc/$pid/stat" || return 1
	printf '%s\n' "$line" | awk '{ sub(/^.*\) /, ""); split($0,f," "); print f[20] }'
}
rm -rf "$SBF_STATE_DIR/lifecycle.lock"
mkdir -p "$SBF_STATE_DIR/lifecycle.lock"
printf '%s %s %s\n' "$mock_delegator_pid" "$mock_delegator_start" delegated-owner > "$SBF_STATE_DIR/lifecycle.lock/owner"
chmod 0600 "$SBF_STATE_DIR/lifecycle.lock/owner"
LF_LIFECYCLE_TOKEN=delegated-owner
export LF_LIFECYCLE_TOKEN
if lf_lifecycle_enter; then
	record_ok 'delegated init publishes a live lifecycle lease'
else
	record_failure 'delegated init publishes a live lifecycle lease'
fi
assert_file_exists "$SBF_STATE_DIR/lifecycle.lock/delegate" 'delegate marker remains live until the init callback'
mock_delegator_live=0
delegated_token=$LF_LIFECYCLE_TOKEN
lf_acquire_lifecycle_lock >/dev/null 2>&1
delegate_competitor_rc=$?
LF_LIFECYCLE_TOKEN=$delegated_token
assert_equals 75 "$delegate_competitor_rc" 'dead parent remains busy while its delegated init child is live'
assert_file_exists "$SBF_STATE_DIR/lifecycle.lock/owner" 'delegate prevents stale recovery from deleting the parent owner'
lf_lifecycle_leave
delegate_rc=$?
assert_equals 0 "$delegate_rc" 'delegated init releases its marker after the callback'
assert_file_not_exists "$SBF_STATE_DIR/lifecycle.lock/delegate" 'completed delegate removes its lifecycle marker'
LF_LIFECYCLE_TOKEN=
if lf_acquire_lifecycle_lock >/dev/null 2>&1; then
	record_ok 'stale parent lock is recoverable after its delegate exits'
	lf_release_lifecycle_lock
else
	record_failure 'stale parent lock is recoverable after its delegate exits'
fi

unset LF_LIFECYCLE_TOKEN
rm -rf "$SBF_STATE_DIR/lifecycle.lock"
: > "$LIFECYCLE_LOG"
install_procd_mutation_mocks
if restart manual >/dev/null 2>&1; then
	record_ok 'direct admin restart completes under one lifecycle owner'
else
	record_failure 'direct admin restart completes under one lifecycle owner'
fi
assert_literal "$LIFECYCLE_LOG" 'kill:locked' 'restart holds the lifecycle lock through procd kill'
assert_literal "$LIFECYCLE_LOG" 'close:locked' 'restart keeps the same lifecycle lock through procd close'
assert_file_not_exists "$SBF_STATE_DIR/lifecycle.lock" 'direct restart releases its lifecycle lock after both phases'

if [ -f "$HELPER_SOURCE" ]; then
	HELPER_UNDER_TEST="$TEST_TMP/run-delayed-under-test.sh"
	HELPER_FUNCTIONS="$TEST_TMP/helper-functions.sh"
	HELPER_STATE="$TEST_TMP/run/liquid-formula"
	HELPER_MARKER="$HELPER_STATE/boot-delay.done"
	HELPER_SLEEP_LOG="$TEST_TMP/helper-sleep.log"
	HELPER_SLEEP_STARTED="$TEST_TMP/helper-sleep.started"
	PAYLOAD_LOG="$TEST_TMP/payload.log"

	cat > "$HELPER_FUNCTIONS" <<'EOF'
config_load() { :; }
config_get_bool() {
	variable=$1
	default=$4
	value=${MOCK_ENABLED_AFTER:-$default}
	eval "$variable=\$value"
}
EOF

	sed \
		-e "s|^[[:space:]]*\.[[:space:]]*/lib/functions\.sh[[:space:]]*$|. '$HELPER_FUNCTIONS'|" \
		-e "s|/var/run/liquid-formula|$HELPER_STATE|g" \
		"$HELPER_SOURCE" > "$HELPER_UNDER_TEST"
	chmod 0755 "$HELPER_UNDER_TEST"

	cat > "$TEST_TMP/payload" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$PAYLOAD_LOG"
EOF
	chmod 0755 "$TEST_TMP/payload"

	write_fast_sleep() {
		cat > "$MOCK_BIN/sleep" <<EOF
#!/bin/sh
printf '%s\n' "\$1" >> "$HELPER_SLEEP_LOG"
exit 0
EOF
		chmod 0755 "$MOCK_BIN/sleep"
	}

	write_fast_sleep
	rm -rf "$HELPER_STATE" "$PAYLOAD_LOG" "$HELPER_SLEEP_LOG"
	MOCK_ENABLED_AFTER=1
	export MOCK_ENABLED_AFTER
	if "$HELPER_UNDER_TEST" invalid "$TEST_TMP/payload" nope >/dev/null 2>&1; then
		record_failure 'delay helper rejects a nonnumeric delay'
	else
		record_ok 'delay helper rejects a nonnumeric delay'
	fi
	if "$HELPER_UNDER_TEST" 601 "$TEST_TMP/payload" too-large >/dev/null 2>&1; then
		record_failure 'delay helper rejects values above 600 seconds'
	else
		record_ok 'delay helper rejects values above 600 seconds'
	fi
	assert_file_not_exists "$PAYLOAD_LOG" 'invalid delays never launch the converter payload'

	rm -rf "$HELPER_STATE" "$PAYLOAD_LOG" "$HELPER_SLEEP_LOG"
	"$HELPER_UNDER_TEST" 3 "$TEST_TMP/payload" first boot
	assert_file_content '3' "$HELPER_SLEEP_LOG" 'first boot waits for the configured delay'
	assert_file_exists "$HELPER_MARKER" 'completed boot delay records a per-boot marker'
	assert_file_content 'first boot' "$PAYLOAD_LOG" 'enabled helper execs the converter payload after waiting'

	: > "$HELPER_SLEEP_LOG"
	: > "$PAYLOAD_LOG"
	"$HELPER_UNDER_TEST" 3 "$TEST_TMP/payload" respawn
	assert_empty "$(cat "$HELPER_SLEEP_LOG")" 'respawn skips a delay already completed this boot'
	assert_file_content 'respawn' "$PAYLOAD_LOG" 'respawn still execs the converter payload'

	rm -rf "$HELPER_STATE" "$PAYLOAD_LOG" "$HELPER_SLEEP_LOG"
	MOCK_ENABLED_AFTER=0
	export MOCK_ENABLED_AFTER
	"$HELPER_UNDER_TEST" 3 "$TEST_TMP/payload" disabled
	assert_file_not_exists "$PAYLOAD_LOG" 'helper rechecks enabled state before launching the converter'
	assert_file_exists "$HELPER_MARKER" 'completed wait is marked even when the service was disabled during delay'

	cat > "$MOCK_BIN/sleep" <<EOF
#!/bin/sh
trap 'exit 0' TERM INT
printf '%s\n' "\$\$" > "$HELPER_SLEEP_STARTED"
while :; do /bin/sleep 1; done
EOF
	chmod 0755 "$MOCK_BIN/sleep"
	rm -rf "$HELPER_STATE" "$PAYLOAD_LOG" "$HELPER_SLEEP_STARTED"
	MOCK_ENABLED_AFTER=1
	export MOCK_ENABLED_AFTER
	"$HELPER_UNDER_TEST" 30 "$TEST_TMP/payload" cancelled &
	helper_pid=$!
	tries=0
	while [ ! -s "$HELPER_SLEEP_STARTED" ] && [ "$tries" -lt 100 ]; do
		/bin/sleep 0.02
		tries=$((tries + 1))
	done
	if [ -s "$HELPER_SLEEP_STARTED" ]; then
		kill -TERM "$helper_pid" 2>/dev/null || true
		wait "$helper_pid" 2>/dev/null || true
		record_ok 'procd TERM cancels the managed delay helper'
	else
		kill -KILL "$helper_pid" 2>/dev/null || true
		wait "$helper_pid" 2>/dev/null || true
		record_failure 'procd TERM cancels the managed delay helper (sleep never started)'
	fi
	assert_file_not_exists "$PAYLOAD_LOG" 'cancelled delay never launches the converter payload'
	assert_file_not_exists "$HELPER_MARKER" 'cancelled delay does not record completion'
	assert_not_contains "$HELPER_SOURCE" '(^|[^[:alnum:]_])pgrep([^[:alnum:]_]|$)' 'delay helper does not depend on pgrep'
fi

finish_tests
