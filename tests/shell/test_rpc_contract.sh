#!/bin/sh
set -u

REPO_ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
. "$REPO_ROOT/tests/shell/harness.sh"

RPC="$REPO_ROOT/openwrt-feed/luci-app-singbox-formula/root/usr/libexec/rpcd/singbox_formula"
ACL="$REPO_ROOT/openwrt-feed/luci-app-singbox-formula/root/usr/share/rpcd/acl.d/luci-app-singbox-formula.json"
OVERVIEW="$REPO_ROOT/openwrt-feed/luci-app-singbox-formula/root/www/luci-static/resources/view/singbox-formula/overview.js"
MAKEFILE="$REPO_ROOT/openwrt-feed/luci-app-singbox-formula/Makefile"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
: > "$TMP/functions.sh"
cat > "$TMP/updater" <<'EOF'
#!/bin/sh
sleep 2
printf 'run\n' >> "$SBF_TEST_RUNS"
EOF
chmod 0755 "$TMP/updater"
cat > "$TMP/process-start" <<'EOF'
#!/bin/sh
[ "$1" = 999999 ] && exit 1
printf '%s\n' "$1"
EOF
chmod 0755 "$TMP/process-start"

SBF_FUNCTIONS_SH="$TMP/functions.sh" "$RPC" list > "$TMP/list.json"
assert_command_success 'rpc list is valid JSON' python3 -m json.tool "$TMP/list.json"
for method in status service_action generate refresh check update list_templates read_template write_template delete_template; do
	assert_contains "$TMP/list.json" "\"$method\"" "publishes narrow RPC method: $method"
done
assert_not_contains "$TMP/list.json" '"action"' 'does not publish the legacy generic action method'
assert_not_contains "$ACL" '"file"' 'ACL grants no generic file capability'
assert_not_contains "$MAKEFILE" 'rpcd-mod-file' 'LuCI package no longer depends on rpcd-mod-file'
assert_contains "$RPC" 'STATE_DIR=.*\/var/run\/singbox-formula' 'runtime action state lives under /var/run'
assert_contains "$RPC" 'chmod 0700 "\$STATE_DIR"' 'runtime action state is root-only'
assert_not_contains "$RPC" 'pgrep' 'RPC service state never depends on pgrep'
assert_contains "$RPC" 'start manual' 'manual service starts bypass boot delay explicitly'
assert_contains "$RPC" 'ACTION_TIMEOUT=900' 'background RPC workers have a 900-second bound'
assert_contains "$RPC" "service.*singbox-subscribe-convert" 'health response verifies converter identity'
assert_contains "$RPC" 'config_digest' 'status exposes a content digest'
assert_not_contains "$OVERVIEW" "method: 'action'" 'Overview uses split RPC methods'
assert_contains "$OVERVIEW" 'typeof res.code !== .number.' 'frontend rejects a missing or nonnumeric result code'
assert_contains "$OVERVIEW" "out !== 'queued'|Invalid asynchronous response" 'frontend rejects nonexact asynchronous acknowledgements'
assert_contains "$OVERVIEW" 'config_digest' 'Save & Apply is digest-driven'
assert_not_contains "$OVERVIEW" 'config_mtime' 'Save & Apply no longer coordinates by second-resolution mtime'
assert_contains "$OVERVIEW" "_\('Converter URL \(this device\)'\)" 'integration exposes the loopback converter URL'
assert_contains "$OVERVIEW" "_\('Converter URL \(LAN\)'\)" 'integration exposes the LAN converter URL'
assert_contains "$RPC" 'lan_url' 'status exposes a LAN converter URL'
assert_contains "$RPC" '_valid_ipv4' 'status validates the LAN address before publishing it'

mkdir "$TMP/responses"
i=1
while [ "$i" -le 20 ]; do
	SBF_FUNCTIONS_SH="$TMP/functions.sh" \
	SBF_UPDATER="$TMP/updater" \
	SBF_STATE_DIR="$TMP/state" \
	SBF_TEST_RUNS="$TMP/runs" \
	SBF_PROCESS_START_HELPER="$TMP/process-start" \
		"$RPC" call refresh </dev/null > "$TMP/responses/$i" &
	i=$((i + 1))
done
wait
n=0
while [ ! -f "$TMP/runs" ] && [ "$n" -lt 5 ]; do
	sleep 1
	n=$((n + 1))
done
queued=$(grep -l '"code":0,"output":"queued"' "$TMP"/responses/* 2>/dev/null | wc -l | tr -d '[:space:]')
busy=$(grep -l '"code":75' "$TMP"/responses/* 2>/dev/null | wc -l | tr -d '[:space:]')
assert_equal 1 "$queued" 'twenty parallel background calls produce exactly one lock winner'
assert_equal 19 "$busy" 'all parallel lock losers fail explicitly'
assert_file_line_count 1 "$TMP/runs" 'only the RPC lock winner reaches the updater'
assert_equal 700 "$(stat -c %a "$TMP/state")" 'runtime state directory is mode 0700'
assert_file_not_exists "$TMP/state/rpc-action.lock" 'worker releases the action lock after completion'
assert_contains "$TMP/state/action.state" '^refresh done 0 [0-9]+ [A-Za-z0-9._-]+$' 'worker publishes a complete atomic terminal state'

mkdir "$TMP/state/rpc-action.lock"
printf '999999 999999 stale-owner\n' > "$TMP/state/rpc-action.lock/owner"
SBF_FUNCTIONS_SH="$TMP/functions.sh" \
SBF_UPDATER="$TMP/updater" \
SBF_STATE_DIR="$TMP/state" \
SBF_TEST_RUNS="$TMP/runs" \
SBF_PROCESS_START_HELPER="$TMP/process-start" \
	"$RPC" call update </dev/null > "$TMP/stale-recovery.json"
assert_contains "$TMP/stale-recovery.json" '"code":0,"output":"queued"' 'a dead owner is recovered before queueing the next worker'
n=0
while [ "$(wc -l < "$TMP/runs")" -lt 2 ] && [ "$n" -lt 5 ]; do sleep 1; n=$((n + 1)); done
assert_file_line_count 2 "$TMP/runs" 'recovered dead ownership still runs exactly one new updater'

finish_tests
