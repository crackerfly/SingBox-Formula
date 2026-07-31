#!/bin/sh

set -u

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)

. "$SCRIPT_DIR/harness.sh"

UPDATE_SOURCE="$REPO_ROOT/openwrt-feed/liquid-formula/files/usr/share/liquid-formula/update.sh"
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/liquid-formula-update-test.XXXXXX") || exit 1
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

UPDATE="$TEST_TMP/update-under-test.sh"
MOCK_BIN="$TEST_TMP/bin"
MOCK_FUNCTIONS="$TEST_TMP/functions.sh"
MOCK_GENERATOR="$TEST_TMP/generate-config.sh"
MOCK_INIT="$TEST_TMP/init.sh"
MOCK_ADVANCE_GENERATION="$TEST_TMP/advance-generation.sh"
MOCK_FAULT_HOOK="$TEST_TMP/fault-hook.sh"
RUNTIME="$TEST_TMP/runtime"
TMP_ROOT="$RUNTIME/tmp"
EVENTS="$RUNTIME/events.log"
LOG_FILE="$RUNTIME/update.log"
LOCK_DIR="$RUNTIME/update.lock"
OUTPUT_CONFIG="$RUNTIME/output/config.json"
CACHE_FILE="$RUNTIME/cache/node.json"
CONFIG_FILE="$RUNTIME/etc/liquid-formula/config.yaml"
SUBSCRIPTION_STATE="$RUNTIME/var/lib/liquid-formula/subscriptions"
CURRENT_FILE="$SUBSCRIPTION_STATE/current"
SUBSCRIPTION_LOCK_FILE="$RUNTIME/subscription.lock"
SUBSCRIPTION_BARRIER_FILE="$SUBSCRIPTION_LOCK_FILE.barrier"
SUBSCRIPTION_URLS="$RUNTIME/subscription-url.list"
GENERATION_COUNTER="$RUNTIME/generation.counter"
FAULT_LOG="$RUNTIME/fault.log"
mkdir -p "$MOCK_BIN" "$TMP_ROOT" "$(dirname "$OUTPUT_CONFIG")" "$(dirname "$CACHE_FILE")" \
	"$(dirname "$CONFIG_FILE")"
SYSTEM_CMP=$(command -v cmp)
SYSTEM_FLOCK=$(command -v flock)
SYSTEM_MKDIR=$(command -v mkdir)
export SYSTEM_CMP SYSTEM_FLOCK SYSTEM_MKDIR

sed \
	-e "s|/etc/liquid-formula/config\\.yaml|$CONFIG_FILE|g" \
	-e "s|/var/lib/liquid-formula/subscriptions|$SUBSCRIPTION_STATE|g" \
	"$UPDATE_SOURCE" > "$UPDATE"
chmod 0755 "$UPDATE"

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

config_get_bool() {
	config_get "$@"
}

config_list_foreach() {
	local section="$1" option="$2" callback="$3" key list_file item
	key="UCI_${section}_${option}_LIST_FILE"
	eval "list_file=\${$key:-}"
	[ -n "$list_file" ] && [ -f "$list_file" ] || return 0
	while IFS= read -r item || [ -n "$item" ]; do
		"$callback" "$item" || return $?
	done < "$list_file"
}

config_foreach() {
	local callback="$1" type="$2" section
	[ "$type" = template ] || return 0
	[ "${MOCK_FOREACH_FAIL:-0}" != 1 ] || return 74
	for section in ${UCI_TEMPLATE_IDS:-momo_template}; do
		"$callback" "$section" || return $?
	done
	return 0
}
EOF

cat > "$MOCK_GENERATOR" <<'EOF'
#!/bin/sh
printf 'generator\n' >> "$MOCK_EVENTS"
printf 'generator-include-disabled:%s\n' "${SBF_INCLUDE_DISABLED_URLS:-0}" >> "$MOCK_EVENTS"
[ "${MOCK_GENERATOR_FAIL:-0}" != 1 ] || exit 74
printf '%s' "${MOCK_CONFIG_BODY:-gateway-config-v1}" > "$MOCK_CONFIG_FILE"
EOF

cat > "$MOCK_ADVANCE_GENERATION" <<'EOF'
#!/bin/sh

set -eu

config_digest=$1
generation_mode=${MOCK_GENERATION_MODE:-advance}
if [ "$generation_mode" = wrong-config ]; then
	config_digest=ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
fi
counter=1
[ ! -f "$MOCK_GENERATION_COUNTER" ] || counter=$(cat "$MOCK_GENERATION_COUNTER")
case "$counter" in ''|*[!0-9]*) exit 1 ;; esac
counter=$((counter + 1))
printf '%s\n' "$counter" > "$MOCK_GENERATION_COUNTER"
generation=$(printf '%064d' "$counter")
parent=
if [ -f "$MOCK_CURRENT_FILE" ]; then
	IFS= read -r parent < "$MOCK_CURRENT_FILE" || exit 1
fi

object="{\"outbounds\":[{\"server\":\"192.0.2.1\",\"server_port\":1080,\"tag\":\"fixture-node-$generation\",\"type\":\"socks\"}]}"
aggregate=$object
object_sha=$(printf '%s' "$object" | sha256sum)
object_sha=${object_sha%% *}
aggregate_sha=$object_sha
object_bytes=$(printf '%s' "$object" | wc -c | tr -d '[:space:]')
aggregate_bytes=$object_bytes

mkdir -p "$MOCK_SUBSCRIPTION_STATE/objects" \
	"$MOCK_SUBSCRIPTION_STATE/generations/$generation"
chmod 0700 "$MOCK_SUBSCRIPTION_STATE" \
	"$MOCK_SUBSCRIPTION_STATE/objects" \
	"$MOCK_SUBSCRIPTION_STATE/generations" \
	"$MOCK_SUBSCRIPTION_STATE/generations/$generation"
if [ ! -f "$MOCK_SUBSCRIPTION_STATE/objects/$object_sha.json" ]; then
	printf '%s' "$object" > "$MOCK_SUBSCRIPTION_STATE/objects/$object_sha.json"
	chmod 0600 "$MOCK_SUBSCRIPTION_STATE/objects/$object_sha.json"
