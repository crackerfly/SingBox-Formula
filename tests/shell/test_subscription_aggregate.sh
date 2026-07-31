#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

WRAPPER_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula/wait-subscription-gateway.sh"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-aggregate-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_BIN="$TEST_TMP/bin"
MOCK_FUNCTIONS="$TEST_TMP/functions.sh"
WRAPPER_UNDER_TEST="$TEST_TMP/wait-subscription-gateway.sh"
STATE_DIR="$TEST_TMP/run/liquid-formula"
UPTIME_FILE="$TEST_TMP/proc-uptime"
CURL_LOG="$TEST_TMP/curl.log"
SLEEP_LOG="$TEST_TMP/sleep.log"
SLEEP_STARTED="$TEST_TMP/sleep.started"
UCI_LOG="$TEST_TMP/uci.log"
PAYLOAD_LOG="$TEST_TMP/payload.log"
STDOUT_FILE="$TEST_TMP/wrapper.stdout"
STDERR_FILE="$TEST_TMP/wrapper.stderr"
DIGEST=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
HEALTH_BODY="{\"service\":\"liquid-formula-subscription-gateway\",\"status\":\"ok\",\"config_digest\":\"$DIGEST\"}"

mkdir -p "$MOCK_BIN"

cat > "$MOCK_FUNCTIONS" <<'EOF'
config_load() {
	printf 'load|%s\n' "$*" >> "$MOCK_UCI_LOG"
	[ "${MOCK_CONFIG_LOAD_FAIL:-0}" != 1 ]
}

config_get_bool() {
	variable=$1
	section=$2
	option=$3
	default=$4
	printf 'get_bool|%s|%s|%s\n' "$section" "$option" "$default" >> "$MOCK_UCI_LOG"
	value=${MOCK_ENABLED_AFTER:-$default}
	eval "$variable=\$value"
}
EOF

cat > "$MOCK_BIN/curl" <<'EOF'
#!/bin/sh

printf 'curl' >> "$MOCK_CURL_LOG"
for argument do printf '|%s' "$argument" >> "$MOCK_CURL_LOG"; done
printf '\n' >> "$MOCK_CURL_LOG"

body_output=
header_output=
write_format=
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o|--output)
			[ "$#" -ge 2 ] || exit 2
			body_output=$2
			shift 2
			;;
		-D|--dump-header)
			[ "$#" -ge 2 ] || exit 2
			header_output=$2
			shift 2
			;;
		-w|--write-out)
			[ "$#" -ge 2 ] || exit 2
			write_format=$2
			shift 2
			;;
		--max-time|--connect-timeout|--request|-X)
			[ "$#" -ge 2 ] || exit 2
			shift 2
			;;
		*)
			shift
			;;
	esac
done

if [ "${MOCK_CURL_ADVANCE:-0}" -gt 0 ] 2>/dev/null; then
	current=$(awk '{ print int($1) }' "$MOCK_UPTIME_FILE")
	printf '%s.00 0.00\n' "$((current + MOCK_CURL_ADVANCE))" > "$MOCK_UPTIME_FILE"
fi

[ "${MOCK_CURL_RC:-0}" = 0 ] || exit "$MOCK_CURL_RC"

if [ -n "$header_output" ]; then
	if [ "$header_output" = - ]; then
		printf 'HTTP/1.1 200 OK\r\nContent-Type: %s\r\n\r\n' "$MOCK_CONTENT_TYPE"
	else
		printf 'HTTP/1.1 200 OK\r\nContent-Type: %s\r\n\r\n' "$MOCK_CONTENT_TYPE" > "$header_output"
	fi
fi

if [ -n "$body_output" ]; then
	if [ "$body_output" = - ]; then
		printf '%s' "$MOCK_HEALTH_BODY"
	else
		printf '%s' "$MOCK_HEALTH_BODY" > "$body_output"
	fi
else
	printf '%s' "$MOCK_HEALTH_BODY"
fi

case "$write_format" in
	*content_type*) printf '%s' "$MOCK_CONTENT_TYPE" ;;
