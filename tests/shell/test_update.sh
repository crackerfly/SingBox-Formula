#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

UPDATE="$REPO_ROOT/openwrt-feed/singbox-formula/files/usr/share/singbox-formula/update.sh"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/singbox-formula-update-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

MOCK_BIN="$TEST_TMP/bin"
MOCK_FUNCTIONS="$TEST_TMP/functions.sh"
MOCK_GENERATOR="$TEST_TMP/generate-config.sh"
MOCK_INIT="$TEST_TMP/init.sh"
RUNTIME="$TEST_TMP/runtime"
TMP_ROOT="$RUNTIME/tmp"
EVENTS="$RUNTIME/events.log"
LOG_FILE="$RUNTIME/update.log"
LOCK_DIR="$RUNTIME/update.lock"
OUTPUT_CONFIG="$RUNTIME/output/config.json"
CACHE_FILE="$RUNTIME/cache/node.json"
mkdir -p "$MOCK_BIN" "$TMP_ROOT" "$(dirname "$OUTPUT_CONFIG")" "$(dirname "$CACHE_FILE")"
SYSTEM_CMP=$(command -v cmp)
SYSTEM_MKDIR=$(command -v mkdir)
export SYSTEM_CMP SYSTEM_MKDIR

cat > "$MOCK_FUNCTIONS" <<'EOF'
config_load() {
	[ "${MOCK_CONFIG_LOAD_FAIL:-0}" != 1 ]
}

config_get() {
	local destination="$1" section="$2" option="$3" default="${4-}"
	local key="UCI_${section}_${option}" is_set __cg_value
	eval "is_set=\${$key+x}"
	if [ "$is_set" = x ]; then
		eval "__cg_value=\${$key}"
	else
		__cg_value=$default
	fi
	eval "$destination=\$__cg_value"
}
EOF

cat > "$MOCK_GENERATOR" <<'EOF'
#!/bin/sh
printf 'generator\n' >> "$MOCK_EVENTS"
[ "${MOCK_GENERATOR_FAIL:-0}" != 1 ]
EOF

cat > "$MOCK_INIT" <<'EOF'
#!/bin/sh
printf 'init:%s\n' "$*" >> "$MOCK_EVENTS"
[ "${MOCK_INIT_FAIL:-0}" != 1 ] || exit 1
[ -z "${MOCK_STARTED_FILE:-}" ] || : > "$MOCK_STARTED_FILE"
exit 0
EOF

cat > "$MOCK_BIN/curl" <<'EOF'
#!/bin/sh
printf 'curl:%s\n' "$*" >> "$MOCK_EVENTS"
url=
output=
want_code=0
code=200
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o) output=$2; shift 2 ;;
		-w) want_code=1; shift 2 ;;
		http://*) url=$1; shift ;;
		*) shift ;;
	esac
done
[ -n "$output" ] && printf 'output:%s\n' "$output" >> "$MOCK_EVENTS"

write_body() {
	if [ -n "$output" ]; then
		printf '%s\n' "$1" > "$output"
	else
		printf '%s\n' "$1"
	fi
}

case "$url" in
	*/health)
		if [ "${MOCK_HEALTH_FAIL:-0}" = 1 ] && { [ -z "${MOCK_STARTED_FILE:-}" ] || [ ! -e "$MOCK_STARTED_FILE" ]; }; then
			exit 22
		fi
		write_body '{"service":"singbox-subscribe-convert","version":"0.7.2-formula","status":"ok"}'
		;;
	*/refresh\?*)
		[ -n "${MOCK_ENTERED_FILE:-}" ] && : > "$MOCK_ENTERED_FILE"
		while [ -n "${MOCK_HOLD_FILE:-}" ] && [ -e "$MOCK_HOLD_FILE" ]; do sleep 0.05; done
		[ "${MOCK_REFRESH_FAIL:-0}" != 1 ] || exit 22
		code=${MOCK_REFRESH_HTTP_CODE:-200}
		case "$code" in
			401) write_body 'Password Error' ;;
			2??) write_body '{"status":"success"}' ;;
			*) write_body '{"status":"error","errors":["mock upstream failure"]}' ;;
		esac
		;;
	*)
		[ "${MOCK_FETCH_FAIL:-0}" != 1 ] || exit 22
		write_body "{\"outbounds\":[{\"tag\":\"${MOCK_GENERATED_SERIAL:-generated}\"}]}"
		;;