fi

sources=
status_sources=
index=0
while IFS= read -r url || [ -n "$url" ]; do
	index=$((index + 1))
	url_sha=$(printf '%s' "$url" | sha256sum)
	url_sha=${url_sha%% *}
	source="{\"index\":$index,\"url_sha256\":\"$url_sha\",\"object_sha256\":\"$object_sha\",\"bytes\":$object_bytes,\"outbounds\":1}"
	status_source="{\"index\":$index,\"result\":\"fresh\",\"fetch_code\":\"ok\",\"format\":\"singbox-json\",\"accepted\":1,\"skipped\":0,\"warnings\":[]}"
	sources="${sources}${sources:+,}$source"
	status_sources="${status_sources}${status_sources:+,}$status_source"
done < "$MOCK_SUBSCRIPTION_URLS"
[ "$index" -gt 0 ] || exit 1

status="{\"schema\":1,\"generation\":\"$generation\",\"state\":\"fresh\",\"fresh_count\":$index,\"fallback_indices\":[],\"sources\":[$status_sources]}"
status_sha=$(printf '%s' "$status" | sha256sum)
status_sha=${status_sha%% *}
manifest="{\"schema\":1,\"generation\":\"$generation\",\"parent\":\"$parent\",\"config_digest\":\"$config_digest\",\"aggregate\":{\"sha256\":\"$aggregate_sha\",\"bytes\":$aggregate_bytes,\"outbounds\":1},\"status_sha256\":\"$status_sha\",\"sources\":[$sources],\"legacy_consumed_url_sha256\":\"\"}"

generation_dir="$MOCK_SUBSCRIPTION_STATE/generations/$generation"
printf '%s' "$aggregate" > "$generation_dir/aggregate.json"
printf '%s' "$manifest" > "$generation_dir/manifest.json"
printf '%s' "$status" > "$generation_dir/status.json"
chmod 0600 "$generation_dir/aggregate.json" "$generation_dir/manifest.json" "$generation_dir/status.json"

case "$generation_mode" in
	advance|advance-twice|wrong-config)
		printf '%s\n' "$generation" > "$MOCK_CURRENT_FILE"
		chmod 0600 "$MOCK_CURRENT_FILE"
		;;
	invalid-generation)
		rm -f "$generation_dir/status.json"
		printf '%s\n' "$generation" > "$MOCK_CURRENT_FILE"
		chmod 0600 "$MOCK_CURRENT_FILE"
		;;
	*)
		exit 2
		;;
esac

if [ "$generation_mode" = advance-twice ]; then
	MOCK_GENERATION_MODE=advance "$0" "$config_digest" >/dev/null
fi
printf '%s\n' "$generation"
EOF

cat > "$MOCK_FAULT_HOOK" <<'EOF'
#!/bin/sh

stage=${1:-}
printf '%s\n' "$stage" >> "$MOCK_FAULT_LOG"
printf 'fault:%s\n' "$stage" >> "$MOCK_EVENTS"
[ "$stage" = before_final_output_rename ] || exit 97
case "${MOCK_FINAL_RENAME_ACTION:-none}" in
	none)
		exit 0
		;;
	fail)
		exit 74
		;;
	advance-same-config)
		MOCK_GENERATION_MODE=advance "$MOCK_ADVANCE_GENERATION" "$MOCK_CONFIG_DIGEST" >/dev/null
		exit $?
		;;
	advance-wrong-config)
		MOCK_GENERATION_MODE=wrong-config "$MOCK_ADVANCE_GENERATION" "$MOCK_CONFIG_DIGEST" >/dev/null
		exit $?
		;;
	*)
		exit 98
		;;
esac
EOF

cat > "$MOCK_INIT" <<'EOF'
#!/bin/sh
printf 'init:%s\n' "$*" >> "$MOCK_EVENTS"
printf 'init-token:%s:%s\n' "$1" "${LF_LIFECYCLE_TOKEN:-}" >> "$MOCK_EVENTS"
case "$1" in
	running)
		[ -n "${MOCK_RUNNING_FILE:-}" ] && [ -e "$MOCK_RUNNING_FILE" ]
		;;
	start)
		[ "${MOCK_INIT_FAIL:-0}" != 1 ] || exit 1
		[ -z "${MOCK_RUNNING_FILE:-}" ] || : > "$MOCK_RUNNING_FILE"
		[ -z "${MOCK_STARTED_FILE:-}" ] || : > "$MOCK_STARTED_FILE"
		[ "${MOCK_INIT_FAIL_AFTER_START:-0}" != 1 ] || exit 1
		;;
	stop)
		[ "${MOCK_INIT_STOP_FAIL:-0}" != 1 ] || exit 1
		[ -z "${MOCK_RUNNING_FILE:-}" ] || rm -f "$MOCK_RUNNING_FILE"
		[ -z "${MOCK_STARTED_FILE:-}" ] || rm -f "$MOCK_STARTED_FILE"
		;;
	*) exit 2 ;;