esac
exit 0
EOF

cat > "$MOCK_BIN/sleep" <<'EOF'
#!/bin/sh

case "${1:-}" in ''|*[!0-9]*) exit 2 ;; esac
printf '%s\n' "$1" >> "$MOCK_SLEEP_LOG"
printf '%s\n' "$$" > "$MOCK_SLEEP_STARTED"
if [ "${MOCK_BLOCK_SLEEP:-0}" = 1 ]; then
	trap 'exit 143' HUP INT TERM
	while :; do /bin/sleep 1; done
fi
current=$(awk '{ print int($1) }' "$MOCK_UPTIME_FILE")
printf '%s.00 0.00\n' "$((current + $1))" > "$MOCK_UPTIME_FILE"
exit 0
EOF

cat > "$TEST_TMP/payload" <<'EOF'
#!/bin/sh
printf 'payload' >> "$MOCK_PAYLOAD_LOG"
for argument do printf '|%s' "$argument" >> "$MOCK_PAYLOAD_LOG"; done
printf '\n' >> "$MOCK_PAYLOAD_LOG"
exit "${MOCK_PAYLOAD_RC:-0}"
EOF

chmod 0755 "$MOCK_BIN/curl" "$MOCK_BIN/sleep" "$TEST_TMP/payload"

if [ -f "$WRAPPER_SOURCE" ]; then
	sed \
		-e "s|^[[:space:]]*\\.[[:space:]]*/lib/functions\\.sh[[:space:]]*$|. '$MOCK_FUNCTIONS'|" \
		-e "s|/lib/functions\\.sh|$MOCK_FUNCTIONS|g" \
		-e "s|/var/run/liquid-formula|$STATE_DIR|g" \
		-e "s|/proc/uptime|$UPTIME_FILE|g" \
		"$WRAPPER_SOURCE" > "$WRAPPER_UNDER_TEST"
	chmod 0755 "$WRAPPER_UNDER_TEST"
	record_ok 'ships the subscription-gateway readiness wrapper'
else
	cat > "$WRAPPER_UNDER_TEST" <<'EOF'
#!/bin/sh
exit 127
EOF
	chmod 0755 "$WRAPPER_UNDER_TEST"
	record_failure "ships the subscription-gateway readiness wrapper (missing: $WRAPPER_SOURCE)"
fi

export PATH="$MOCK_BIN:$PATH"
export MOCK_CURL_LOG="$CURL_LOG"
export MOCK_SLEEP_LOG="$SLEEP_LOG"
export MOCK_SLEEP_STARTED="$SLEEP_STARTED"
export MOCK_UCI_LOG="$UCI_LOG"
export MOCK_PAYLOAD_LOG="$PAYLOAD_LOG"
export MOCK_UPTIME_FILE="$UPTIME_FILE"
export MOCK_HEALTH_BODY MOCK_CONTENT_TYPE MOCK_CURL_RC MOCK_CURL_ADVANCE
export MOCK_ENABLED_AFTER MOCK_CONFIG_LOAD_FAIL MOCK_BLOCK_SLEEP MOCK_PAYLOAD_RC

reset_mocks() {
	rm -rf "$STATE_DIR"
	rm -f "$CURL_LOG" "$SLEEP_LOG" "$SLEEP_STARTED" "$UCI_LOG" \
		"$PAYLOAD_LOG" "$STDOUT_FILE" "$STDERR_FILE"
	printf '100.75 200.00\n' > "$UPTIME_FILE"
	MOCK_HEALTH_BODY=$HEALTH_BODY
	MOCK_CONTENT_TYPE=application/json
	MOCK_CURL_RC=0
	MOCK_CURL_ADVANCE=0
	MOCK_ENABLED_AFTER=1
	MOCK_CONFIG_LOAD_FAIL=0
	MOCK_BLOCK_SLEEP=0
	MOCK_PAYLOAD_RC=0
	export MOCK_HEALTH_BODY MOCK_CONTENT_TYPE MOCK_CURL_RC MOCK_CURL_ADVANCE
	export MOCK_ENABLED_AFTER MOCK_CONFIG_LOAD_FAIL MOCK_BLOCK_SLEEP MOCK_PAYLOAD_RC
}