esac
[ "$want_code" = 1 ] && printf '%s' "$code"
exit 0
EOF

cat > "$MOCK_BIN/cmp" <<'EOF'
#!/bin/sh
[ "${MOCK_CMP_FAIL:-0}" != 1 ] || exit 2
exec "$SYSTEM_CMP" "$@"
EOF

cat > "$MOCK_BIN/mkdir" <<'EOF'
#!/bin/sh
"$SYSTEM_MKDIR" "$@" || exit $?
last=
for argument do last=$argument; done
if [ "${MOCK_KILL_AFTER_LOCK_CLAIM:-0}" = 1 ] && [ "$last" = "$SBF_LOCK_DIR" ]; then
	kill -KILL "$PPID"
fi
exit 0
EOF

cat > "$MOCK_BIN/jsonfilter" <<'EOF'
#!/bin/sh
file=
expression=
while [ "$#" -gt 0 ]; do
	case "$1" in
		-i) file=$2; shift 2 ;;
		-e) expression=$2; shift 2 ;;
		*) shift ;;
	esac
done
printf 'jsonfilter:%s:%s\n' "$expression" "$file" >> "$MOCK_EVENTS"
case "$expression" in
	'@.service')
		grep -q '"service":"singbox-subscribe-convert"' "$file" || exit 1
		printf 'singbox-subscribe-convert\n'
		;;
	'@.status')
		grep -q '"status":"ok"' "$file" || exit 1
		printf 'ok\n'
		;;
	'@.outbounds')
		[ "${MOCK_JSON_FAIL:-0}" != 1 ] || exit 1
		grep -q '"outbounds"' "$file"
		;;
	*) exit 1 ;;
esac
EOF

cat > "$MOCK_BIN/sing-box" <<'EOF'
#!/bin/sh
printf 'sing-box:%s\n' "$*" >> "$MOCK_EVENTS"
[ "${MOCK_SING_FAIL:-0}" != 1 ]
EOF

cat > "$MOCK_BIN/install" <<'EOF'
#!/bin/sh
printf 'install:%s\n' "$*" >> "$MOCK_EVENTS"
exit 99
EOF

chmod 0755 "$MOCK_GENERATOR" "$MOCK_INIT" "$MOCK_BIN/curl" "$MOCK_BIN/jsonfilter" \
	"$MOCK_BIN/sing-box" "$MOCK_BIN/install" "$MOCK_BIN/cmp" "$MOCK_BIN/mkdir"

export PATH="$MOCK_BIN:$PATH"
export SBF_FUNCTIONS_SH="$MOCK_FUNCTIONS"
export SBF_GENERATOR="$MOCK_GENERATOR"
export SBF_INIT_SCRIPT="$MOCK_INIT"
export SBF_LOG_FILE="$LOG_FILE"
export SBF_LOCK_DIR="$LOCK_DIR"
export SBF_TMP_ROOT="$TMP_ROOT"
export MOCK_EVENTS="$EVENTS"