esac
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
		if [ "${MOCK_HEALTH_ALWAYS_FAIL:-0}" = 1 ]; then
			exit 22
		fi
		if [ "${MOCK_HEALTH_FAIL:-0}" = 1 ] && { [ -z "${MOCK_STARTED_FILE:-}" ] || [ ! -e "$MOCK_STARTED_FILE" ]; }; then
			exit 22
		fi
		write_body '{"service":"singbox-subscribe-convert","version":"0.7.2-formula","status":"ok"}'
		;;
	*\?password=*\&template=%21liquid_formula_barrier\&refresh=1)
		if [ -f "${MOCK_SUBSCRIPTION_BARRIER_FILE:-}" ]; then
			printf 'barrier-refresh\n' >> "$MOCK_EVENTS"
			printf 'barrier-mode:%s\n' \
				"$(stat -c %a "$MOCK_SUBSCRIPTION_BARRIER_FILE" 2>/dev/null)" \
				>> "$MOCK_EVENTS"
			if [ "${MOCK_BARRIER_ADVANCE:-0}" = 1 ]; then
				barrier_generation=$(
					MOCK_GENERATION_MODE=advance \
						"$MOCK_ADVANCE_GENERATION" "$MOCK_CONFIG_DIGEST"
				) || exit 76
				cp "$MOCK_SUBSCRIPTION_STATE/generations/$barrier_generation/aggregate.json" \
					"$MOCK_CACHE_FILE" || exit 77
			fi
		fi
		code=${MOCK_BARRIER_HTTP_CODE:-400}
		barrier_body=${MOCK_BARRIER_BODY:-"Template Error: template '!liquid_formula_barrier' not found in configuration"}
		if [ -n "$output" ]; then
			printf '%s' "$barrier_body" > "$output"
		else
			printf '%s' "$barrier_body"
		fi
		;;
	*/refresh\?*)
		[ -n "${MOCK_ENTERED_FILE:-}" ] && : > "$MOCK_ENTERED_FILE"
		while [ -n "${MOCK_HOLD_FILE:-}" ] && [ -e "$MOCK_HOLD_FILE" ]; do sleep 0.05; done
		[ "${MOCK_REFRESH_FAIL:-0}" != 1 ] || exit 22
		code=${MOCK_REFRESH_HTTP_CODE:-200}
		case "$code" in
			401) write_body 'Password Error' ;;
			2??)
				case "${MOCK_GENERATION_MODE:-advance}" in
					advance|advance-twice|wrong-config|invalid-generation)
						served_generation=$(
							"$MOCK_ADVANCE_GENERATION" "$MOCK_CONFIG_DIGEST"
						) || exit 70
						if [ "${MOCK_SKIP_CACHE_WRITE:-0}" != 1 ]; then
							cp "$MOCK_SUBSCRIPTION_STATE/generations/$served_generation/aggregate.json" \
								"$MOCK_CACHE_FILE" || exit 75
							chmod 0644 "$MOCK_CACHE_FILE" || exit 75
						fi
						;;
					no-advance)
						;;
					missing-current)
						rm -f "$MOCK_CURRENT_FILE"
						;;
					invalid-current)
						printf 'not-a-generation\n' > "$MOCK_CURRENT_FILE"
						chmod 0600 "$MOCK_CURRENT_FILE"
						;;
					*)
						exit 71
						;;
				esac
				write_body '{"status":"success"}'
				;;
			*) write_body '{"service":"liquid-formula-subscription-gateway","status":"error","code":"source_unavailable","failure_stage":"source_fetch","fetch_code":"http_status","source_index":2,"preserved":true}' ;;
		esac
		;;
	*)
		[ "${MOCK_FETCH_FAIL:-0}" != 1 ] || exit 22
		served_generation=
		if [ "${MOCK_BIND_OUTPUT_TO_GENERATION:-0}" = 1 ]; then
			IFS= read -r served_generation < "$MOCK_CURRENT_FILE" || exit 74
			printf 'served-generation:%s\n' "$served_generation" >> "$MOCK_EVENTS"
		fi
		case "${MOCK_AFTER_FETCH_GENERATION_MODE:-none}" in
			none)
				;;
			advance|wrong-config|invalid-generation)
				MOCK_GENERATION_MODE=$MOCK_AFTER_FETCH_GENERATION_MODE \
					"$MOCK_ADVANCE_GENERATION" "$MOCK_CONFIG_DIGEST" >/dev/null || exit 72
				;;
			missing-current)
				rm -f "$MOCK_CURRENT_FILE"
				;;
			invalid-current)
				printf 'not-a-generation\n' > "$MOCK_CURRENT_FILE"
				chmod 0600 "$MOCK_CURRENT_FILE"
				;;
			*)
				exit 73
				;;
		esac
		generated_serial=${MOCK_GENERATED_SERIAL:-generated}
		[ -z "$served_generation" ] || generated_serial="generation-$served_generation"
		write_body "{\"outbounds\":[{\"tag\":\"$generated_serial\"}]}"
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

cat > "$MOCK_BIN/flock" <<'EOF'
#!/bin/sh
if [ "${MOCK_FLOCK_REPLACE_PATH:-0}" = 1 ] &&
   [ "$1" = -n ] && [ ! -e "$MOCK_FLOCK_REPLACED" ]; then
	mv "$SBF_SUBSCRIPTION_LOCK_FILE" "$SBF_SUBSCRIPTION_LOCK_FILE.opened" ||
		exit 70
	: > "$SBF_SUBSCRIPTION_LOCK_FILE" || exit 71
	chmod 0600 "$SBF_SUBSCRIPTION_LOCK_FILE" || exit 72
	: > "$MOCK_FLOCK_REPLACED"
fi
exec "$SYSTEM_FLOCK" "$@"
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
	'@.schema')
		sed -n 's/.*"schema":\([0-9][0-9]*\).*/\1/p' "$file" | head -n 1
		;;
	'@.generation')
		sed -n 's/.*"generation":"\([^"]*\)".*/\1/p' "$file" | head -n 1
		;;
	'@.parent')
		sed -n 's/.*"parent":"\([^"]*\)".*/\1/p' "$file" | head -n 1
		;;
	'@.config_digest')
		sed -n 's/.*"config_digest":"\([^"]*\)".*/\1/p' "$file" | head -n 1
		;;
	'@.aggregate.sha256')
		sed -n 's/.*"aggregate":{"sha256":"\([^"]*\)".*/\1/p' "$file" | head -n 1
		;;
	'@.aggregate.bytes')
		sed -n 's/.*"aggregate":{"sha256":"[^"]*","bytes":\([0-9][0-9]*\).*/\1/p' "$file" | head -n 1
		;;
	'@.aggregate.outbounds')
		sed -n 's/.*"aggregate":{"sha256":"[^"]*","bytes":[0-9][0-9]*,"outbounds":\([0-9][0-9]*\).*/\1/p' "$file" | head -n 1
		;;
	'@.status_sha256')
		sed -n 's/.*"status_sha256":"\([^"]*\)".*/\1/p' "$file" | head -n 1
		;;
	'@.state')
		sed -n 's/.*"state":"\([^"]*\)".*/\1/p' "$file" | head -n 1
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