run_wrapper() {
	"$WRAPPER_UNDER_TEST" "$@" > "$STDOUT_FILE" 2> "$STDERR_FILE"
	WRAPPER_RC=$?
}

expect_wrapper_success() {
	description=$1
	shift
	run_wrapper "$@"
	if [ "$WRAPPER_RC" -eq 0 ]; then
		record_ok "$description"
	else
		record_failure "$description (exit $WRAPPER_RC: $(cat "$STDERR_FILE"))"
	fi
}

expect_wrapper_failure() {
	description=$1
	shift
	run_wrapper "$@"
	if [ "$WRAPPER_RC" -ne 0 ]; then
		record_ok "$description"
	else
		record_failure "$description (unexpected success)"
	fi
}

assert_no_runtime_side_effects() {
	description=$1
	if [ ! -e "$CURL_LOG" ] && [ ! -e "$SLEEP_LOG" ] && [ ! -e "$PAYLOAD_LOG" ]; then
		record_ok "$description"
	else
		record_failure "$description"
	fi
}

reset_mocks
expect_wrapper_success \
	'manual mode verifies the gateway then execs the converter' \
	manual 37 43211 "$DIGEST" 9 -- "$TEST_TMP/payload" alpha 'beta gamma'
assert_file_content \
	'payload|alpha|beta gamma' \
	"$PAYLOAD_LOG" \
	'passes the converter path and every argument byte-for-byte'
assert_contains \
	"$CURL_LOG" \
	'\|--max-time(\|1|=1)(\||$)' \
	'clips each readiness request to at most one second'
assert_contains \
	"$CURL_LOG" \
	'\|http://127\.0\.0\.1:43211/health(\||$)' \
	'probes only the configured IPv4-loopback health endpoint'
assert_file_not_exists "$SLEEP_LOG" 'manual mode does not apply the boot delay'

reset_mocks
MOCK_PAYLOAD_RC=23
export MOCK_PAYLOAD_RC
run_wrapper reconcile 0 43211 "$DIGEST" 9 -- "$TEST_TMP/payload" reconcile
assert_equal 23 "$WRAPPER_RC" 'reconcile mode execs the payload and propagates its result'
assert_file_content 'payload|reconcile' "$PAYLOAD_LOG" 'reconcile mode uses the same readiness wrapper'

reset_mocks
expect_wrapper_success \
	'boot mode waits, rechecks UCI, verifies health, and starts the converter' \
	boot 3 43211 "$DIGEST" 9 -- "$TEST_TMP/payload" first boot
assert_file_content 3 "$SLEEP_LOG" 'first boot waits for the configured delay'
assert_file_exists "$STATE_DIR/boot-delay.done" 'completed boot delay records a per-boot marker'
assert_contains "$UCI_LOG" '^load\\|liquid_formula$' 'boot delay reloads the Liquid Formula UCI package'
assert_contains "$UCI_LOG" '^get_bool\\|main\\|enabled\\|0$' 'boot delay rechecks the main enabled option'
assert_file_content 'payload|first|boot' "$PAYLOAD_LOG" 'enabled boot continues through readiness to the payload'

: > "$SLEEP_LOG"
: > "$PAYLOAD_LOG"
expect_wrapper_success \
	'a procd respawn skips a boot delay already completed this boot' \
	boot 3 43211 "$DIGEST" 9 -- "$TEST_TMP/payload" respawn
assert_empty "$(cat "$SLEEP_LOG")" 'boot marker prevents a repeated delay'
assert_file_content 'payload|respawn' "$PAYLOAD_LOG" 'boot marker does not skip readiness or payload execution'

reset_mocks
MOCK_ENABLED_AFTER=0
export MOCK_ENABLED_AFTER
expect_wrapper_success \
	'boot cancellation after the delay exits cleanly' \
	boot 3 43211 "$DIGEST" 9 -- "$TEST_TMP/payload" disabled