reset_mocks() {
	rm -rf "$TMP_ROOT" "$LOCK_DIR"
	mkdir -p "$TMP_ROOT" "$(dirname "$OUTPUT_CONFIG")" "$(dirname "$CACHE_FILE")"
	: > "$EVENTS"
	export UCI_main_port=9716
	export UCI_main_enabled=0
	export UCI_main_password='p@ss word&complete'
	export UCI_main_subscription_url='https://provider.example/sub?token=complete-secret'
	export UCI_main_subscription_timeout=60
	export UCI_main_default_template='momo template'
	export UCI_main_output_config="$OUTPUT_CONFIG"
	export MOCK_CONFIG_LOAD_FAIL=0
	export MOCK_GENERATOR_FAIL=0
	export MOCK_INIT_FAIL=0
	export MOCK_HEALTH_FAIL=0
	export MOCK_CMP_FAIL=0
	export MOCK_KILL_AFTER_LOCK_CLAIM=0
	export MOCK_REFRESH_FAIL=0
	export MOCK_FETCH_FAIL=0
	export MOCK_JSON_FAIL=0
	export MOCK_SING_FAIL=0
	export MOCK_GENERATED_SERIAL=generated
	export MOCK_STARTED_FILE="$RUNTIME/converter.started"
	rm -f "$MOCK_STARTED_FILE"
	unset MOCK_HOLD_FILE MOCK_ENTERED_FILE 2>/dev/null || true
}

run_update() {
	command=$1
	"$UPDATE" "$command" >"$TEST_TMP/update.stdout" 2>"$TEST_TMP/update.stderr"
	UPDATE_RC=$?
}

expect_update_success() {
	command=$1
	description=$2
	run_update "$command"
	if [ "$UPDATE_RC" -eq 0 ]; then
		record_ok "$description"
	else
		record_failure "$description (exit $UPDATE_RC: $(cat "$TEST_TMP/update.stderr"))"
	fi
}

expect_update_failure() {
	command=$1
	description=$2
	run_update "$command"
	if [ "$UPDATE_RC" -ne 0 ]; then
		record_ok "$description"
	else
		record_failure "$description (unexpected success)"
	fi
}

event_count() {
	pattern=$1
	grep -c "$pattern" "$EVENTS" 2>/dev/null || true
}

reset_mocks
expect_update_success refresh "refresh succeeds against a healthy converter"
assert_equal 0 "$(event_count '^generator$')" "refresh never invokes the config generator"
assert_contains "$EVENTS" '/refresh\?password=p%40ss%20word%26complete' "refresh preserves the complete percent-encoded password"
assert_contains "$EVENTS" '--max-time 120' "refresh uses subscription timeout plus 60"
assert_equal 600 "$(stat -c %a "$LOG_FILE" 2>/dev/null)" "creates update.log with mode 0600"
assert_file_not_exists "$LOCK_DIR" "releases the updater lock after refresh"

# 刷新失败时必须把真实原因写进 update.log, 否则界面只报“Operation failed”而无从排查。
reset_mocks
export MOCK_REFRESH_HTTP_CODE=401
expect_update_failure refresh "refresh fails when the converter rejects the password"
assert_contains "$LOG_FILE" 'HTTP 401' "records the 401 status in the update log"
assert_contains "$LOG_FILE" 'does not match the UCI password' "explains a password mismatch in the update log"
unset MOCK_REFRESH_HTTP_CODE

reset_mocks
export MOCK_REFRESH_HTTP_CODE=500
expect_update_failure refresh "refresh fails when the converter returns a server error"
assert_contains "$LOG_FILE" 'HTTP 500' "records the 500 status in the update log"
assert_contains "$LOG_FILE" 'mock upstream failure' "records the converter response body in the update log"
unset MOCK_REFRESH_HTTP_CODE

reset_mocks
export MOCK_HEALTH_FAIL=1
expect_update_failure refresh "refresh fails when the health probe does not answer"
assert_contains "$LOG_FILE" '/health' "points at the health endpoint when the probe fails"
unset MOCK_HEALTH_FAIL
temp_entries=$(find "$TMP_ROOT" -mindepth 1 -print)
assert_empty "$temp_entries" "cleans the refresh working directory"
first_refresh_output=$(sed -n 's/^output:\(.*refresh.*\.json\)$/\1/p' "$EVENTS" | tail -n 1)
expect_update_success refresh "a second refresh succeeds"
second_refresh_output=$(sed -n 's/^output:\(.*refresh.*\.json\)$/\1/p' "$EVENTS" | tail -n 1)
assert_not_equal "$first_refresh_output" "$second_refresh_output" "uses a unique refresh response path per operation"