chmod 0755 "$MOCK_GENERATOR" "$MOCK_INIT" "$MOCK_ADVANCE_GENERATION" "$MOCK_FAULT_HOOK" \
	"$MOCK_BIN/curl" "$MOCK_BIN/jsonfilter" "$MOCK_BIN/sing-box" "$MOCK_BIN/install" \
	"$MOCK_BIN/cmp" "$MOCK_BIN/flock" "$MOCK_BIN/mkdir"

export PATH="$MOCK_BIN:$PATH"
export SBF_FUNCTIONS_SH="$MOCK_FUNCTIONS"
export SBF_GENERATOR="$MOCK_GENERATOR"
export SBF_INIT_SCRIPT="$MOCK_INIT"
export SBF_LOG_FILE="$LOG_FILE"
export SBF_LOCK_DIR="$LOCK_DIR"
export SBF_LIFECYCLE_LOCK_DIR="$RUNTIME/lifecycle.lock"
export SBF_SUBSCRIPTION_LOCK_FILE="$SUBSCRIPTION_LOCK_FILE"
export SBF_SUBSCRIPTION_BARRIER_FILE="$SUBSCRIPTION_BARRIER_FILE"
export SBF_TMP_ROOT="$TMP_ROOT"
export SBF_TEST_FAULT_HOOK="$MOCK_FAULT_HOOK"
export MOCK_EVENTS="$EVENTS"
export MOCK_CONFIG_FILE="$CONFIG_FILE"
export MOCK_SUBSCRIPTION_STATE="$SUBSCRIPTION_STATE"
export MOCK_CACHE_FILE="$CACHE_FILE"
export MOCK_CURRENT_FILE="$CURRENT_FILE"
export MOCK_SUBSCRIPTION_URLS="$SUBSCRIPTION_URLS"
export MOCK_GENERATION_COUNTER="$GENERATION_COUNTER"
export MOCK_ADVANCE_GENERATION="$MOCK_ADVANCE_GENERATION"
export MOCK_FAULT_LOG="$FAULT_LOG"
export MOCK_SUBSCRIPTION_BARRIER_FILE="$SUBSCRIPTION_BARRIER_FILE"
export MOCK_FLOCK_REPLACED="$RUNTIME/flock-replaced"

reset_mocks() {
	rm -rf "$TMP_ROOT" "$LOCK_DIR" "$SBF_LIFECYCLE_LOCK_DIR" "$SUBSCRIPTION_STATE"
	rm -f "$SUBSCRIPTION_URLS" "$GENERATION_COUNTER" "$FAULT_LOG" "$CONFIG_FILE" \
		"$SUBSCRIPTION_LOCK_FILE" "$SUBSCRIPTION_LOCK_FILE.opened" \
		"$SUBSCRIPTION_BARRIER_FILE" "$MOCK_FLOCK_REPLACED"
	mkdir -p "$TMP_ROOT" "$(dirname "$OUTPUT_CONFIG")" "$(dirname "$CACHE_FILE")" \
		"$(dirname "$CONFIG_FILE")"
	: > "$EVENTS"
	printf '%s\n' \
		'https://provider.example/sub?token=complete-secret' \
		'https://backup.example/sub?token=backup-secret' \
		'https://provider.example/sub?token=complete-secret' > "$SUBSCRIPTION_URLS"
	export UCI_main_port=9716
	export UCI_main_enabled=0
	export UCI_main_password='p@ss word&complete'
	export UCI_main_subscription_url='https://provider.example/sub?token=complete-secret'
	export UCI_main_subscription_url_LIST_FILE="$SUBSCRIPTION_URLS"
	export UCI_main_subscription_timeout=60
	export UCI_main_default_template='momo template'
	export UCI_main_cache_dir="$(dirname "$CACHE_FILE")"
	export UCI_main_output_config="$OUTPUT_CONFIG"
	export UCI_TEMPLATE_IDS=momo_template
	export UCI_momo_template_enabled=1
	export MOCK_CONFIG_LOAD_FAIL=0
	export MOCK_GENERATOR_FAIL=0
	export MOCK_INIT_FAIL=0
	export MOCK_INIT_FAIL_AFTER_START=0
	export MOCK_INIT_STOP_FAIL=0
	export MOCK_HEALTH_FAIL=0
	export MOCK_HEALTH_ALWAYS_FAIL=0
	export MOCK_CMP_FAIL=0
	export MOCK_FLOCK_REPLACE_PATH=0
	export MOCK_KILL_AFTER_LOCK_CLAIM=0
	export MOCK_REFRESH_FAIL=0
	export MOCK_FETCH_FAIL=0
	export MOCK_JSON_FAIL=0
	export MOCK_SING_FAIL=0
	export MOCK_GENERATION_MODE=advance
	export MOCK_AFTER_FETCH_GENERATION_MODE=none
	export MOCK_FINAL_RENAME_ACTION=none
	export MOCK_BIND_OUTPUT_TO_GENERATION=0
	export MOCK_SKIP_CACHE_WRITE=0
	export MOCK_BARRIER_ADVANCE=0
	export MOCK_BARRIER_HTTP_CODE=400
	unset MOCK_BARRIER_BODY 2>/dev/null || true
	export MOCK_CONFIG_BODY=gateway-config-v1
	export MOCK_GENERATED_SERIAL=generated
	export MOCK_STARTED_FILE="$RUNTIME/converter.started"
	export MOCK_RUNNING_FILE="$RUNTIME/converter.running"
	printf '%s' "$MOCK_CONFIG_BODY" > "$CONFIG_FILE"
	MOCK_CONFIG_DIGEST=$(sha256sum "$CONFIG_FILE")
	MOCK_CONFIG_DIGEST=${MOCK_CONFIG_DIGEST%% *}
	export MOCK_CONFIG_DIGEST
	MOCK_GENERATION_MODE=advance "$MOCK_ADVANCE_GENERATION" "$MOCK_CONFIG_DIGEST" >/dev/null || exit 1
	INITIAL_GENERATION=$(cat "$CURRENT_FILE")
	cp "$SUBSCRIPTION_STATE/generations/$INITIAL_GENERATION/aggregate.json" "$CACHE_FILE" || exit 1
	chmod 0644 "$CACHE_FILE" || exit 1
	rm -f "$MOCK_STARTED_FILE" "$MOCK_RUNNING_FILE"
	unset MOCK_HOLD_FILE MOCK_ENTERED_FILE 2>/dev/null || true
	unset SBF_STARTUP_WAIT_LIMIT 2>/dev/null || true
	unset SBF_SUBSCRIPTION_LOCK_WAIT_LIMIT 2>/dev/null || true
	unset MOCK_REFRESH_HTTP_CODE 2>/dev/null || true
}

