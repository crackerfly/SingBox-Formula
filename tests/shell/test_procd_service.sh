#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

INIT_SOURCE="$REPO_ROOT/openwrt-feed/singbox-formula/files/etc/init.d/singbox-formula"
HELPER_SOURCE="$REPO_ROOT/openwrt-feed/singbox-formula/files/usr/share/singbox-formula/run-delayed.sh"

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-procd-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_BIN="$TEST_TMP/bin"
MOCK_ROOT="$TEST_TMP/root"
PROCD_LOG="$TEST_TMP/procd.log"
START_LOG="$TEST_TMP/start.log"
CONF="$MOCK_ROOT/etc/singbox-formula/config.yaml"
PROG="$MOCK_ROOT/usr/bin/sb-sub-c"
GEN="$MOCK_ROOT/usr/share/singbox-formula/generate-config.sh"
DELAY_HELPER="$MOCK_ROOT/usr/share/singbox-formula/run-delayed.sh"
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
	-e "s|/var/lib/singbox-formula|$MOCK_ROOT/var/lib/singbox-formula|g" \
	-e "s|/var/log/singbox-formula|$MOCK_ROOT/var/log/singbox-formula|g" \
	"$INIT_SOURCE" > "$INIT_UNDER_TEST"

PATH="$MOCK_BIN:$PATH"
export PATH MOCK_CONFIG_BODY MOCK_TIMEZONE

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

start() {
	printf '%s\n' "$*" > "$START_LOG"
	: > "$PROCD_LOG"
	start_service "$@"
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
		start_service "$_mode"
	else
		start_service
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
if start_service boot >/dev/null 2>&1; then
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
assert_file_exists "$HELPER_SOURCE" 'ships a procd-managed boot delay helper'

if [ -f "$HELPER_SOURCE" ]; then
	HELPER_UNDER_TEST="$TEST_TMP/run-delayed-under-test.sh"
	HELPER_FUNCTIONS="$TEST_TMP/helper-functions.sh"
	HELPER_STATE="$TEST_TMP/run/singbox-formula"
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
		-e "s|/var/run/singbox-formula|$HELPER_STATE|g" \
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