assert_file_exists "$STATE_DIR/boot-delay.done" 'disabled-after-delay boot still records completed waiting'
assert_file_not_exists "$CURL_LOG" 'disabled-after-delay boot never probes the gateway'
assert_file_not_exists "$PAYLOAD_LOG" 'disabled-after-delay boot never launches the converter'

invalid_case=0
while IFS='|' read -r description arguments; do
	[ -n "$description" ] || continue
	reset_mocks
	# Arguments are fixed test literals and intentionally split here.
	# shellcheck disable=SC2086
	expect_wrapper_failure "$description" $arguments
	assert_no_runtime_side_effects "$description has no runtime side effects"
	invalid_case=$((invalid_case + 1))
done <<EOF
rejects an unknown mode|invalid 0 43211 $DIGEST 9 -- $TEST_TMP/payload
rejects a negative delay|boot -1 43211 $DIGEST 9 -- $TEST_TMP/payload
rejects a delay above 600|boot 601 43211 $DIGEST 9 -- $TEST_TMP/payload
rejects a noncanonical delay|boot 01 43211 $DIGEST 9 -- $TEST_TMP/payload
rejects port zero|manual 0 0 $DIGEST 9 -- $TEST_TMP/payload
rejects a port above 65535|manual 0 65536 $DIGEST 9 -- $TEST_TMP/payload
rejects an uppercase digest|manual 0 43211 ABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCD 9 -- $TEST_TMP/payload
rejects a short digest|manual 0 43211 deadbeef 9 -- $TEST_TMP/payload
rejects a zero readiness budget|manual 0 43211 $DIGEST 0 -- $TEST_TMP/payload
rejects readiness-budget overflow|manual 0 43211 $DIGEST 2147483648 -- $TEST_TMP/payload
rejects a missing separator|manual 0 43211 $DIGEST 9 $TEST_TMP/payload
rejects a missing converter command|manual 0 43211 $DIGEST 9 --
EOF
assert_equal 12 "$invalid_case" 'covers every wrapper argument class'

for response_case in wrong-content-type wrong-service wrong-status wrong-digest extra-field newline malformed; do
	reset_mocks
	case "$response_case" in
		wrong-content-type)
			MOCK_CONTENT_TYPE='application/json; charset=utf-8'
			;;
		wrong-service)
			MOCK_HEALTH_BODY="{\"service\":\"singbox-subscribe-convert\",\"status\":\"ok\",\"config_digest\":\"$DIGEST\"}"
			;;
		wrong-status)
			MOCK_HEALTH_BODY="{\"service\":\"liquid-formula-subscription-gateway\",\"status\":\"starting\",\"config_digest\":\"$DIGEST\"}"
			;;
		wrong-digest)
			MOCK_HEALTH_BODY='{"service":"liquid-formula-subscription-gateway","status":"ok","config_digest":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}'
			;;
		extra-field)
			MOCK_HEALTH_BODY="{\"service\":\"liquid-formula-subscription-gateway\",\"status\":\"ok\",\"config_digest\":\"$DIGEST\",\"extra\":true}"
			;;
		newline)
			MOCK_HEALTH_BODY="$(printf '%s\n_' "$HEALTH_BODY")"
			MOCK_HEALTH_BODY=${MOCK_HEALTH_BODY%_}
			;;
		malformed)
			MOCK_HEALTH_BODY='{"service":'
			;;
	esac
	export MOCK_CONTENT_TYPE MOCK_HEALTH_BODY
	expect_wrapper_failure \
		"rejects health response variant: $response_case" \
		manual 0 43211 "$DIGEST" 1 -- "$TEST_TMP/payload" must-not-run
	assert_file_not_exists "$PAYLOAD_LOG" "$response_case health response never launches the converter"
done

reset_mocks
MOCK_CURL_RC=28
MOCK_CURL_ADVANCE=2
export MOCK_CURL_RC MOCK_CURL_ADVANCE
expect_wrapper_failure \
	'readiness timeout uses monotonic elapsed time from proc uptime' \
	manual 0 43211 "$DIGEST" 2 -- "$TEST_TMP/payload" must-not-run