assert_cache_matches_current() {
	local description="$1" generation expected_hash
	generation=$(cat "$CURRENT_FILE") || {
		record_failure "$description (cannot read current generation)"
		return
	}
	expected_hash=$(sha256sum \
		"$SUBSCRIPTION_STATE/generations/$generation/aggregate.json") || {
			record_failure "$description (cannot hash selected aggregate)"
			return
		}
	expected_hash=${expected_hash%% *}
	assert_file_sha256 "$expected_hash" "$CACHE_FILE" "$description"
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
assert_contains "$EVENTS" '--max-time 360' "refresh budgets all three ordered URL occurrences plus every enabled template"
assert_not_equal "$INITIAL_GENERATION" "$(cat "$CURRENT_FILE")" "refresh advances current to a newly committed generation"
assert_equal 1 "$(event_count '^barrier-refresh$')" "refresh synchronizes converter memory after pinning the new generation"
assert_contains "$EVENTS" '^barrier-mode:600$' "refresh publishes a mode-0600 synchronization barrier"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "refresh removes its synchronization barrier before returning"
assert_contains "$UPDATE" 'startup_wait=.*request_timeout' "temporary startup wait derives from the same dynamic request budget"
assert_not_contains "$UPDATE" 'while \[ "\$i" -lt 20 \]' "temporary startup no longer has a fixed 20-second ceiling"
assert_contains "$UPDATE" 'flock -n -x 8' "subscription generation lock acquisition is nonblocking and cancellable"
assert_contains "$UPDATE" 'subscription_lock_fd_identity' "subscription generation lock validates the opened file identity"

reset_mocks
: > "$SUBSCRIPTION_URLS"
export UCI_main_enabled=0
expect_update_success generate \
	"generate allows the documented zero-source configuration while disabled"
assert_equal 1 "$(event_count '^generator-include-disabled:0$')" \
	"disabled zero-source generation reaches only the scrubbed generator path"

for invalid_subscription_url in \
	'http://?query' \
	'https:///path' \
	'http://:80/sub' \
	'http://user@:80/sub' \
	'https://exa%6Dple.com/sub' \
	'https://user[bad]@provider.example/sub' \
	'https://provider|invalid.example/sub' \
	'https://[x:y]/sub' \
	'https://[::::]/sub' \
	'https://[2001::db8::1]/sub' \
	'https://provider.example/raw space' \
	'https://provider.example/sub ' \
	'https://provider.example/%zz' \
	"https://provider.example/sub$(printf '\177')"; do
	reset_mocks
	printf '%s\n' "$invalid_subscription_url" > "$SUBSCRIPTION_URLS"
	expect_update_failure generate \
		"generate rejects invalid subscription URL before side effects: $invalid_subscription_url"
	assert_equal 0 "$(event_count '^generator')" \
		"invalid subscription URL never reaches generator: $invalid_subscription_url"
	assert_equal 0 "$(event_count '^curl:')" \
		"invalid subscription URL never reaches curl: $invalid_subscription_url"
done

for valid_subscription_url in \
	'HTTPS://provider.example/sub' \
	'https://user:pass@provider.example/sub' \
	'https://[2001:db8::1]/sub' \
	'https://[::ffff:192.0.2.1]/sub' \
	'https://[fe80::1%25eth0]/sub' \
	'https://provider.example:0/sub' \
	'https://provider.example:65536/sub' \
	'https://provider.example/sub#fragment' \
	'https://provider.example/sub?opaque=%zz' \
	'https://provider.example/escaped%20space'; do
	reset_mocks
	printf '%s\n' "$valid_subscription_url" > "$SUBSCRIPTION_URLS"
	expect_update_success generate \
		"generate accepts backend-valid subscription URL: $valid_subscription_url"
	assert_equal 1 "$(event_count '^generator-include-disabled:0$')" \
		"backend-valid subscription URL reaches generator unchanged: $valid_subscription_url"
done

reset_mocks
: > "$SUBSCRIPTION_LOCK_FILE"
chmod 0600 "$SUBSCRIPTION_LOCK_FILE"
exec 7<> "$SUBSCRIPTION_LOCK_FILE"
flock -x 7
export SBF_SUBSCRIPTION_LOCK_WAIT_LIMIT=1
expect_update_failure refresh "refresh stops after the bounded subscription lock wait"
assert_contains "$LOG_FILE" 'timed out waiting for the subscription generation lock' "bounded lock timeout records a specific failure"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "lock timeout cannot publish a synchronization barrier"
flock -u 7
exec 7>&-

reset_mocks
export MOCK_FLOCK_REPLACE_PATH=1
expect_update_failure check "check rejects a subscription lock path replaced after open"
assert_contains "$LOG_FILE" 'subscription lock identity changed while acquiring it' "lock replacement reports the identity mismatch"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "replaced lock path cannot publish a synchronization barrier"

reset_mocks
printf 'v1\n' > "$SUBSCRIPTION_BARRIER_FILE"
chmod 0600 "$SUBSCRIPTION_BARRIER_FILE"
expect_update_success refresh "refresh safely recovers a valid marker left by an interrupted updater"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "recovered stale marker is removed after synchronization"

# The updater uses the same checked formulas as the generated server:
# A=S*T+60 and R=A+E*T+60. URL occurrences, including exact duplicates, count
# toward S; every enabled template counts toward E.
reset_mocks
export UCI_TEMPLATE_IDS='momo_template second_template'
export UCI_second_template_enabled=1
expect_update_success refresh "refresh succeeds with a second enabled template"
assert_contains "$EVENTS" '--max-time 420' "the client budget grows by one source timeout for each enabled template"
unset UCI_TEMPLATE_IDS UCI_second_template_enabled

reset_mocks
export UCI_TEMPLATE_IDS='momo_template second_template'
export UCI_second_template_enabled=0
expect_update_success refresh "refresh succeeds with a disabled second template"
assert_contains "$EVENTS" '--max-time 360' "a disabled template costs no client budget"
unset UCI_TEMPLATE_IDS UCI_second_template_enabled

reset_mocks
: > "$SUBSCRIPTION_URLS"
i=1
while [ "$i" -le 8 ]; do
	printf 'https://provider.example/sub-%s?token=secret-%s\n' "$i" "$i" >> "$SUBSCRIPTION_URLS"
	i=$((i + 1))
done
UCI_TEMPLATE_IDS=
i=1
while [ "$i" -le 12 ]; do
	UCI_TEMPLATE_IDS="${UCI_TEMPLATE_IDS}${UCI_TEMPLATE_IDS:+ }template_$i"
	eval "UCI_template_${i}_enabled=1"
	eval "export UCI_template_${i}_enabled"
	i=$((i + 1))
done
export UCI_TEMPLATE_IDS UCI_main_subscription_timeout=600
expect_update_success refresh "refresh accepts an uncapped eight-source, twelve-template request budget"
assert_contains "$EVENTS" '--max-time 12120' "request budget is not truncated by the obsolete 3660-second cap"

# Check / Update 在服务关着时会临时把转换器拉起来。既然是临时的, 结束后必须
# 停掉, 否则界面会停在 "Autostart: Off / Status: Running"。
reset_mocks
export MOCK_HEALTH_FAIL=1
expect_update_success check "check starts the converter when it is not running"
assert_contains "$MOCK_EVENTS" '^init:start manual$' "check starts the converter in manual mode"
assert_contains "$MOCK_EVENTS" '^init:stop$' "check stops the converter it started"
start_lifecycle_token=$(sed -n 's/^init-token:start://p' "$MOCK_EVENTS" | tail -n 1)
stop_lifecycle_token=$(sed -n 's/^init-token:stop://p' "$MOCK_EVENTS" | tail -n 1)
if [ -n "$start_lifecycle_token" ] && [ "$start_lifecycle_token" = "$stop_lifecycle_token" ]; then
	record_ok "temporary start and cleanup stop use the same lifecycle ownership token"
else
	record_failure "temporary start and cleanup stop use the same lifecycle ownership token"
fi
unset MOCK_HEALTH_FAIL

# A failed temporary start happens after the private manual configuration was
# generated. Since no converter is running, cleanup must still restore the
# disabled at-rest configuration instead of leaving saved URLs exposed there.
reset_mocks
export MOCK_HEALTH_FAIL=1 MOCK_INIT_FAIL=1
expect_update_failure check "check reports a failed temporary converter start"
assert_equal 1 "$(event_count '^generator-include-disabled:1$')" \
	"failed temporary start first generates the explicit manual configuration"
assert_equal 1 "$(event_count '^generator-include-disabled:0$')" \
	"failed temporary start restores the disabled at-rest configuration"
assert_file_not_exists "$MOCK_RUNNING_FILE" \
	"failed temporary start leaves no converter running"
unset MOCK_HEALTH_FAIL MOCK_INIT_FAIL

# procd may publish the instance and then report failure while closing the
# service transaction. Since this invocation caused the running process, it
# must retain temporary ownership long enough to stop it and scrub the file.
reset_mocks
export MOCK_HEALTH_FAIL=1 MOCK_INIT_FAIL_AFTER_START=1
expect_update_failure check \
	"check cleans a converter published by a partially failed temporary start"
assert_contains "$MOCK_EVENTS" '^init:stop$' \
	"partial temporary start failure is stopped with this invocation's ownership"
assert_file_not_exists "$MOCK_RUNNING_FILE" \
	"partial temporary start failure leaves no converter running"
assert_equal 1 "$(event_count '^generator-include-disabled:0$')" \
	"partial temporary start failure restores the scrubbed configuration"
unset MOCK_HEALTH_FAIL MOCK_INIT_FAIL_AFTER_START

# 服务本来就在跑的时候不许去动它。
reset_mocks
expect_update_success check "check succeeds against an already running converter"
assert_not_contains "$MOCK_EVENTS" '^init:stop$' "check leaves an already running converter alone"
assert_equal 1 "$(event_count '^generator-include-disabled:1$')" \
	"disabled check gives an existing converter the manual source configuration"
assert_equal 1 "$(event_count '^generator-include-disabled:0$')" \
	"disabled check restores the scrubbed file without stopping an existing converter"

# procd 已经报告 running 但健康端点暂时未就绪时，这个 updater 没有进程 ownership。
# 它可以等待并失败，但绝不能把别的调用者启动的服务当成自己的临时实例停掉。
reset_mocks
export MOCK_HEALTH_ALWAYS_FAIL=1 SBF_STARTUP_WAIT_LIMIT=1
: > "$MOCK_RUNNING_FILE"
expect_update_failure check "check fails cleanly when an existing running converter remains unhealthy"
assert_not_contains "$MOCK_EVENTS" '^init:start manual$' "running but unhealthy converter is not started a second time"
assert_not_contains "$MOCK_EVENTS" '^init:stop$' "running but unhealthy converter is never mistaken for a temporary instance"
assert_file_exists "$MOCK_RUNNING_FILE" "running but unhealthy converter remains running after check cleanup"

# A boot/admin/RPC lifecycle action holding the shared lock wins before this updater.
# The updater must refuse to start rather than perform a stale running=false check and
# later stop the instance published by that other action.
reset_mocks
export MOCK_HEALTH_FAIL=1
mkdir -p "$SBF_LIFECYCLE_LOCK_DIR"
lifecycle_owner_info="$TEST_TMP/lifecycle-owner.info"
sh -c '
	IFS= read -r stat_line < /proc/self/stat || exit 1
	pid=${stat_line%% *}
	start=$(printf "%s\n" "$stat_line" | awk '\''{ line=$0; sub(/^.*\) /, "", line); split(line, field, " "); print field[20] }'\'')
	printf "%s %s\n" "$pid" "$start"
	sleep 30