reset_mocks
export MOCK_HEALTH_FAIL=1
expect_update_failure refresh "refresh fails when a disabled converter is stopped"
assert_equal 0 "$(event_count '^generator$')" "stopped refresh never invokes the generator"
assert_equal 0 "$(event_count '^init:')" "stopped refresh never auto-starts the converter"
assert_equal 0 "$(grep -F -c '/refresh?' "$EVENTS" 2>/dev/null || true)" "stopped refresh never calls the refresh endpoint"

reset_mocks
export MOCK_HEALTH_FAIL=1
expect_update_success check "check may auto-start a stopped disabled converter"
assert_equal 1 "$(event_count '^generator$')" "stopped check generates converter config once"
assert_equal 1 "$(event_count '^init:start manual$')" "stopped check uses the exact manual procd start mode"

reset_mocks
export UCI_main_password=
expect_update_failure refresh "rejects an empty updater authentication password"
assert_equal 0 "$(event_count '^curl:')" "empty password fails before any HTTP request"

reset_mocks
expect_update_success generate "generate runs under the updater"
assert_equal 1 "$(event_count '^generator$')" "generate invokes the generator exactly once"
assert_equal 0 "$(event_count '^curl:')" "generate performs no HTTP request"

reset_mocks
mkdir -p "$LOCK_DIR"
live_owner_info="$TEST_TMP/live-owner.info"
sh -c '
	IFS= read -r stat_line < /proc/self/stat || exit 1
	pid=${stat_line%% *}
	start=$(printf "%s\n" "$stat_line" | awk '\''{ line=$0; sub(/^.*\) /, "", line); split(line, field, " "); print field[20] }'\'')
	printf "%s %s\n" "$pid" "$start"
	sleep 30
' > "$live_owner_info" &
live_owner_job=$!
n=0
while [ ! -s "$live_owner_info" ] && [ "$n" -lt 100 ]; do sleep 0.05; n=$((n + 1)); done
read -r live_owner_pid live_owner_start < "$live_owner_info"
printf '%s %s %s\n' "$live_owner_pid" "$live_owner_start" live-owner > "$LOCK_DIR/owner"
expect_update_failure generate "a live lock owner makes the updater busy"
assert_equal 0 "$(event_count '^generator$')" "busy updater performs no generator side effect"
kill "$live_owner_job" 2>/dev/null || true
wait "$live_owner_job" 2>/dev/null || true
rm -rf "$LOCK_DIR"

reset_mocks
mkdir -p "$LOCK_DIR"
printf '999999 1 dead-owner\n' > "$LOCK_DIR/owner"
expect_update_success generate "recovers a dead lock owner"
assert_file_not_exists "$LOCK_DIR" "removes the recovered lock after completion"

reset_mocks
export MOCK_KILL_AFTER_LOCK_CLAIM=1
expect_update_failure generate "simulates interruption immediately after the atomic directory claim"
if [ -d "$LOCK_DIR" ] && [ ! -e "$LOCK_DIR/owner" ]; then
	record_ok "claim interruption leaves only an empty recoverable lock directory"
else
	record_failure "claim interruption leaves only an empty recoverable lock directory"
fi
export MOCK_KILL_AFTER_LOCK_CLAIM=0
expect_update_success generate "next updater safely recovers an interrupted empty claim"
assert_file_not_exists "$LOCK_DIR" "recovered claim is released after completion"