curl_count=0
[ ! -f "$CURL_LOG" ] || curl_count=$(wc -l < "$CURL_LOG" | tr -d '[:space:]')
assert_equal 1 "$curl_count" 'a request consuming the remaining budget is not retried'
assert_file_not_exists "$SLEEP_LOG" 'does not sleep after curl consumed the remaining budget'
assert_file_not_exists "$PAYLOAD_LOG" 'timeout never launches the converter'

reset_mocks
MOCK_CURL_RC=28
export MOCK_CURL_RC
expect_wrapper_failure \
	'a failed readiness probe exhausts its bounded retry budget' \
	manual 0 43211 "$DIGEST" 3 -- "$TEST_TMP/payload" must-not-run
oversized_sleeps=$(awk '$1 < 1 || $1 > 3 { print }' "$SLEEP_LOG" 2>/dev/null)
sleep_total=$(awk '{ total += $1 } END { print total + 0 }' "$SLEEP_LOG" 2>/dev/null)
[ -n "$sleep_total" ] || sleep_total=0
assert_empty "$oversized_sleeps" 'every readiness sleep is a positive bounded integer'
if [ "$sleep_total" -le 3 ] 2>/dev/null; then
	record_ok 'readiness retries never sleep past the remaining budget'
else
	record_failure "readiness retries never sleep past the remaining budget (slept $sleep_total seconds)"
fi
assert_file_not_exists "$PAYLOAD_LOG" 'failed readiness retries never launch the converter'

reset_mocks
MOCK_BLOCK_SLEEP=1
export MOCK_BLOCK_SLEEP
"$WRAPPER_UNDER_TEST" boot 30 43211 "$DIGEST" 30 -- "$TEST_TMP/payload" cancelled \
	> "$STDOUT_FILE" 2> "$STDERR_FILE" &
wrapper_pid=$!
tries=0
while [ ! -s "$SLEEP_STARTED" ] && [ "$tries" -lt 100 ]; do
	/bin/sleep 0.02
	tries=$((tries + 1))
done
if [ -s "$SLEEP_STARTED" ]; then
	kill -TERM "$wrapper_pid" 2>/dev/null || true
	wait "$wrapper_pid" 2>/dev/null || true
	record_ok 'TERM cancels a managed boot wait'
else
	kill -KILL "$wrapper_pid" 2>/dev/null || true
	wait "$wrapper_pid" 2>/dev/null || true
	record_failure 'TERM cancels a managed boot wait (sleep never started)'
fi
assert_file_not_exists "$STATE_DIR/boot-delay.done" 'cancelled boot wait does not publish its completion marker'
assert_file_not_exists "$PAYLOAD_LOG" 'cancelled boot wait never launches the converter'

reset_mocks
MOCK_CURL_RC=28
MOCK_BLOCK_SLEEP=1
export MOCK_CURL_RC MOCK_BLOCK_SLEEP
"$WRAPPER_UNDER_TEST" manual 0 43211 "$DIGEST" 30 -- "$TEST_TMP/payload" cancelled \
	> "$STDOUT_FILE" 2> "$STDERR_FILE" &
wrapper_pid=$!
tries=0
while [ ! -s "$SLEEP_STARTED" ] && [ "$tries" -lt 100 ]; do
	/bin/sleep 0.02
	tries=$((tries + 1))
done
if [ -s "$SLEEP_STARTED" ]; then
	kill -TERM "$wrapper_pid" 2>/dev/null || true
	wait "$wrapper_pid" 2>/dev/null || true
	record_ok 'TERM cancels a readiness retry wait'
else
	kill -KILL "$wrapper_pid" 2>/dev/null || true
	wait "$wrapper_pid" 2>/dev/null || true
	record_failure 'TERM cancels a readiness retry wait (sleep never started)'
fi
assert_file_not_exists "$PAYLOAD_LOG" 'cancelled readiness retry never launches the converter'

finish_tests