' > "$lifecycle_owner_info" &
lifecycle_owner_job=$!
n=0
while [ ! -s "$lifecycle_owner_info" ] && [ "$n" -lt 100 ]; do sleep 0.05; n=$((n + 1)); done
read -r lifecycle_owner_pid lifecycle_owner_start < "$lifecycle_owner_info"
printf '%s %s %s\n' "$lifecycle_owner_pid" "$lifecycle_owner_start" other-lifecycle > "$SBF_LIFECYCLE_LOCK_DIR/owner"
expect_update_failure check "check refuses to race a live service lifecycle owner"
assert_not_contains "$MOCK_EVENTS" '^init:start manual$' "busy lifecycle lock prevents a stale temporary start"
assert_not_contains "$MOCK_EVENTS" '^init:stop$' "busy lifecycle lock never grants temporary stop ownership"
kill "$lifecycle_owner_job" 2>/dev/null || true
wait "$lifecycle_owner_job" 2>/dev/null || true
rm -rf "$SBF_LIFECYCLE_LOCK_DIR"

# A temporary converter stop is part of the operation's contract. Reporting success after
# procd rejected the stop leaves a surprising orphan process and hides the only useful error.
reset_mocks
export MOCK_HEALTH_FAIL=1 MOCK_INIT_STOP_FAIL=1
expect_update_failure check "check reports failure when its temporary converter cannot be stopped"
assert_contains "$LOG_FILE" 'failed to stop the converter that this run started' "records a temporary converter stop failure"
assert_file_exists "$MOCK_RUNNING_FILE" "failed stop leaves the mock converter state visible"
assert_equal 1 "$(event_count '^generator-include-disabled:0$')" \
	"failed temporary stop still restores the scrubbed on-disk configuration"