reset_mocks
HOLD_FILE="$RUNTIME/hold"
ENTERED_FILE="$RUNTIME/entered"
: > "$HOLD_FILE"
export MOCK_HOLD_FILE="$HOLD_FILE" MOCK_ENTERED_FILE="$ENTERED_FILE"
( "$UPDATE" refresh >"$TEST_TMP/owner.stdout" 2>"$TEST_TMP/owner.stderr"; printf '%s' "$?" > "$TEST_TMP/owner.rc" ) &
owner_job=$!
n=0
while [ ! -e "$ENTERED_FILE" ] && [ "$n" -lt 100 ]; do sleep 0.05; n=$((n + 1)); done
if [ -e "$ENTERED_FILE" ]; then record_ok "first parallel updater reaches refresh"; else record_failure "first parallel updater reaches refresh"; fi

contender_pids=
i=1
while [ "$i" -le 8 ]; do
	( "$UPDATE" refresh >"$TEST_TMP/contender.$i.stdout" 2>"$TEST_TMP/contender.$i.stderr"; printf '%s' "$?" > "$TEST_TMP/contender.$i.rc" ) &
	contender_pids="$contender_pids $!"
	i=$((i + 1))
done
for pid in $contender_pids; do wait "$pid"; done
busy_count=0
i=1
while [ "$i" -le 8 ]; do
	[ "$(cat "$TEST_TMP/contender.$i.rc")" -ne 0 ] && busy_count=$((busy_count + 1))
	i=$((i + 1))
done
assert_equal 8 "$busy_count" "only one of nine parallel updater calls owns the lock"
rm -f "$HOLD_FILE"
wait "$owner_job"
refresh_calls=$(grep -F -c '/refresh?' "$EVENTS" 2>/dev/null || true)
assert_equal 1 "$refresh_calls" "only the lock winner reaches converter refresh"
unset MOCK_HOLD_FILE MOCK_ENTERED_FILE

reset_mocks
: > "$HOLD_FILE"
export MOCK_HOLD_FILE="$HOLD_FILE" MOCK_ENTERED_FILE="$ENTERED_FILE"
rm -f "$ENTERED_FILE"
( "$UPDATE" refresh >/dev/null 2>&1; printf '%s' "$?" > "$TEST_TMP/token-owner.rc" ) &
token_job=$!
n=0
while [ ! -f "$LOCK_DIR/owner" ] && [ "$n" -lt 100 ]; do sleep 0.05; n=$((n + 1)); done
if [ -f "$LOCK_DIR/owner" ]; then
	printf '999999 1 replacement-owner\n' > "$LOCK_DIR/owner"
	record_ok "can simulate lock ownership replacement"
else
	record_failure "can simulate lock ownership replacement"
fi
rm -f "$HOLD_FILE"
wait "$token_job"
assert_file_exists "$LOCK_DIR/owner" "old owner cleanup does not remove a replacement token"
rm -rf "$LOCK_DIR"
unset MOCK_HOLD_FILE MOCK_ENTERED_FILE

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
printf 'last-known-good-cache\n' > "$CACHE_FILE"
export MOCK_JSON_FAIL=1
expect_update_failure apply "rejects generated data when jsonfilter fails"
assert_file_content old-output "$OUTPUT_CONFIG" "JSON failure preserves the installed output"
assert_file_content last-known-good-cache "$CACHE_FILE" "JSON failure preserves converter cache"
assert_equal 0 "$(event_count '^sing-box:')" "does not run sing-box after JSON validation failure"

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_SING_FAIL=1
expect_update_failure apply "rejects generated data when sing-box check fails"
assert_file_content old-output "$OUTPUT_CONFIG" "sing-box failure preserves the installed output"
json_line=$(grep -n '^jsonfilter:@.outbounds:' "$EVENTS" | tail -n 1 | cut -d: -f1)
sing_line=$(grep -n '^sing-box:' "$EVENTS" | tail -n 1 | cut -d: -f1)
if [ -n "$json_line" ] && [ -n "$sing_line" ] && [ "$json_line" -lt "$sing_line" ]; then
	record_ok "always runs jsonfilter before optional sing-box validation"
else
	record_failure "always runs jsonfilter before optional sing-box validation"