# 输出路径若已是同名目录, mv 会把临时文件搬进去并成功返回, Update 就会报成功
# 而实际什么也没产出。
reset_mocks
mkdir -p "$OUTPUT_CONFIG"
expect_update_failure apply "apply refuses a directory at the output path"
assert_contains "$LOG_FILE" 'refusing directory output path' "the directory is named in the update log"
rmdir "$OUTPUT_CONFIG"

reset_mocks
mkfifo "$OUTPUT_CONFIG" 2>/dev/null && {
	expect_update_failure apply "apply refuses a non regular file at the output path"
	rm -f "$OUTPUT_CONFIG"
}
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
failed_generation_before=$(cat "$CURRENT_FILE")
failed_status_before=$(sha256sum "$SUBSCRIPTION_STATE/generations/$failed_generation_before/status.json")
failed_status_before=${failed_status_before%% *}
expect_update_failure refresh "refresh fails when the converter returns a server error"
assert_contains "$LOG_FILE" 'HTTP 500' "records the 500 status in the update log"
assert_contains "$LOG_FILE" 'source_unavailable' "records the bounded gateway failure code"
assert_contains "$LOG_FILE" '"failure_stage":"source_fetch"' "records the bounded gateway failure stage"
assert_contains "$LOG_FILE" '"fetch_code":"http_status"' "records the bounded source fetch classification"
assert_contains "$LOG_FILE" '"source_index":2' "records the failed ordered source index"
assert_contains "$LOG_FILE" '"preserved":true' "records that the prior complete generation was preserved"
assert_not_contains "$LOG_FILE" 'provider\.example|backup\.example|complete-secret|backup-secret|momo template' "failure diagnostics never log source URLs, tokens, or node names"
assert_file_content "$failed_generation_before" "$CURRENT_FILE" "complete refresh failure keeps the selected generation"
assert_file_sha256 "$failed_status_before" "$SUBSCRIPTION_STATE/generations/$failed_generation_before/status.json" "complete refresh failure keeps immutable selected status"
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

# Every converter refresh flow must observe a newly selected, fully valid
# generation for this exact generated-config digest. A stale converter snapshot
# is not success even when the converter HTTP endpoint itself returned 2xx.
for command in refresh check apply; do
	for generation_mode in no-advance missing-current invalid-current wrong-config invalid-generation; do
		reset_mocks
		printf 'old-output\n' > "$OUTPUT_CONFIG"
		export MOCK_GENERATION_MODE=$generation_mode
		expect_update_failure \
			"$command" \
			"$command rejects generation state: $generation_mode"
		assert_file_content \
			old-output \
			"$OUTPUT_CONFIG" \
			"$command preserves installed output for generation state: $generation_mode"
	done
done

reset_mocks
export MOCK_GENERATION_MODE=advance-twice
expect_update_failure refresh "refresh rejects a concurrent generation whose converter snapshot was not committed"
assert_not_equal "$INITIAL_GENERATION" "$(cat "$CURRENT_FILE")" "concurrent same-config winner leaves a newer valid current"

reset_mocks
export MOCK_SKIP_CACHE_WRITE=1
expect_update_failure refresh "refresh rejects a generation whose converter cache was not committed"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "failed refresh removes its synchronization barrier"

reset_mocks
export MOCK_BARRIER_HTTP_CODE=500
expect_update_failure check "check rejects an unexpected synchronization sentinel response"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "unexpected sentinel response is cleaned after failure"

reset_mocks
export MOCK_BARRIER_BODY="Template Error: template 'attacker' not found in configuration"
expect_update_failure check "check rejects a nonexact synchronization sentinel body"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "invalid sentinel content is cleaned after failure"

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_BARRIER_ADVANCE=1
expect_update_failure apply "apply rejects a pre-existing refresh that finishes after the generation was pinned"
assert_file_content old-output "$OUTPUT_CONFIG" "late refresh completion preserves installed output"
assert_file_not_exists "$SUBSCRIPTION_BARRIER_FILE" "late-refresh failure removes its synchronization barrier"