fi

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_FETCH_FAIL=1
expect_update_failure apply "fails when generated profile download fails"
assert_file_content old-output "$OUTPUT_CONFIG" "download failure preserves the installed output"

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
printf 'last-known-good-cache\n' > "$CACHE_FILE"
export MOCK_CMP_FAIL=1 MOCK_GENERATED_SERIAL=new-output
expect_update_failure apply "treats updater cmp I/O failure as fatal"
assert_file_content old-output "$OUTPUT_CONFIG" "updater cmp failure preserves installed output"
assert_file_content last-known-good-cache "$CACHE_FILE" "updater cmp failure preserves converter cache"

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_REFRESH_FAIL=1 MOCK_GENERATED_SERIAL=from-cache
expect_update_success apply "apply falls back to last-known-good converter data after refresh failure"
assert_contains "$OUTPUT_CONFIG" 'from-cache' "cached fallback installs validated converter output"

reset_mocks
printf 'seed\n' > "$OUTPUT_CONFIG"
i=1
while [ "$i" -le 7 ]; do
	export MOCK_GENERATED_SERIAL="version-$i"
	expect_update_success apply "applies generated profile version $i"
	i=$((i + 1))
done
set -- "$OUTPUT_CONFIG".bak.*
if [ -e "$1" ]; then backup_count=$#; else backup_count=0; fi
assert_equal 5 "$backup_count" "retains exactly five output backups"
assert_contains "$OUTPUT_CONFIG" 'version-7' "atomically installs the latest generated profile"
assert_equal 600 "$(stat -c %a "$OUTPUT_CONFIG")" "installs generated output with mode 0600"
assert_equal 0 "$(event_count '^install:')" "never invokes the unavailable install utility"

reset_mocks
printf '{"outbounds":[{"tag":"same"}]}\n' > "$OUTPUT_CONFIG"
rm -f "$OUTPUT_CONFIG".bak.*
i=1
while [ "$i" -le 7 ]; do
	printf 'backup-%s\n' "$i" > "$OUTPUT_CONFIG.bak.seed-$i"
	i=$((i + 1))
done
export MOCK_GENERATED_SERIAL=same
expect_update_success apply "handles an unchanged generated output"
set -- "$OUTPUT_CONFIG".bak.*
if [ -e "$1" ]; then noop_backup_count=$#; else noop_backup_count=0; fi
assert_equal 5 "$noop_backup_count" "prunes backup retention even when generated output is unchanged"

reset_mocks
expect_update_success check "checks generated output without installing it"
check_output=$(sed -n 's/^output:\(.*generated.*\.json\)$/\1/p' "$EVENTS" | tail -n 1)
if [ -n "$check_output" ] && [ ! -e "$check_output" ]; then
	record_ok "check cleans its unique generated file"
else
	record_failure "check cleans its unique generated file"
fi

reset_mocks
rm -f "$LOG_FILE" "$LOG_FILE.1" "$LOG_FILE.2" "$LOG_FILE.3"
dd if=/dev/zero of="$LOG_FILE" bs=1024 count=256 >/dev/null 2>&1
chmod 0644 "$LOG_FILE"
expect_update_success generate "rotates a full update log"
assert_file_exists "$LOG_FILE.1" "moves a full update log to backup one"
assert_equal 600 "$(stat -c %a "$LOG_FILE")" "recreates the current log with mode 0600"
dd if=/dev/zero of="$LOG_FILE" bs=1024 count=256 >/dev/null 2>&1
expect_update_success generate "rotates the update log a second time"
assert_file_exists "$LOG_FILE.2" "retains a second rotated update log"
assert_file_not_exists "$LOG_FILE.3" "never retains a third rotated update log"

reset_mocks
expect_update_failure unknown "rejects an unknown updater command"
assert_file_not_exists "$LOCK_DIR" "unknown command does not acquire the updater lock"
assert_equal 0 "$(event_count '.')" "unknown command performs no operation"

finish_tests