reset_mocks
printf 'unsafe\n' > "$RUNTIME/barrier-target"
ln -s "$RUNTIME/barrier-target" "$SUBSCRIPTION_BARRIER_FILE"
expect_update_failure check "check refuses an unsafe synchronization barrier path"
[ -L "$SUBSCRIPTION_BARRIER_FILE" ] &&
	record_ok "unsafe barrier symlink is not followed or replaced" ||
	record_failure "unsafe barrier symlink is not followed or replaced"
rm -f "$SUBSCRIPTION_BARRIER_FILE"

reset_mocks
export MOCK_HEALTH_FAIL=1
expect_update_failure refresh "refresh fails when a disabled converter is stopped"
assert_equal 0 "$(event_count '^generator$')" "stopped refresh never invokes the generator"
assert_equal 0 "$(event_count '^init:')" "stopped refresh never auto-starts the converter"
assert_equal 0 "$(grep -F -c '/refresh?' "$EVENTS" 2>/dev/null || true)" "stopped refresh never calls the refresh endpoint"

reset_mocks
export MOCK_HEALTH_FAIL=1
expect_update_success check "check may auto-start a stopped disabled converter"
assert_equal 2 "$(event_count '^generator$')" "stopped disabled check generates one manual config and one scrubbed config"
assert_equal 1 "$(event_count '^generator-include-disabled:1$')" "stopped disabled check exposes saved URLs only to its manual config"
assert_equal 1 "$(event_count '^generator-include-disabled:0$')" "stopped disabled check restores the scrubbed at-rest config after stopping"
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
# A busy lock is by far the most common failure, and it happens before any
# command-specific work. It must still reach the update log, otherwise the UI
# points the user at a file that was never created.
assert_contains "$LOG_FILE" 'another update operation is already running' "logs a busy lock instead of failing silently"
assert_file_exists "$LOG_FILE" "creates the update log even when locking fails"
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
assert_equal 1 "$refresh_calls" "only the lock winner reaches the manual refresh endpoint"
assert_equal 1 "$(event_count '^barrier-refresh$')" "only the lock winner reaches the query-refresh synchronization fence"
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
assert_cache_matches_current "JSON failure retains the complete refreshed converter snapshot"
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
export MOCK_BIND_OUTPUT_TO_GENERATION=1 MOCK_AFTER_FETCH_GENERATION_MODE=advance
expect_update_failure apply "apply rejects a same-config generation advance during bound output fetch"
assert_file_content old-output "$OUTPUT_CONFIG" "fetch-time generation advance preserves installed output"
served_generation=$(sed -n 's/^served-generation://p' "$EVENTS" | tail -n 1)
assert_not_equal "$served_generation" "$(cat "$CURRENT_FILE")" "fetch fixture advances current beyond the generation that served output"

for after_fetch_mode in wrong-config missing-current invalid-current invalid-generation; do
	reset_mocks
	printf 'old-output\n' > "$OUTPUT_CONFIG"
	export MOCK_AFTER_FETCH_GENERATION_MODE=$after_fetch_mode
	expect_update_failure \
		apply \
		"apply revalidates current before install: $after_fetch_mode"
	assert_file_content \
		old-output \
		"$OUTPUT_CONFIG" \
		"unstable generation state preserves output before install: $after_fetch_mode"
done

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_BIND_OUTPUT_TO_GENERATION=1 MOCK_FINAL_RENAME_ACTION=advance-same-config
expect_update_failure apply "apply rejects a bound generation advance before atomic output replacement"
assert_file_content old-output "$OUTPUT_CONFIG" "pre-install generation advance preserves installed output"
served_generation=$(sed -n 's/^served-generation://p' "$EVENTS" | tail -n 1)
assert_not_equal "$served_generation" "$(cat "$CURRENT_FILE")" "install fixture advances current beyond the generation that served output"

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_FINAL_RENAME_ACTION=fail
expect_update_failure apply "final-output fault injection aborts before atomic replacement"
assert_file_content old-output "$OUTPUT_CONFIG" "final-output fault preserves the installed output"
assert_file_content before_final_output_rename "$FAULT_LOG" "invokes only the named final-output fault stage"
sing_line=$(grep -n '^sing-box:' "$EVENTS" | tail -n 1 | cut -d: -f1)
fault_line=$(grep -n '^fault:before_final_output_rename$' "$EVENTS" | tail -n 1 | cut -d: -f1)
if [ -n "$sing_line" ] && [ -n "$fault_line" ] && [ "$sing_line" -lt "$fault_line" ]; then
	record_ok 'fault hook runs after generated output validation'
else
	record_failure 'fault hook runs after generated output validation'
fi

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
printf 'last-known-good-cache\n' > "$CACHE_FILE"
export MOCK_CMP_FAIL=1 MOCK_GENERATED_SERIAL=new-output
expect_update_failure apply "treats updater cmp I/O failure as fatal"
assert_file_content old-output "$OUTPUT_CONFIG" "updater cmp failure preserves installed output"
assert_cache_matches_current "updater cmp failure retains the complete refreshed converter snapshot"

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_SKIP_CACHE_WRITE=1
expect_update_failure apply "rejects a converter snapshot that does not match the selected generation"
assert_file_content old-output "$OUTPUT_CONFIG" "mismatched converter snapshot preserves installed output"

reset_mocks
printf 'old-output\n' > "$OUTPUT_CONFIG"
export MOCK_REFRESH_FAIL=1 MOCK_GENERATED_SERIAL=from-cache
expect_update_failure apply "apply rejects a stale converter snapshot after refresh failure"
assert_file_content old-output "$OUTPUT_CONFIG" "refresh failure preserves the installed output"
assert_not_contains "$LOG_FILE" 'trying cached data' "updater no longer treats converter cache fallback as refresh success"
assert_equal 0 "$(grep -c 'generated\.json' "$EVENTS" 2>/dev/null || true)" "refresh failure stops before fetching cached converter output"

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
